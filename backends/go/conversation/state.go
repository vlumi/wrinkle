package conversation

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// The kinds of conversation, as stored.
const (
	kindDirect = "direct"
	kindGroup  = "group"
)

// state is a conversation's row: what a change checks and fans out to.
type state struct {
	kind    string
	topic   string
	members []ids.UUID // sorted bytewise
	lastSeq uint64
	// The current key epoch, 0 before the first, and whether the
	// membership changed since it.
	keyEpoch uint32
	rotate   bool
}

func (st state) has(userID ids.UUID) bool {
	_, found := slices.BinarySearchFunc(st.members, userID, compareIDs)
	return found
}

func compareIDs(a, b ids.UUID) int { return bytes.Compare(a[:], b[:]) }

// encodeMembers concatenates the ids, sorted bytewise.
func encodeMembers(members []ids.UUID) []byte {
	sorted := slices.SortedFunc(slices.Values(members), compareIDs)
	b := make([]byte, 0, 16*len(sorted))
	for _, id := range sorted {
		b = append(b, id[:]...)
	}
	return b
}

func decodeMembers(b []byte) ([]ids.UUID, error) {
	if len(b)%16 != 0 {
		return nil, fmt.Errorf("members of %d bytes", len(b))
	}
	members := make([]ids.UUID, len(b)/16)
	for i := range members {
		copy(members[i][:], b[16*i:])
	}
	return members, nil
}

const (
	selectState          = `SELECT kind, topic, members, last_seq, key_epoch, rotate FROM conversation WHERE conversation_id = ?`
	selectStateForUpdate = selectState + ` FOR UPDATE`
)

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// load reads the conversation's row with the given query.
func load(ctx context.Context, q rowQuerier, query string, conversationID ids.UUID) (state, error) {
	var st state
	var members []byte
	err := q.QueryRowContext(ctx, query, conversationID).Scan(&st.kind, &st.topic, &members, &st.lastSeq, &st.keyEpoch, &st.rotate)
	if errors.Is(err, sql.ErrNoRows) {
		return st, apierror.NotFound("conversation %s does not exist", conversationID)
	}
	if err != nil {
		return st, err
	}
	st.members, err = decodeMembers(members)
	return st, err
}

// member locks the conversation's row and checks that the user is a
// current member.
func member(ctx context.Context, q rowQuerier, conversationID, userID ids.UUID) (state, error) {
	st, err := load(ctx, q, selectStateForUpdate, conversationID)
	if err != nil {
		return st, err
	}
	if !st.has(userID) {
		return st, apierror.PermissionDenied("user %s is not in conversation %s", userID, conversationID)
	}
	return st, nil
}

// group loads a group with the given query and checks that the user is a
// current member.
func group(ctx context.Context, q rowQuerier, query string, conversationID, userID ids.UUID) (state, error) {
	st, err := load(ctx, q, query, conversationID)
	if err != nil {
		return st, err
	}
	if st.kind != kindGroup {
		return st, apierror.FailedPrecondition("conversation %s is not a group", conversationID)
	}
	if !st.has(userID) {
		return st, apierror.PermissionDenied("user %s is not in group %s", userID, conversationID)
	}
	return st, nil
}

// encodeBody keeps only the message's content, or its deletion time, as
// message.body stores it.
func encodeBody(msg *pb.Message) ([]byte, error) {
	b, err := proto.Marshal(&pb.Message{Content: msg.GetContent(), DeletedAt: msg.GetDeletedAt()})
	if err != nil {
		return nil, fmt.Errorf("marshal message body: %w", err)
	}
	return b, nil
}

// encodeBlobIDs concatenates a message's attachment ids, as
// message.blob_ids stores them.
func encodeBlobIDs(blobIDs [][]byte) []byte {
	b := make([]byte, 0, 16*len(blobIDs))
	for _, id := range blobIDs {
		b = append(b, id...)
	}
	return b
}

func decodeBlobIDs(b []byte) [][]byte {
	var list [][]byte
	for len(b) >= 16 {
		list = append(list, slices.Clone(b[:16]))
		b = b[16:]
	}
	return list
}

// membershipChanged marks that the next send needs a new key epoch, unless
// there is none yet: the first publish starts one anyway.
func (st *state) membershipChanged() {
	if st.keyEpoch > 0 {
		st.rotate = true
	}
}

