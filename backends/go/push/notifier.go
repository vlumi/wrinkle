package push

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/vlumi/wrinkle/backends/go/contacts"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Presence tells which of a user's devices are connected to some gateway.
type Presence interface {
	ConnectedDevices(ctx context.Context, userID ids.UUID) (map[uint64]bool, error)
}

// Notifier pushes a message, or the deletion of one, to a user's devices
// that are not connected and have registered a token. Sends run in the background, bounded, so the
// fan-out never waits for APNs.
type Notifier struct {
	apns     *APNs
	db       *sql.DB
	presence Presence
	log      *slog.Logger
	slots    chan struct{}
}

const (
	maxInFlight = 64
	sendTimeout = 10 * time.Second
	// genericBody is what an alert says when the extension cannot decrypt
	// the message.
	genericBody = "New message"
)

func NewNotifier(apns *APNs, userDB *sql.DB, presence Presence, log *slog.Logger) *Notifier {
	return &Notifier{apns: apns, db: userDB, presence: presence, log: log, slots: make(chan struct{}, maxInFlight)}
}

// MessageDelivered is called after a message was appended to the user's
// log. The sender's own devices are not notified, nor anyone of notices,
// nor in a group a user who blocked the sender.
func (n *Notifier) MessageDelivered(userID ids.UUID, msg *pb.Message, group bool) {
	if n == nil || bytes.Equal(msg.GetSenderUserId(), userID[:]) || msg.GetNotice() != nil {
		return
	}
	var senderID ids.UUID
	if group {
		senderID, _ = ids.Parse(msg.GetSenderUserId())
	}
	n.background(userID, func(ctx context.Context) error { return n.notify(ctx, userID, senderID, msg) })
}

// MessageDeleted is called after a deletion was appended to the user's
// log. A background notification lets the app withdraw the message's
// alert if it showed one; none goes to a user who blocked the sender,
// when the sender is given.
func (n *Notifier) MessageDeleted(userID ids.UUID, deleted *pb.MessageDeletedEvent, senderID ids.UUID) {
	if n == nil {
		return
	}
	n.background(userID, func(ctx context.Context) error { return n.withdraw(ctx, userID, senderID, deleted) })
}

// registrationsFor returns the user's registered devices, none when the
// user blocked the sender, unless the sender is zero.
func (n *Notifier) registrationsFor(ctx context.Context, userID, senderID ids.UUID) ([]registration, error) {
	regs, err := n.registrations(ctx, userID)
	if err != nil || len(regs) == 0 || senderID.IsZero() {
		return regs, err
	}
	blocked, err := contacts.Blocks(ctx, n.db, userID, senderID)
	if err != nil || blocked {
		return nil, err
	}
	return regs, nil
}

// background runs a send in a goroutine, when a slot is free.
func (n *Notifier) background(userID ids.UUID, send func(ctx context.Context) error) {
	select {
	case n.slots <- struct{}{}:
	default:
		// APNs is slow or down; the device syncs when it opens anyway.
		metrics.PushSent.WithLabelValues("dropped").Inc()
		return
	}
	go func() {
		defer func() { <-n.slots }()
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		if err := send(ctx); err != nil {
			n.log.Warn("push failed", "user", userID, "err", err)
		}
	}()
}

type registration struct {
	deviceID uint64
	service  string
	token    string
}

func (n *Notifier) notify(ctx context.Context, userID, senderID ids.UUID, msg *pb.Message) error {
	regs, err := n.registrationsFor(ctx, userID, senderID)
	if err != nil || len(regs) == 0 {
		return err
	}
	conversationID, ok := ids.Parse(msg.GetConversationId())
	if !ok {
		return fmt.Errorf("message without a conversation id")
	}
	title, err := n.alertTitle(ctx, userID, conversationID)
	if err != nil {
		return err
	}
	badge, err := n.unread(ctx, userID)
	if err != nil {
		return err
	}
	return n.sendDisconnected(ctx, userID, regs, messageAlert(title, badge, conversationID, msg))
}

