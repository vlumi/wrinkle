package fanout

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	backendv1 "github.com/vlumi/wrinkle/proto/go/wrinkle/backend/v1"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// listUpdate changes the user's conversation list in the transaction that
// appends the event.
type listUpdate func(ctx context.Context, tx *sql.Tx, event *pb.Event) error

// listEntry is a recipient's place in the conversation list for the row
// being delivered.
type listEntry struct {
	userID ids.UUID
	o      *outboxRow
	// peerID is the other user of a direct conversation; peer is their
	// profile when the entry may be new, nil otherwise.
	peerID ids.UUID
	peer   *pb.User
	// deletedSender receives the sender of a group message being deleted,
	// from the user's copy of it, for the notifier.
	deletedSender *ids.UUID
}

// deliver appends a conversation's event to one recipient's log, then
// pushes it.
func (w *Worker) deliver(ctx context.Context, userID ids.UUID, o *outboxRow) error {
	entry := listEntry{userID: userID, o: o}
	if !o.direct() && o.event.GetMessageDeleted() != nil {
		entry.deletedSender = new(ids.UUID)
	}
	if o.direct() {
		entry.peerID = o.peerOf(userID)
		if o.event.GetMessage() != nil {
			var err error
			if entry.peer, err = w.peerForNewEntry(ctx, userID, o.conversationID, entry.peerID); err != nil {
				return err
			}
		}
	}
	event, err := w.append(ctx, userID, o.conversationID, o.outboxID, o.event, entry.update)
	if err != nil || event == nil {
		return err
	}
	w.messageDelivered(userID, o, event)
	if deleted := event.GetMessageDeleted(); deleted != nil && w.notifier != nil {
		var sender ids.UUID
		if entry.deletedSender != nil {
			sender = *entry.deletedSender
		}
		w.notifier.MessageDeleted(userID, deleted, sender)
	}
	return nil
}

// messageDelivered notes a message's list entry as existing and tells the
// notifier.
func (w *Worker) messageDelivered(userID ids.UUID, o *outboxRow, event *pb.Event) {
	msg := event.GetMessage().GetMessage()
	if msg == nil {
		return
	}
	w.listed.add(userID, o.conversationID)
	if w.notifier != nil {
		w.notifier.MessageDelivered(userID, msg, !o.direct())
	}
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
	var deletedAt sql.NullTime
	err = w.db.QueryRowContext(ctx, `SELECT name, icon, avatar, deleted_at FROM user WHERE user_id = ?`, peerID).
		Scan(&peer.Name, &peer.Icon, &peer.Avatar, &deletedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	peer.Deleted = deletedAt.Valid
	return peer, nil
}

// append adds the event to the user's log, in one transaction with the
// list update, and publishes it. It returns the event as stored, or none
// when there is nothing to push: the user does not exist or is deleted, or
// the log already has the event.
func (w *Worker) append(ctx context.Context, userID, sourceID ids.UUID, outboxID uint64, source *pb.Event, update listUpdate) (*pb.Event, error) {
	return w.appendBuilt(ctx, userID, sourceID, outboxID, source, nil, update)
}

// appendBuilt is append with the event's body completed by build, in the
// transaction and under the lock on the user row, before it is stored.
func (w *Worker) appendBuilt(ctx context.Context, userID, sourceID ids.UUID, outboxID uint64, source *pb.Event, build listUpdate, update listUpdate) (*pb.Event, error) {
	event, payload, err := w.store(ctx, userID, sourceID, outboxID, source, build, update)
	if err != nil || event == nil {
		return nil, err
	}
	w.delivered(ctx, userID, event, payload)
	return event, nil
}

// delivered counts an event stored in the user's log and publishes it.
func (w *Worker) delivered(ctx context.Context, userID ids.UUID, event *pb.Event, payload []byte) {
	metrics.FanoutDeliveries.Inc()
	metrics.FanoutLag.Observe(time.Since(event.GetOccurredAt().AsTime()).Seconds())
	w.publish(ctx, userID, &backendv1.Push{Kind: &backendv1.Push_Event{Event: payload}})
}

// publish sends a push to the gateways holding the user's devices. A lost
// event is caught up on the next sync.
func (w *Worker) publish(ctx context.Context, userID ids.UUID, push *backendv1.Push) {
	data, err := proto.Marshal(push)
	if err == nil {
		err = w.pub.Publish(ctx, userID, data)
	}
	if err != nil {
		w.log.Warn("publish failed", "user", userID, "err", err)
	}
}

func (w *Worker) store(ctx context.Context, userID, sourceID ids.UUID, outboxID uint64, source *pb.Event, build, update listUpdate) (*pb.Event, []byte, error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var head uint64
	var deletedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT log_head, deleted_at FROM user WHERE user_id = ? FOR UPDATE`, userID).Scan(&head, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		w.log.Warn("no such user, dropping event", "user", userID)
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if deletedAt.Valid {
		return nil, nil, nil
	}
	event := proto.Clone(source).(*pb.Event)
	event.Cursor = head + 1
	if build != nil {
		if err := build(ctx, tx, event); err != nil {
			return nil, nil, err
		}
	}
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
		if vtdb.IsDuplicate(err) {
			return false, nil
		}
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user SET log_head = ? WHERE user_id = ?`, event.Cursor, userID); err != nil {
		return false, err
	}
	return true, nil
}

