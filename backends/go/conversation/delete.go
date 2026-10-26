package conversation

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Delete deletes one of the user's own messages for everyone: its body
// keeps only the deletion time, and the event tells the current members,
// and the user when no longer one, so that their logs and lists drop the
// content. A former member may delete what they sent before leaving.
// Deleting a deleted message again does nothing. The outbox row names the
// attachments' uploads, which the fan-out releases for the sweeper. A message in the
// cold tier is changed there, under the conversation's row lock as in MySQL.
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
	var body, blobs []byte
	var outboxID uint64
	var cold coldMessage
	inCold := seq <= st.coldSeq
	if inCold {
		var found bool
		if cold, found, err = s.cold.Get(ctx, conversationID, seq); err != nil {
			return err
		}
		if !found {
			return notFound
		}
		sender, body, blobs, outboxID = cold.sender, cold.body, cold.blobIDs, cold.outboxID
	} else {
		err = tx.QueryRowContext(ctx, `
			SELECT sender_user_id, body, blob_ids, outbox_id FROM message WHERE conversation_id = ? AND seq = ? FOR UPDATE`,
			conversationID, seq).Scan(&sender, &body, &blobs, &outboxID)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound
		}
		if err != nil {
			return err
		}
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
	now := vtdb.Now()
	deletedAt := timestamppb.New(now)
	if body, err = encodeBody(&pb.Message{DeletedAt: deletedAt}); err != nil {
		return err
	}
	committed := false
	if inCold {
		deleted := cold
		deleted.body, deleted.blobIDs, deleted.reactions = body, nil, nil
		undo, err := s.cold.change(ctx, conversationID, cold, deleted)
		if err != nil {
			return err
		}
		defer undo(&committed)
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE message SET body = ?, blob_ids = '' WHERE conversation_id = ? AND seq = ?`,
			body, conversationID, seq); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM reaction WHERE conversation_id = ? AND seq = ?`,
			conversationID, seq); err != nil {
			return err
		}
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
	row := outboxRow{
		conversationID: conversationID, outboxID: ids.Random(), recipients: recipients, st: st, kind: "message_deleted",
		target: sql.Null[uint64]{V: outboxID, Valid: true}, event: event, at: now,
	}
	if len(blobs) > 0 {
		row.releasedUploads = append(userID.Bytes(), blobs...)
	}
	if err := row.insert(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