// decodeBody sets the message's content, or its deletion time, from a
// stored body.
func decodeBody(body []byte, msg *pb.Message) error {
	content := &pb.Message{}
	if err := proto.Unmarshal(body, content); err != nil {
		return fmt.Errorf("unmarshal message body: %w", err)
	}
	msg.Content = content.Content
	msg.DeletedAt = content.DeletedAt
	return nil
}

// store saves a message at st.lastSeq, moves the conversation's counter to
// it, with the topic, members and rotate of st when withState, moves the sender's
// read mark to it, and writes the outbox row for the recipients.
func (s *Service) store(ctx context.Context, tx *sql.Tx, conversationID ids.UUID, st state, msg *pb.Message, recipients []ids.UUID, withState bool) error {
	sentAt := s.now()
	msg.Seq = st.lastSeq
	msg.SentAt = timestamppb.New(sentAt)
	body, err := encodeBody(msg)
	if err != nil {
		return err
	}
	if withState {
		_, err = tx.ExecContext(ctx, `
			UPDATE conversation SET topic = ?, members = ?, last_seq = ?, rotate = ? WHERE conversation_id = ?`,
			st.topic, encodeMembers(st.members), st.lastSeq, st.rotate, conversationID)
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE conversation SET last_seq = ? WHERE conversation_id = ?`, st.lastSeq, conversationID)
	}
	if err != nil {
		return err
	}
	outboxID := ids.Random()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO message (conversation_id, seq, sender_user_id, client_message_id, body, blob_ids, sent_at, outbox_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		conversationID, msg.Seq, msg.GetSenderUserId(), msg.GetClientMessageId(), body, encodeBlobIDs(msg.GetBlobIds()), sentAt, outboxID); err != nil {
		return err
	}
	// Sending moves the sender's own read position, without a read event:
	// the lists move it with the message.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO read_mark (conversation_id, user_id, seq, updated_at) VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE seq = GREATEST(seq, ?), updated_at = ?`,
		conversationID, msg.GetSenderUserId(), msg.Seq, sentAt, msg.Seq, sentAt); err != nil {
		return err
	}
	event := &pb.Event{
		OccurredAt: msg.SentAt,
		Body:       &pb.Event_Message{Message: &pb.MessageEvent{Message: msg}},
	}
	return insertOutbox(ctx, tx, conversationID, outboxID, recipients, st, "message", sql.Null[uint64]{}, event, sentAt)
}

// notice stores a change to a group, made by the sender, as the message at
// st.lastSeq, with st as the group after the change.
func (s *Service) notice(ctx context.Context, tx *sql.Tx, conversationID ids.UUID, st state, senderID ids.UUID, notice *pb.Notice, recipients []ids.UUID) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	msg := &pb.Message{
		ConversationId:  conversationID.Bytes(),
		SenderUserId:    senderID.Bytes(),
		ClientMessageId: "notice-" + hex.EncodeToString(b[:]),
		Content:         &pb.Message_Notice{Notice: notice},
	}
	return s.store(ctx, tx, conversationID, st, msg, recipients, true)
}

// insertOutbox writes the outbox row of a change; target is the outbox row
// of the message a deletion is about.
func insertOutbox(ctx context.Context, tx *sql.Tx, conversationID ids.UUID, outboxID uint64, recipients []ids.UUID, st state, kind string, target sql.Null[uint64], event *pb.Event, at time.Time) error {
	payload, err := proto.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal outbox event: %w", err)
	}
	to := make([]byte, 0, 16*len(recipients))
	for _, id := range recipients {
		to = append(to, id[:]...)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO outbox (conversation_id, outbox_id, recipients, conversation_kind, topic, member_count, kind, target_outbox_id, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		conversationID, outboxID, to, st.kind, st.topic, len(st.members), kind, target, payload, at); err != nil {
		return err
	}
	// The insert reaches the binlog, which is all the fan-out reads; deleting
	// the row in the same transaction keeps the table empty.
	_, err = tx.ExecContext(ctx, `
		DELETE FROM outbox WHERE conversation_id = ? AND outbox_id = ?`,
		conversationID, outboxID)
	return err
}
