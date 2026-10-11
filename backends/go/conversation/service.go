// Package conversation is the conversation service: direct conversations
// and groups, sending, read marks and history, against the conversation
// keyspace. Every change writes an outbox row in the same transaction,
// which change data capture carries to the fan-out.
package conversation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	maxClientMessageID = 64
	maxTextLength      = 4000
	defaultPageSize    = 50
	maxPageSize        = 500
	// readSendersWindow bounds the messages a group read event looks at
	// for the senders to tell.
	readSendersWindow = 1000
)

// Profiles reads users' current profiles from the user keyspace, for the
// snapshots in notices and member lists.
type Profiles interface {
	Profiles(ctx context.Context, userIDs []ids.UUID) (map[ids.UUID]*pb.User, error)
}

type Service struct {
	db       *sql.DB
	profiles Profiles
	now      func() time.Time
}

func New(db *sql.DB, profiles Profiles) *Service {
	return &Service{db: db, profiles: profiles, now: func() time.Time { return time.Now().UTC() }}
}

// Open finds or creates the direct conversation between two users, user_a
// < user_b. The pair row, on user_a's shard, decides between two opening at
// once; a pair whose conversation row is missing gets it here.
func (s *Service) Open(ctx context.Context, userA, userB ids.UUID) (ids.UUID, error) {
	conversationID, found, err := s.pair(ctx, userA, userB)
	if err != nil {
		return conversationID, err
	}
	if !found {
		conversationID = ids.New()
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO conversation_pair (user_a, user_b, conversation_id, created_at) VALUES (?, ?, ?, ?)`,
			userA, userB, conversationID, s.now())
		if err != nil {
			if !isDuplicate(err) {
				return conversationID, err
			}
			if conversationID, found, err = s.pair(ctx, userA, userB); err != nil {
				return conversationID, err
			}
			if !found {
				return conversationID, fmt.Errorf("conversation pair missing after a duplicate key")
			}
		}
	}
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `
		INSERT IGNORE INTO conversation (conversation_id, kind, topic, members, last_seq, created_at)
		VALUES (?, ?, '', ?, 0, ?)`,
		conversationID, kindDirect, encodeMembers([]ids.UUID{userA, userB}), now); err != nil {
		return conversationID, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT IGNORE INTO conversation_member (conversation_id, user_id, admin, joined_seq, left_seq, joined_at)
		VALUES (?, ?, FALSE, 1, NULL, ?), (?, ?, FALSE, 1, NULL, ?)`,
		conversationID, userA, now, conversationID, userB, now)
	return conversationID, err
}

func (s *Service) pair(ctx context.Context, userA, userB ids.UUID) (ids.UUID, bool, error) {
	var conversationID ids.UUID
	err := s.db.QueryRowContext(ctx, `
		SELECT conversation_id FROM conversation_pair WHERE user_a = ? AND user_b = ?`,
		userA, userB).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return conversationID, false, nil
	}
	return conversationID, err == nil, err
}

// MySQL error for a unique key violation.
const duplicateKey = 1062

func isDuplicate(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == duplicateKey
}

