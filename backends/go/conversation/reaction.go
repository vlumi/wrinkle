package conversation

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const maxReactionCiphertext = 1 << 10

// SetReaction replaces the user's sealed reactions to a message, or
// removes them when sealed is nil. Setting the same bytes again, or
// removing none, changes nothing and tells no one. A message in the cold
// tier is changed there, under the conversation's row lock as in MySQL.
func (s *Service) SetReaction(ctx context.Context, userID, conversationID ids.UUID, seq uint64, sealed *pb.Sealed) error {
	if seq == 0 {
		return apierror.InvalidArgument("seq must be positive")
	}
	if sealed != nil {
		if len(sealed.GetNonce()) != nonceBytes {
			return apierror.InvalidArgument("sealed.nonce must be %d bytes", nonceBytes)
		}
		if n := len(sealed.GetCiphertext()); n == 0 || n > maxReactionCiphertext {
			return apierror.InvalidArgument("sealed.ciphertext must be 1 to %d bytes", maxReactionCiphertext)
		}
	}
	if err := s.checkBlocks(ctx, userID, conversationID); err != nil {
		return err
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
	var joinedSeq uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT joined_seq FROM conversation_member WHERE conversation_id = ? AND user_id = ?`,
		conversationID, userID).Scan(&joinedSeq); err != nil {
		return err
	}
	notFound := apierror.NotFound("message %d of conversation %s does not exist", seq, conversationID)
	if seq < joinedSeq {
		return notFound
	}
	var body []byte
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
		body = cold.body
	} else {
		err = tx.QueryRowContext(ctx, `
			SELECT body FROM message WHERE conversation_id = ? AND seq = ?`,
			conversationID, seq).Scan(&body)
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
	switch {
	case msg.GetNotice() != nil:
		return apierror.InvalidArgument("message %d is a notice", seq)
	case msg.GetDeletedAt() != nil:
		return apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_MESSAGE_DELETED, "message %d is deleted", seq)
	}
	var newBody []byte
	if sealed != nil {
		switch {
		case st.keyEpoch == 0 || sealed.GetEpoch() != st.keyEpoch:
			return apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_STALE_KEY_EPOCH, "stale key epoch: the current one is %d", st.keyEpoch)
		case st.rotate:
			return apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_KEYS_MUST_ROTATE, "keys must rotate: the membership changed since epoch %d", st.keyEpoch)
		}
		sealed = &pb.Sealed{Epoch: sealed.GetEpoch(), Nonce: sealed.GetNonce(), Ciphertext: sealed.GetCiphertext()}
		if newBody, err = (proto.MarshalOptions{Deterministic: true}).Marshal(sealed); err != nil {
			return fmt.Errorf("marshal reaction: %w", err)
		}
	}
	var oldBody []byte
	var found bool
	var others []*pb.Reaction
	if inCold {
		list, err := cold.reactionList()
		if err != nil {
			return err
		}
		for _, r := range list {
			if !bytes.Equal(r.GetUserId(), userID[:]) {
				others = append(others, r)
				continue
			}
			found = true
			if oldBody, err = (proto.MarshalOptions{Deterministic: true}).Marshal(r.GetSealed()); err != nil {
				return fmt.Errorf("marshal reaction: %w", err)
			}
		}
	} else {
		err = tx.QueryRowContext(ctx, `
			SELECT body FROM reaction WHERE conversation_id = ? AND seq = ? AND user_id = ?`,
			conversationID, seq, userID).Scan(&oldBody)
		found = err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if (sealed == nil && !found) || (sealed != nil && found && bytes.Equal(oldBody, newBody)) {
		return nil
	}
	// The column keeps microseconds; the event carries the stored time.
	now := vtdb.Now()
	updatedAt := timestamppb.New(now)
	committed := false
	switch {
	case inCold:
		if sealed != nil {
			others = append(others, &pb.Reaction{UserId: userID.Bytes(), Sealed: sealed, UpdatedAt: updatedAt})
		}
		changed := cold
		if changed.reactions, err = encodeReactions(others); err != nil {
			return err
		}
		undo, err := s.cold.change(ctx, conversationID, cold, changed)
		if err != nil {
			return err
		}
		defer undo(&committed)
	case sealed == nil:
		_, err = tx.ExecContext(ctx, `
			DELETE FROM reaction WHERE conversation_id = ? AND seq = ? AND user_id = ?`,
			conversationID, seq, userID)
	default:
		_, err = tx.ExecContext(ctx, `
			INSERT INTO reaction (conversation_id, seq, user_id, body, updated_at) VALUES (?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE body = ?, updated_at = ?`,
			conversationID, seq, userID, newBody, now, newBody, now)
	}
	if err != nil {
		return err
	}
	event := &pb.Event{
		OccurredAt: updatedAt,
		Body: &pb.Event_Reaction{Reaction: &pb.ReactionEvent{
			ConversationId: conversationID.Bytes(), Seq: seq, UserId: userID.Bytes(), Sealed: sealed, UpdatedAt: updatedAt,
		}},
	}
	row := outboxRow{conversationID: conversationID, outboxID: ids.Random(), recipients: st.members, st: st, kind: "reaction", event: event, at: now}
	if err := row.insert(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// addReactions fills in the reactions of the messages, which are of one
// conversation and sorted by seq, newest first, with one query over their
// range.
func addReactions(ctx context.Context, q vtdb.Querier, conversationID ids.UUID, messages []*pb.Message) error {
	bySeq := make(map[uint64]*pb.Message, len(messages))
	for _, msg := range messages {
		if msg.GetDeletedAt() == nil && msg.GetNotice() == nil {
			bySeq[msg.GetSeq()] = msg
		}
	}
	if len(bySeq) == 0 {
		return nil
	}
	rows, err := q.QueryContext(ctx, `
		SELECT seq, user_id, body, updated_at FROM reaction
		WHERE conversation_id = ? AND seq >= ? AND seq <= ? ORDER BY seq, updated_at, user_id`,
		conversationID, messages[len(messages)-1].GetSeq(), messages[0].GetSeq())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var seq uint64
		var userID, body []byte
		var updatedAt time.Time
		if err := rows.Scan(&seq, &userID, &body, &updatedAt); err != nil {
			return err
		}
		msg := bySeq[seq]
		if msg == nil {
			continue
		}
		sealed := &pb.Sealed{}
		if err := proto.Unmarshal(body, sealed); err != nil {
			return fmt.Errorf("unmarshal reaction: %w", err)
		}
		msg.Reactions = append(msg.Reactions, &pb.Reaction{
			UserId: userID, Sealed: sealed, UpdatedAt: timestamppb.New(updatedAt),
		})
	}
	return rows.Err()
}