// messageAlert is the notification of a message: the server cannot read
// it, so the body is generic and the sealed content rides along, when it
// fits, for the app's extension to decrypt and show instead.
func messageAlert(title string, badge int, conversationID ids.UUID, msg *pb.Message) Alert {
	// Clients find the conversation by the lowercase hex of its id.
	alert := Alert{
		Title:          title,
		Body:           genericBody,
		Badge:          badge,
		ThreadID:       conversationID.String(),
		MutableContent: true,
		Data:           map[string]any{"conversation_id": conversationID.String(), "seq": msg.GetSeq()},
	}
	sealed := msg.GetSealed()
	if sealed == nil {
		return alert
	}
	plain := alert.Data
	alert.Data = maps.Clone(plain)
	alert.Data["epoch"] = sealed.GetEpoch()
	alert.Data["nonce"] = base64.StdEncoding.EncodeToString(sealed.GetNonce())
	alert.Data["ciphertext"] = base64.StdEncoding.EncodeToString(sealed.GetCiphertext())
	alert.Data["sender_user_id"] = hex.EncodeToString(msg.GetSenderUserId())
	alert.Data["client_message_id"] = msg.GetClientMessageId()
	if b, err := alert.payload(); err != nil || len(b) > maxPayload {
		alert.Data = plain
	}
	return alert
}

func (n *Notifier) withdraw(ctx context.Context, userID, senderID ids.UUID, deleted *pb.MessageDeletedEvent) error {
	regs, err := n.registrationsFor(ctx, userID, senderID)
	if err != nil || len(regs) == 0 {
		return err
	}
	conversationID, ok := ids.Parse(deleted.GetConversationId())
	if !ok {
		return fmt.Errorf("deletion without a conversation id")
	}
	alert := Alert{
		Background: true,
		Data:       map[string]any{"conversation_id": conversationID.String(), "seq": deleted.GetSeq(), "deleted": true},
	}
	return n.sendDisconnected(ctx, userID, regs, alert)
}

// sendDisconnected sends the alert to each registered device that is not
// connected.
func (n *Notifier) sendDisconnected(ctx context.Context, userID ids.UUID, regs []registration, alert Alert) error {
	connected, err := n.presence.ConnectedDevices(ctx, userID)
	if err != nil {
		return err
	}
	for _, r := range regs {
		if !connected[r.deviceID] {
			n.send(ctx, userID, r, alert)
		}
	}
	return nil
}

// alertTitle is the notification's title: in a direct conversation the
// peer's current name, from the conversation list; in a group the topic.
func (n *Notifier) alertTitle(ctx context.Context, userID, conversationID ids.UUID) (string, error) {
	var kind, peerName, topic string
	if err := n.db.QueryRowContext(ctx, `
		SELECT kind, peer_name, topic FROM user_conversation WHERE user_id = ? AND conversation_id = ?`,
		userID, conversationID).Scan(&kind, &peerName, &topic); err != nil {
		return "", fmt.Errorf("conversation entry: %w", err)
	}
	if kind != "group" {
		return peerName, nil
	}
	return topic, nil
}

func (n *Notifier) send(ctx context.Context, userID ids.UUID, r registration, alert Alert) {
	sandbox := r.service == pb.PushService_PUSH_SERVICE_APNS_SANDBOX.String()
	err := n.apns.Send(ctx, sandbox, r.token, alert)
	switch {
	case err == nil:
		metrics.PushSent.WithLabelValues("ok").Inc()
	case errors.Is(err, ErrUnregistered):
		metrics.PushSent.WithLabelValues("unregistered").Inc()
		if err := n.unregister(ctx, userID, r); err != nil {
			n.log.Warn("clearing push token failed", "err", err)
		}
	default:
		metrics.PushSent.WithLabelValues("error").Inc()
		n.log.Warn("push send failed", "user", userID, "device", r.deviceID, "err", err)
	}
}

func (n *Notifier) registrations(ctx context.Context, userID ids.UUID) ([]registration, error) {
	rows, err := n.db.QueryContext(ctx, `
		SELECT device_id, push_service, push_token FROM device
		WHERE user_id = ? AND push_token IS NOT NULL`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var regs []registration
	for rows.Next() {
		var r registration
		if err := rows.Scan(&r.deviceID, &r.service, &r.token); err != nil {
			return nil, err
		}
		regs = append(regs, r)
	}
	return regs, rows.Err()
}

// unread counts the user's unread messages across conversations, for the
// badge.
func (n *Notifier) unread(ctx context.Context, userID ids.UUID) (int, error) {
	var count int
	err := n.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(last_seq - read_seq), 0) FROM user_conversation WHERE user_id = ?`,
		userID).Scan(&count)
	return count, err
}

// unregister removes a token APNs rejected, unless the device has
// registered a new one meanwhile.
func (n *Notifier) unregister(ctx context.Context, userID ids.UUID, r registration) error {
	_, err := n.db.ExecContext(ctx, `
		UPDATE device SET push_service = NULL, push_token = NULL
		WHERE user_id = ? AND device_id = ? AND push_token = ?`,
		userID, r.deviceID, r.token)
	return err
}