func (e listEntry) update(ctx context.Context, tx *sql.Tx, event *pb.Event) error {
	switch body := event.GetBody().(type) {
	case *pb.Event_Message:
		return e.recordMessage(ctx, tx, body.Message.GetMessage(), event.GetOccurredAt().AsTime())
	case *pb.Event_Read:
		return e.recordRead(ctx, tx, body.Read)
	case *pb.Event_MessageDeleted:
		return e.recordDeletion(ctx, tx, body.MessageDeleted)
	}
	return nil
}

// flag is a change to a membership flag of a list entry, when set.
type flag struct{ set, value bool }

// membership is what a group's notice changes about the recipient's place
// in it.
func membership(userID ids.UUID, msg *pb.Message) (member, admin flag) {
	is := func(u *pb.User) bool { return u != nil && bytes.Equal(u.GetUserId(), userID[:]) }
	sender := bytes.Equal(msg.GetSenderUserId(), userID[:])
	switch n := msg.GetNotice().GetKind().(type) {
	case *pb.Notice_GroupCreated:
		return flag{true, true}, flag{true, sender}
	case *pb.Notice_MembersAdded:
		for _, u := range n.MembersAdded.GetUsers() {
			if is(u) {
				return flag{true, true}, flag{true, false}
			}
		}
	case *pb.Notice_MemberRemoved:
		if is(n.MemberRemoved.GetUser()) {
			return flag{true, false}, flag{true, false}
		}
	case *pb.Notice_MemberLeft:
		if sender {
			return flag{true, false}, flag{true, false}
		}
		if is(n.MemberLeft.GetPromoted()) {
			return flag{}, flag{true, true}
		}
	case *pb.Notice_AdminChanged:
		if is(n.AdminChanged.GetUser()) {
			return flag{}, flag{true, n.AdminChanged.GetAdmin()}
		}
	}
	return flag{}, flag{}
}

