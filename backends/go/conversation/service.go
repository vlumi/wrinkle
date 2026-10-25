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
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	maxClientMessageID = 64
	nonceBytes         = 12
	maxCiphertext      = 48 << 10
	maxBlobIDs         = 10
	defaultPageSize    = 50
	maxPageSize        = 500
	// readSendersWindow bounds the messages a group read event looks at
	// for the senders to tell.
	readSendersWindow = 1000
	releaseTimeout    = 5 * time.Second
)

// Profiles reads users' current profiles from the user keyspace, for the
// snapshots in notices and member lists.
type Profiles interface {
	Profiles(ctx context.Context, userIDs []ids.UUID) (map[ids.UUID]*pb.User, error)
}

// Attachments takes the uploads of a message's attachments into use, and
// releases them once it is deleted, in the user keyspace.
type Attachments interface {
	UseAttachments(ctx context.Context, userID ids.UUID, blobIDs []ids.UUID) error
	ReleaseAttachments(ctx context.Context, userID ids.UUID, blobIDs []ids.UUID) error
}

// Blocks refuses messaging between two users when either blocked the
// other, from the user keyspace.
type Blocks interface {
	CheckMessaging(ctx context.Context, userID, peerID ids.UUID) error
}

type Service struct {
	db          *sql.DB
	profiles    Profiles
	keys        DeviceKeys
	attachments Attachments
	blocks      Blocks
	cold        *Cold
	direct      directMembers
}

func New(db *sql.DB, profiles Profiles, keys DeviceKeys, attachments Attachments, blocks Blocks, cold *Cold) *Service {
	return &Service{db: db, profiles: profiles, keys: keys, attachments: attachments, blocks: blocks, cold: cold}
}

// Open finds or creates the direct conversation between two users, user_a
// < user_b. The pair row, on user_a's shard, decides between two opening at
// once; a pair whose conversation row is missing gets it here.
func (s *Service) Open(ctx context.Context, userA, userB ids.UUID) (ids.UUID, error) {
	return s.open(ctx, kindDirect, userA, userB)
}

// OpenNotes finds or creates the user's notes: the pair of the user with
// themselves, decided by the pair row as a direct conversation is.
func (s *Service) OpenNotes(ctx context.Context, userID ids.UUID) (ids.UUID, error) {
	return s.open(ctx, kindNotes, userID, userID)
}

