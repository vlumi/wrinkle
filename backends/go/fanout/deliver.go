package fanout

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// MySQL error for a unique key violation.
const duplicateKey = 1062

// listUpdate changes the user's conversation list in the transaction that
// appends the event.
type listUpdate func(ctx context.Context, tx *sql.Tx, event *pb.Event) error

// deliver appends a conversation's event to one participant's log, then
// pushes it.
func (w *Worker) deliver(ctx context.Context, userID ids.UUID, o *outboxRow) error {
	peerID := o.peerOf(userID)
	var peer *pb.User
	if o.event.GetMessage() != nil {
		var err error
		if peer, err = w.peerForNewEntry(ctx, userID, o.conversationID, peerID); err != nil {
			return err
		}
	}
	update := func(ctx context.Context, tx *sql.Tx, event *pb.Event) error {
		return updateConversationList(ctx, tx, userID, peerID, peer, o.conversationID, event)
	}
	event, err := w.append(ctx, userID, o.conversationID, o.outboxID, o.event, update)
	if err != nil || event == nil {
		return err
	}
	if msg := event.GetMessage().GetMessage(); msg != nil {
		w.listed.add(userID, o.conversationID)
		if w.notifier != nil {
			w.notifier.MessageDelivered(userID, msg)
		}
	}
	return nil
}

// peerForNewEntry returns the peer's current profile when the user's list
// may not have the conversation yet, for the entry a message creates. Once
// an entry is known to exist, it is never looked up again.
func (w *Worker) peerForNewEntry(ctx context.Context, userID, conversationID, peerID ids.UUID) (*pb.User, error) {
	if w.listed.has(userID, conversationID) {
		return nil, nil
	}
	var one int
	err := w.db.QueryRowContext(ctx, `
		SELECT 1 FROM user_conversation WHERE user_id = ? AND conversation_id = ?`,
		userID, conversationID).Scan(&one)
	if err == nil {
		w.listed.add(userID, conversationID)
		return nil, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	peer := &pb.User{UserId: peerID.Bytes()}
	err = w.db.QueryRowContext(ctx, `SELECT name, icon FROM user WHERE user_id = ?`, peerID).Scan(&peer.Name, &peer.Icon)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return peer, nil
}

// append adds the event to the user's log, in one transaction with the
// list update, and publishes it. It returns the event as stored, or none
// when there is nothing to push: the user does not exist, or the log
// already has the event.
func (w *Worker) append(ctx context.Context, userID, sourceID ids.UUID, outboxID uint64, source *pb.Event, update listUpdate) (*pb.Event, error) {
	event, payload, err := w.store(ctx, userID, sourceID, outboxID, source, update)
	if err != nil || event == nil {
		return nil, err
	}
	metrics.FanoutDeliveries.Inc()
	metrics.FanoutLag.Observe(time.Since(event.GetOccurredAt().AsTime()).Seconds())
	if err := w.pub.Publish(ctx, userID, payload); err != nil {
		// The log has it; devices catch up on their next sync.
		w.log.Warn("publish failed", "user", userID, "err", err)
	}
	return event, nil
}

func (w *Worker) store(ctx context.Context, userID, sourceID ids.UUID, outboxID uint64, source *pb.Event, update listUpdate) (*pb.Event, []byte, error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var head uint64
	err = tx.QueryRowContext(ctx, `SELECT log_head FROM user WHERE user_id = ? FOR UPDATE`, userID).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		w.log.Warn("no such user, dropping event", "user", userID)
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	event := proto.Clone(source).(*pb.Event)
	event.Cursor = head + 1
	payload, err := proto.Marshal(event)
	if err != nil {
		return nil, nil, err
	}
	if appended, err := appendEvent(ctx, tx, userID, sourceID, outboxID, event, payload); err != nil || !appended {
		return nil, nil, err
	}
	if update != nil {
		if err := update(ctx, tx, event); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return event, payload, nil
}

// appendEvent adds the event at its cursor and moves the log head. The
// unique key on the source outbox row makes a redelivery fail the insert;
// it reports false then.
func appendEvent(ctx context.Context, tx *sql.Tx, userID, sourceID ids.UUID, outboxID uint64, event *pb.Event, payload []byte) (bool, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_event (user_id, position, source_id, outbox_id, payload, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		userID, event.Cursor, sourceID, outboxID, payload, event.GetOccurredAt().AsTime()); err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == duplicateKey {
			return false, nil
		}
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user SET log_head = ? WHERE user_id = ?`, event.Cursor, userID); err != nil {
		return false, err
	}
	return true, nil
}

func updateConversationList(ctx context.Context, tx *sql.Tx, userID, peerID ids.UUID, peer *pb.User, conversationID ids.UUID, event *pb.Event) error {
	switch body := event.GetBody().(type) {
	case *pb.Event_Message:
		return recordMessage(ctx, tx, userID, peerID, peer, conversationID, body.Message.GetMessage(), event.GetOccurredAt().AsTime())
	case *pb.Event_Read:
		return recordRead(ctx, tx, userID, conversationID, body.Read)
	}
	return nil
}

// recordMessage adds the conversation to the user's list, or makes the
// message its last unless a later one is already there. The sender's own
// read position moves to the message. A new entry takes the peer's
// profile; an existing one keeps what profile changes put there.
func recordMessage(ctx context.Context, tx *sql.Tx, userID, peerID ids.UUID, peer *pb.User, conversationID ids.UUID, msg *pb.Message, occurredAt time.Time) error {
	var ownSeq uint64
	if bytes.Equal(msg.GetSenderUserId(), userID[:]) {
		ownSeq = msg.GetSeq()
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_conversation
			(user_id, conversation_id, peer_user_id, peer_name, peer_icon, last_seq,
			 last_sender_user_id, last_client_message_id, last_body, last_sent_at,
			 read_seq, peer_read_seq, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
		ON DUPLICATE KEY UPDATE
			last_seq = GREATEST(last_seq, ?),
			last_sender_user_id = IF(? >= last_seq, ?, last_sender_user_id),
			last_client_message_id = IF(? >= last_seq, ?, last_client_message_id),
			last_body = IF(? >= last_seq, ?, last_body),
			last_sent_at = IF(? >= last_seq, ?, last_sent_at),
			read_seq = GREATEST(read_seq, ?),
			updated_at = GREATEST(updated_at, ?)`,
		userID, conversationID, peerID, peer.GetName(), peer.GetIcon(), msg.GetSeq(),
		msg.GetSenderUserId(), msg.GetClientMessageId(), msg.GetText(), msg.GetSentAt().AsTime(),
		ownSeq, occurredAt,
		msg.GetSeq(),
		msg.GetSeq(), msg.GetSenderUserId(),
		msg.GetSeq(), msg.GetClientMessageId(),
		msg.GetSeq(), msg.GetText(),
		msg.GetSeq(), msg.GetSentAt().AsTime(),
		ownSeq,
		occurredAt)
	return err
}

// recordRead moves the user's own read position or the peer's, whichever
// the event is about.
func recordRead(ctx context.Context, tx *sql.Tx, userID, conversationID ids.UUID, read *pb.ReadEvent) error {
	column := "peer_read_seq"
	if bytes.Equal(read.GetUserId(), userID[:]) {
		column = "read_seq"
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE user_conversation SET `+column+` = GREATEST(`+column+`, ?)
		WHERE user_id = ? AND conversation_id = ?`,
		read.GetSeq(), userID, conversationID)
	return err
}