// Send stores a message, assigning the next sequence number. Resending with
// the same client message id returns the message stored the first time.
func (s *Service) Send(ctx context.Context, userID, conversationID ids.UUID, clientMessageID, text string) (*pb.Message, error) {
	if clientMessageID == "" || len(clientMessageID) > maxClientMessageID {
		return nil, apierror.InvalidArgument("client_message_id must be 1 to %d bytes", maxClientMessageID)
	}
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > maxTextLength {
		return nil, apierror.InvalidArgument("text must be valid UTF-8 and 1 to %d characters", maxTextLength)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	st, err := member(ctx, tx, selectStateForUpdate, conversationID, userID)
	if err != nil {
		return nil, err
	}
	msg := &pb.Message{
		ConversationId:  conversationID.Bytes(),
		SenderUserId:    userID.Bytes(),
		ClientMessageId: clientMessageID,
	}
	var body []byte
	var sentAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT seq, body, sent_at FROM message
		WHERE conversation_id = ? AND sender_user_id = ? AND client_message_id = ?`,
		conversationID, userID, clientMessageID).Scan(&msg.Seq, &body, &sentAt)
	if err == nil {
		msg.SentAt = timestamppb.New(sentAt)
		return msg, decodeBody(body, msg)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	msg.Content = &pb.Message_Text{Text: text}
	st.lastSeq++
	if err := s.store(ctx, tx, conversationID, st, msg, st.members, false); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return msg, nil
}

// MarkRead moves the user's read position forward. Moving it backwards or
// to where it is does nothing. In a group, the read event goes to the
// reader and to the senders of the messages it covers.
func (s *Service) MarkRead(ctx context.Context, userID, conversationID ids.UUID, seq uint64) error {
	if seq == 0 {
		return apierror.InvalidArgument("seq must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	st, err := member(ctx, tx, selectStateForUpdate, conversationID, userID)
	if err != nil {
		return err
	}
	if seq > st.lastSeq {
		return apierror.InvalidArgument("seq %d is beyond the last message %d", seq, st.lastSeq)
	}
	var current uint64
	err = tx.QueryRowContext(ctx, `
		SELECT seq FROM read_mark WHERE conversation_id = ? AND user_id = ? FOR UPDATE`,
		conversationID, userID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current >= seq {
		return nil
	}
	now := s.now()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO read_mark (conversation_id, user_id, seq, updated_at) VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE seq = ?, updated_at = ?`,
		conversationID, userID, seq, now, seq, now); err != nil {
		return err
	}
	recipients := st.members
	if st.kind == kindGroup {
		if recipients, err = readRecipients(ctx, tx, st, conversationID, userID, current, seq); err != nil {
			return err
		}
	}
	event := &pb.Event{
		OccurredAt: timestamppb.New(now),
		Body: &pb.Event_Read{Read: &pb.ReadEvent{
			ConversationId: conversationID.Bytes(), UserId: userID.Bytes(), Seq: seq,
		}},
	}
	if err := insertOutbox(ctx, tx, conversationID, ids.Random(), recipients, st, "read", event, now); err != nil {
		return err
	}
	return tx.Commit()
}

// readRecipients is the reader and the current members among the senders
// of the newest messages in (from, to].
func readRecipients(ctx context.Context, tx *sql.Tx, st state, conversationID, readerID ids.UUID, from, to uint64) ([]ids.UUID, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT sender_user_id FROM (
			SELECT sender_user_id FROM message
			WHERE conversation_id = ? AND seq > ? AND seq <= ? ORDER BY seq DESC LIMIT ?) AS m`,
		conversationID, from, to, readSendersWindow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recipients := []ids.UUID{readerID}
	for rows.Next() {
		var sender ids.UUID
		if err := rows.Scan(&sender); err != nil {
			return nil, err
		}
		if sender != readerID && st.has(sender) {
			recipients = append(recipients, sender)
		}
	}
	return recipients, rows.Err()
}

// Messages returns messages with seq below beforeSeq, newest first: in a
// group, those since the user joined and, for a former member, until they
// left.
func (s *Service) Messages(ctx context.Context, userID, conversationID ids.UUID, beforeSeq uint64, limit uint32) ([]*pb.Message, error) {
	var joinedSeq uint64
	var leftSeq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT joined_seq, left_seq FROM conversation_member WHERE conversation_id = ? AND user_id = ?`,
		conversationID, userID).Scan(&joinedSeq, &leftSeq)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := load(ctx, s.db, selectState, conversationID); err != nil {
			return nil, err
		}
		return nil, apierror.PermissionDenied("user %s is not in conversation %s", userID, conversationID)
	}
	if err != nil {
		return nil, err
	}
	upTo := uint64(math.MaxUint64)
	if leftSeq.Valid {
		upTo = uint64(leftSeq.Int64)
	}
	if beforeSeq == 0 {
		beforeSeq = math.MaxUint64
	}
	if limit == 0 {
		limit = defaultPageSize
	}
	limit = min(limit, maxPageSize)
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, sender_user_id, client_message_id, body, sent_at FROM message
		WHERE conversation_id = ? AND seq >= ? AND seq <= ? AND seq < ? ORDER BY seq DESC LIMIT ?`,
		conversationID, joinedSeq, upTo, beforeSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []*pb.Message
	for rows.Next() {
		msg := &pb.Message{ConversationId: conversationID.Bytes()}
		var body []byte
		var sentAt time.Time
		if err := rows.Scan(&msg.Seq, &msg.SenderUserId, &msg.ClientMessageId, &body, &sentAt); err != nil {
			return nil, err
		}
		msg.SentAt = timestamppb.New(sentAt)
		if err := decodeBody(body, msg); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}