// recordMessage adds the conversation to the user's list, or makes the
// message its last unless a later one is already there; the group's topic,
// size and the user's membership follow the latest message the same way.
// The sender's own read position moves to the message. A new direct entry
// takes the peer's profile; an existing one keeps what profile changes put
// there.
func (e listEntry) recordMessage(ctx context.Context, tx *sql.Tx, msg *pb.Message, occurredAt time.Time) error {
	userID, peer, o := e.userID, e.peer, e.o
	var ownSeq uint64
	if bytes.Equal(msg.GetSenderUserId(), userID[:]) {
		ownSeq = msg.GetSeq()
	}
	var peerColumn any
	if o.direct() {
		peerColumn = e.peerID
	}
	body, err := proto.Marshal(&pb.Message{Content: msg.GetContent(), BlobIds: msg.GetBlobIds()})
	if err != nil {
		return err
	}
	member, admin := membership(userID, msg)
	seq := msg.GetSeq()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO user_conversation
			(user_id, conversation_id, kind, peer_user_id, peer_name, peer_icon, peer_avatar, peer_deleted,
			 topic, member_count, member, admin, last_seq,
			 last_sender_user_id, last_client_message_id, last_body, last_sent_at,
			 read_seq, peer_read_seq, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
		ON DUPLICATE KEY UPDATE
			last_seq = GREATEST(last_seq, ?),
			topic = IF(? >= last_seq, ?, topic),
			member_count = IF(? >= last_seq, ?, member_count),
			member = IF(? AND ? >= last_seq, ?, member),
			admin = IF(? AND ? >= last_seq, ?, admin),
			last_sender_user_id = IF(? >= last_seq, ?, last_sender_user_id),
			last_client_message_id = IF(? >= last_seq, ?, last_client_message_id),
			last_body = IF(? >= last_seq, ?, last_body),
			last_sent_at = IF(? >= last_seq, ?, last_sent_at),
			read_seq = GREATEST(read_seq, ?),
			updated_at = GREATEST(updated_at, ?)`,
		userID, o.conversationID, o.conversationKind, peerColumn, peer.GetName(), peer.GetIcon(), vtdb.NullBytes(peer.GetAvatar()), peer.GetDeleted(),
		o.topic, o.memberCount, !member.set || member.value, admin.value, seq,
		msg.GetSenderUserId(), msg.GetClientMessageId(), body, msg.GetSentAt().AsTime(),
		ownSeq, occurredAt,
		seq,
		seq, o.topic,
		seq, o.memberCount,
		member.set, seq, member.value,
		admin.set, seq, admin.value,
		seq, msg.GetSenderUserId(),
		seq, msg.GetClientMessageId(),
		seq, body,
		seq, msg.GetSentAt().AsTime(),
		ownSeq,
		occurredAt)
	return err
}

// recordRead moves the user's own read position or, in a direct
// conversation, the peer's, whichever the event is about.
func (e listEntry) recordRead(ctx context.Context, tx *sql.Tx, read *pb.ReadEvent) error {
	column := "peer_read_seq"
	switch {
	case bytes.Equal(read.GetUserId(), e.userID[:]):
		column = "read_seq"
	case !e.o.direct():
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE user_conversation SET `+column+` = GREATEST(`+column+`, ?)
		WHERE user_id = ? AND conversation_id = ?`,
		read.GetSeq(), e.userID, e.o.conversationID)
	return err
}

// recordDeletion drops the deleted message's content from the copy in the
// user's log, so that a sync from before it never returns the content, and
// from the list entry when it is the last message, keeping the entry's
// place in the list.
func (e listEntry) recordDeletion(ctx context.Context, tx *sql.Tx, deleted *pb.MessageDeletedEvent) error {
	userID, o := e.userID, e.o
	var position uint64
	var payload []byte
	err := tx.QueryRowContext(ctx, `
		SELECT position, payload FROM user_event WHERE user_id = ? AND source_id = ? AND outbox_id = ? FOR UPDATE`,
		userID, o.conversationID, o.targetOutboxID).Scan(&position, &payload)
	switch {
	case err == nil:
		event := &pb.Event{}
		if err := proto.Unmarshal(payload, event); err != nil {
			// Retrying would not mend it; the copy becomes a bare tombstone.
			slog.Warn("unreadable copy of a deleted message", "user", userID, "position", position, "err", err)
			event = &pb.Event{Body: &pb.Event_Message{Message: &pb.MessageEvent{Message: &pb.Message{
				ConversationId: o.conversationID.Bytes(), Seq: deleted.GetSeq(),
			}}}}
		}
		if msg := event.GetMessage().GetMessage(); msg != nil {
			if e.deletedSender != nil {
				*e.deletedSender, _ = ids.Parse(msg.GetSenderUserId())
			}
			msg.Content = nil
			msg.BlobIds = nil
			msg.DeletedAt = deleted.GetDeletedAt()
			if payload, err = proto.Marshal(event); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE user_event SET payload = ? WHERE user_id = ? AND position = ?`,
				payload, userID, position); err != nil {
				return err
			}
		}
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	body, err := proto.Marshal(&pb.Message{DeletedAt: deleted.GetDeletedAt()})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE user_conversation SET last_body = ?
		WHERE user_id = ? AND conversation_id = ? AND last_seq = ?`,
		body, userID, o.conversationID, deleted.GetSeq())
	return err
}