func (s *Service) open(ctx context.Context, kind string, userA, userB ids.UUID) (ids.UUID, error) {
	conversationID, found, err := s.pair(ctx, userA, userB)
	if err != nil {
		return conversationID, err
	}
	if !found {
		conversationID = ids.New()
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO conversation_pair (user_a, user_b, conversation_id, created_at) VALUES (?, ?, ?, ?)`,
			userA, userB, conversationID, vtdb.Now())
		if err != nil {
			if !vtdb.IsDuplicate(err) {
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
	members := slices.Compact([]ids.UUID{userA, userB})
	now := vtdb.Now()
	if _, err := s.db.ExecContext(ctx, `
		INSERT IGNORE INTO conversation (conversation_id, kind, topic, members, last_seq, created_at)
		VALUES (?, ?, '', ?, 0, ?)`,
		conversationID, kind, encodeMembers(members), now); err != nil {
		return conversationID, err
	}
	for _, id := range members {
		if _, err := s.db.ExecContext(ctx, `
			INSERT IGNORE INTO conversation_member (conversation_id, user_id, admin, joined_seq, left_seq, joined_at)
			VALUES (?, ?, FALSE, 1, NULL, ?)`,
			conversationID, id, now); err != nil {
			return conversationID, err
		}
	}
	return conversationID, nil
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

// Send stores a sealed message, assigning the next sequence number.
// Resending with the same client message id returns the message stored the
// first time; otherwise the content must be sealed with the current key
// epoch, and none is current while the conversation needs a new one. The
// attachments' uploads are taken into use first, in the user keyspace, and
// released again if the send then fails. An upload goes with one message
// only.
func (s *Service) Send(ctx context.Context, userID, conversationID ids.UUID, clientMessageID string, sealed *pb.Sealed, blobIDs [][]byte) (msg *pb.Message, err error) {
	if clientMessageID == "" || len(clientMessageID) > maxClientMessageID {
		return nil, apierror.InvalidArgument("client_message_id must be 1 to %d bytes", maxClientMessageID)
	}
	if len(sealed.GetNonce()) != nonceBytes {
		return nil, apierror.InvalidArgument("sealed.nonce must be %d bytes", nonceBytes)
	}
	if n := len(sealed.GetCiphertext()); n == 0 || n > maxCiphertext {
		return nil, apierror.InvalidArgument("sealed.ciphertext must be 1 to %d bytes", maxCiphertext)
	}
	blobs, err := parseBlobIDs(blobIDs)
	if err != nil {
		return nil, err
	}
	if err := s.checkBlocks(ctx, userID, conversationID); err != nil {
		return nil, err
	}
	if len(blobs) > 0 {
		used, err := s.useAttachments(ctx, userID, conversationID, clientMessageID, blobs)
		if err != nil {
			return nil, err
		}
		if used {
			defer func() {
				if err != nil {
					s.releaseAttachments(ctx, userID, blobs)
				}
			}()
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	st, err := member(ctx, tx, conversationID, userID)
	if err != nil {
		return nil, err
	}
	msg = &pb.Message{
		ConversationId:  conversationID.Bytes(),
		SenderUserId:    userID.Bytes(),
		ClientMessageId: clientMessageID,
	}
	var body, storedBlobs []byte
	var sentAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT seq, body, blob_ids, sent_at FROM message
		WHERE conversation_id = ? AND sender_user_id = ? AND client_message_id = ?`,
		conversationID, userID, clientMessageID).Scan(&msg.Seq, &body, &storedBlobs, &sentAt)
	if err == nil {
		msg.SentAt = timestamppb.New(sentAt)
		msg.BlobIds = decodeBlobIDs(storedBlobs)
		return msg, decodeBody(body, msg)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	switch {
	case st.keyEpoch == 0 || sealed.GetEpoch() != st.keyEpoch:
		return nil, apierror.FailedPrecondition("stale key epoch: the current one is %d", st.keyEpoch)
	case st.rotate:
		return nil, apierror.FailedPrecondition("keys must rotate: the membership changed since epoch %d", st.keyEpoch)
	}
	msg.Content = &pb.Message_Sealed{Sealed: &pb.Sealed{
		Epoch: sealed.GetEpoch(), Nonce: sealed.GetNonce(), Ciphertext: sealed.GetCiphertext(),
	}}
	for _, id := range blobs {
		msg.BlobIds = append(msg.BlobIds, id.Bytes())
	}
	st.lastSeq++
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation SET last_seq = ? WHERE conversation_id = ?`, st.lastSeq, conversationID); err != nil {
		return nil, err
	}
	if err := storeMessage(ctx, tx, conversationID, st, msg, st.members); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return msg, nil
}

// checkBlocks refuses a message or reaction in a direct conversation when
// either user blocked the other. The row is read without a lock, before
// anything is taken into use; the change's transaction checks membership
// again.
func (s *Service) checkBlocks(ctx context.Context, userID, conversationID ids.UUID) error {
	members, direct := s.direct.get(conversationID)
	if members == nil {
		st, err := load(ctx, s.db, selectState, conversationID)
		if err != nil {
			return err
		}
		if st.kind == kindDirect || st.kind == kindNotes {
			s.direct.add(conversationID, st.kind, st.members)
		}
		members, direct = st.members, st.kind == kindDirect
	}
	if !slices.Contains(members, userID) {
		return apierror.PermissionDenied("user %s is not in conversation %s", userID, conversationID)
	}
	if !direct {
		return nil
	}
	for _, id := range members {
		if id != userID {
			return s.blocks.CheckMessaging(ctx, userID, id)
		}
	}
	return nil
}

// parseBlobIDs checks a message's attachment ids: at most ten distinct
// ones.
func parseBlobIDs(blobIDs [][]byte) ([]ids.UUID, error) {
	if len(blobIDs) > maxBlobIDs {
		return nil, apierror.InvalidArgument("blob_ids must have at most %d ids", maxBlobIDs)
	}
	list := make([]ids.UUID, 0, len(blobIDs))
	for _, b := range blobIDs {
		id, ok := ids.Parse(b)
		if !ok {
			return nil, apierror.InvalidArgument("blob_ids must be 16 bytes each")
		}
		if slices.Contains(list, id) {
			return nil, apierror.InvalidArgument("blob_ids must be distinct")
		}
		list = append(list, id)
	}
	return list, nil
}

// useAttachments takes the attachments' uploads into use, unless the
// message is a resend of one already stored, and reports whether it did.
func (s *Service) useAttachments(ctx context.Context, userID, conversationID ids.UUID, clientMessageID string, blobIDs []ids.UUID) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM message WHERE conversation_id = ? AND sender_user_id = ? AND client_message_id = ?`,
		conversationID, userID, clientMessageID).Scan(&one)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err := s.attachments.UseAttachments(ctx, userID, blobIDs); err != nil {
		return false, err
	}
	return true, nil
}

