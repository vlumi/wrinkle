package conversation

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Delete deletes one of the user's own messages for everyone: its body
// keeps only the deletion time, and the event tells the current members,
// and the user when no longer one, so that their logs and lists drop the
// content. A former member may delete what they sent before leaving.
// Deleting a deleted message again does nothing.
func (s *Service) Delete(ctx context.Context, userID, conversationID ids.UUID, seq uint64) error {
	if seq == 0 {
		return apierror.InvalidArgument("seq must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	st, err := load(ctx, tx, selectStateForUpdate, conversationID)
	if err != nil {
		return err
	}
	var joinedSeq uint64
	var leftSeq sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT joined_seq, left_seq FROM conversation_member WHERE conversation_id = ? AND user_id = ?`,
		conversationID, userID).Scan(&joinedSeq, &leftSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return apierror.PermissionDenied("user %s is not in conversation %s", userID, conversationID)
	}
	if err != nil {
		return err
	}
	notFound := apierror.NotFound("message %d of conversation %s does not exist", seq, conversationID)
	if seq < joinedSeq || (leftSeq.Valid && seq > uint64(leftSeq.Int64)) {
		return notFound
	}
	var sender ids.UUID
	var body []byte
	var outboxID uint64
	err = tx.QueryRowContext(ctx, `
		SELECT sender_user_id, body, outbox_id FROM message WHERE conversation_id = ? AND seq = ? FOR UPDATE`,
		conversationID, seq).Scan(&sender, &body, &outboxID)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound
	}
	if err != nil {
		return err
	}
	msg := &pb.Message{}
	if err := decodeBody(body, msg); err != nil {
		return err
	}
	if msg.GetNotice() != nil {
		return apierror.InvalidArgument("message %d is a notice", seq)
	}
	if sender != userID {
		return apierror.PermissionDenied("message %d is not the user's own", seq)
	}
	if msg.GetDeletedAt() != nil {
		return tx.Commit()
	}
	now := s.now()
	deletedAt := timestamppb.New(now)
	if body, err = encodeBody(&pb.Message{DeletedAt: deletedAt}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE message SET body = ? WHERE conversation_id = ? AND seq = ?`,
		body, conversationID, seq); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM reaction WHERE conversation_id = ? AND seq = ?`,
		conversationID, seq); err != nil {
		return err
	}
	recipients := st.members
	if !st.has(userID) {
		recipients = append(slices.Clone(recipients), userID)
	}
	event := &pb.Event{
		OccurredAt: deletedAt,
		Body: &pb.Event_MessageDeleted{MessageDeleted: &pb.MessageDeletedEvent{
			ConversationId: conversationID.Bytes(), Seq: seq, DeletedAt: deletedAt,
		}},
	}
	target := sql.Null[uint64]{V: outboxID, Valid: true}
	if err := insertOutbox(ctx, tx, conversationID, ids.Random(), recipients, st, "message_deleted", target, event, now); err != nil {
		return err
	}
	return tx.Commit()
}
