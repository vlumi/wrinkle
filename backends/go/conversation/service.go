// Package conversation is the conversation service: sending, read marks and
// history, against the conversation keyspace. Every change writes an
// outbox row in the same transaction, which change data capture carries to
// the fan-out.
package conversation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
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
)

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Service {
	return &Service{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// Ensure creates the conversation between two users if it does not exist.
func (s *Service) Ensure(ctx context.Context, conversationID, userA, userB uint64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT IGNORE INTO conversation (conversation_id, user_a, user_b, last_seq, created_at)
		VALUES (?, ?, ?, 0, ?)`,
		conversationID, userA, userB, s.now())
	return err
}

type header struct {
	userA, userB, lastSeq uint64
}

func (h header) has(userID uint64) bool {
	return userID == h.userA || userID == h.userB
}

const (
	selectHeader          = `SELECT user_a, user_b, last_seq FROM conversation WHERE conversation_id = ?`
	selectHeaderForUpdate = selectHeader + ` FOR UPDATE`
)

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// member reads the conversation's header with the given query and checks
// that the user is a participant.
func member(ctx context.Context, q rowQuerier, query string, conversationID, userID uint64) (header, error) {
	var h header
	err := q.QueryRowContext(ctx, query, conversationID).Scan(&h.userA, &h.userB, &h.lastSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return h, apierror.NotFound("conversation %d does not exist", conversationID)
	}
	if err != nil {
		return h, err
	}
	if !h.has(userID) {
		return h, apierror.PermissionDenied("user %d is not in conversation %d", userID, conversationID)
	}
	return h, nil
}

// Send stores a message, assigning the next sequence number. Resending with
// the same client message id returns the message stored the first time.
func (s *Service) Send(ctx context.Context, userID, conversationID uint64, clientMessageID, text string) (*pb.Message, error) {
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

	h, err := member(ctx, tx, selectHeaderForUpdate, conversationID, userID)
	if err != nil {
		return nil, err
	}
	msg := &pb.Message{
		ConversationId:  conversationID,
		SenderUserId:    userID,
		ClientMessageId: clientMessageID,
		Text:            text,
	}
	var sentAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT seq, body, sent_at FROM message
		WHERE conversation_id = ? AND sender_user_id = ? AND client_message_id = ?`,
		conversationID, userID, clientMessageID).Scan(&msg.Seq, &msg.Text, &sentAt)
	if err == nil {
		msg.SentAt = timestamppb.New(sentAt)
		return msg, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	sentAt = s.now()
	msg.Seq = h.lastSeq + 1
	msg.SentAt = timestamppb.New(sentAt)
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation SET last_seq = ? WHERE conversation_id = ?`,
		msg.Seq, conversationID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO message (conversation_id, seq, sender_user_id, client_message_id, body, sent_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		conversationID, msg.Seq, userID, clientMessageID, text, sentAt); err != nil {
		return nil, err
	}
	event := &pb.Event{
		OccurredAt: msg.SentAt,
		Body:       &pb.Event_Message{Message: &pb.MessageEvent{Message: msg}},
	}
	if err := insertOutbox(ctx, tx, h, conversationID, "message", event, sentAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return msg, nil
}

// MarkRead moves the user's read position forward. Moving it backwards or
// to where it is does nothing.
func (s *Service) MarkRead(ctx context.Context, userID, conversationID, seq uint64) error {
	if seq == 0 {
		return apierror.InvalidArgument("seq must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	h, err := member(ctx, tx, selectHeaderForUpdate, conversationID, userID)
	if err != nil {
		return err
	}
	if seq > h.lastSeq {
		return apierror.InvalidArgument("seq %d is beyond the last message %d", seq, h.lastSeq)
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
	event := &pb.Event{
		OccurredAt: timestamppb.New(now),
		Body: &pb.Event_Read{Read: &pb.ReadEvent{
			ConversationId: conversationID, UserId: userID, Seq: seq,
		}},
	}
	if err := insertOutbox(ctx, tx, h, conversationID, "read", event, now); err != nil {
		return err
	}
	return tx.Commit()
}

// Messages returns messages with seq below beforeSeq, newest first.
func (s *Service) Messages(ctx context.Context, userID, conversationID, beforeSeq uint64, limit uint32) ([]*pb.Message, error) {
	if _, err := member(ctx, s.db, selectHeader, conversationID, userID); err != nil {
		return nil, err
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
		WHERE conversation_id = ? AND seq < ? ORDER BY seq DESC LIMIT ?`,
		conversationID, beforeSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []*pb.Message
	for rows.Next() {
		msg := &pb.Message{ConversationId: conversationID}
		var sentAt time.Time
		if err := rows.Scan(&msg.Seq, &msg.SenderUserId, &msg.ClientMessageId, &msg.Text, &sentAt); err != nil {
			return nil, err
		}
		msg.SentAt = timestamppb.New(sentAt)
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

func insertOutbox(ctx context.Context, tx *sql.Tx, h header, conversationID uint64, kind string, event *pb.Event, at time.Time) error {
	payload, err := proto.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal outbox event: %w", err)
	}
	outboxID := ids.Random()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO outbox (conversation_id, outbox_id, user_a, user_b, kind, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		conversationID, outboxID, h.userA, h.userB, kind, payload, at); err != nil {
		return err
	}
	// The insert reaches the binlog, which is all the fan-out reads; deleting
	// the row in the same transaction keeps the table empty.
	_, err = tx.ExecContext(ctx, `
		DELETE FROM outbox WHERE conversation_id = ? AND outbox_id = ?`,
		conversationID, outboxID)
	return err
}