// releaseAttachments gives the uploads back after a failed send, on its own
// deadline, since the send may have failed on the request's. A release
// that fails leaves them used; the send's own error is what the device
// hears.
func (s *Service) releaseAttachments(ctx context.Context, userID ids.UUID, blobIDs []ids.UUID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	_ = s.attachments.ReleaseAttachments(ctx, userID, blobIDs)
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

	st, err := member(ctx, tx, conversationID, userID)
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
	now := vtdb.Now()
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
	row := outboxRow{conversationID: conversationID, outboxID: ids.Random(), recipients: recipients, st: st, kind: "read", event: event, at: now}
	if err := row.insert(ctx, tx); err != nil {
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
// left. The newer ones are in MySQL; the page goes on in the cold tier
// below the watermark.
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
	for {
		messages, again, err := s.messages(ctx, conversationID, joinedSeq, upTo, beforeSeq, limit)
		if !again {
			return messages, err
		}
	}
}

// coldRange is where a page that MySQL gave goes on in the cold tier: from
// joinedSeq up to hi, when there is room and anything there. A page with a
// message the watermark has since passed is read again.
func coldRange(page []*pb.Message, joinedSeq, upTo, beforeSeq, coldSeq uint64, limit uint32) (hi uint64, more, again bool) {
	hi = min(upTo, beforeSeq-1, coldSeq)
	if len(page) > 0 {
		lowest := page[len(page)-1].GetSeq()
		if lowest <= coldSeq {
			return 0, false, true
		}
		hi = min(hi, lowest-1)
	}
	return hi, len(page) < int(limit) && hi >= joinedSeq, false
}

// messages reads a page of history: MySQL first, then the watermark, then
// the cold tier below the lowest seq MySQL gave. The watermark only rises,
// and what it covers is in the cold tier before it does, so nothing falls
// between the two. A move that overlapped the read may have taken the
// reactions of what MySQL gave, so the read is made again then.
func (s *Service) messages(ctx context.Context, conversationID ids.UUID, joinedSeq, upTo, beforeSeq uint64, limit uint32) ([]*pb.Message, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, sender_user_id, client_message_id, body, blob_ids, sent_at FROM message
		WHERE conversation_id = ? AND seq >= ? AND seq <= ? AND seq < ? ORDER BY seq DESC LIMIT ?`,
		conversationID, joinedSeq, upTo, beforeSeq, limit)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var messages []*pb.Message
	for rows.Next() {
		msg := &pb.Message{ConversationId: conversationID.Bytes()}
		var body, blobs []byte
		var sentAt time.Time
		if err := rows.Scan(&msg.Seq, &msg.SenderUserId, &msg.ClientMessageId, &body, &blobs, &sentAt); err != nil {
			return nil, false, err
		}
		msg.SentAt = timestamppb.New(sentAt)
		msg.BlobIds = decodeBlobIDs(blobs)
		if err := decodeBody(body, msg); err != nil {
			return nil, false, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	// Closed before the next query, which frees the connection for it.
	rows.Close()
	if err := addReactions(ctx, s.db, conversationID, messages); err != nil {
		return nil, false, err
	}
	var coldSeq uint64
	if err := s.db.QueryRowContext(ctx, `
		SELECT cold_seq FROM conversation WHERE conversation_id = ?`, conversationID).Scan(&coldSeq); err != nil {
		return nil, false, err
	}
	hi, more, again := coldRange(messages, joinedSeq, upTo, beforeSeq, coldSeq, limit)
	if again || !more {
		return messages, again, nil
	}
	cold, err := s.cold.Scan(ctx, conversationID, joinedSeq, hi, int(limit)-len(messages))
	if err != nil {
		return nil, false, err
	}
	for _, c := range cold {
		msg, err := c.message(conversationID)
		if err != nil {
			return nil, false, err
		}
		messages = append(messages, msg)
	}
	return messages, false, nil
}
