package push

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Presence tells which of a user's devices are connected to some gateway.
type Presence interface {
	ConnectedDevices(ctx context.Context, userID ids.UUID) (map[uint64]bool, error)
}

// Notifier pushes a message to a user's devices that are not connected and
// have registered a token. Sends run in the background, bounded, so the
// fan-out never waits for APNs.
type Notifier struct {
	apns     *APNs
	db       *sql.DB
	presence Presence
	log      *slog.Logger
	slots    chan struct{}
}

const (
	maxInFlight  = 64
	maxBodyRunes = 180
	sendTimeout  = 10 * time.Second
)

func NewNotifier(apns *APNs, userDB *sql.DB, presence Presence, log *slog.Logger) *Notifier {
	return &Notifier{apns: apns, db: userDB, presence: presence, log: log, slots: make(chan struct{}, maxInFlight)}
}

// MessageDelivered is called after a message was appended to the user's
// log. The sender's own devices are not notified, nor anyone of notices.
func (n *Notifier) MessageDelivered(userID ids.UUID, msg *pb.Message) {
	if n == nil || bytes.Equal(msg.GetSenderUserId(), userID[:]) || msg.GetNotice() != nil {
		return
	}
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
		if err := n.notify(ctx, userID, msg); err != nil {
			n.log.Warn("push failed", "user", userID, "err", err)
		}
	}()
}

type registration struct {
	deviceID uint64
	service  string
	token    string
}

func (n *Notifier) notify(ctx context.Context, userID ids.UUID, msg *pb.Message) error {
	regs, err := n.registrations(ctx, userID)
	if err != nil || len(regs) == 0 {
		return err
	}
	conversationID, ok := ids.Parse(msg.GetConversationId())
	if !ok {
		return fmt.Errorf("message without a conversation id")
	}
	title, body, err := n.alertText(ctx, userID, conversationID, msg)
	if err != nil {
		return err
	}
	connected, err := n.presence.ConnectedDevices(ctx, userID)
	if err != nil {
		return err
	}
	badge, err := n.unread(ctx, userID)
	if err != nil {
		return err
	}
	// Clients find the conversation by the lowercase hex of its id.
	alert := Alert{
		Title:    title,
		Body:     truncate(body, maxBodyRunes),
		Badge:    badge,
		ThreadID: conversationID.String(),
		Data:     map[string]string{"conversation_id": conversationID.String()},
	}
	for _, r := range regs {
		if !connected[r.deviceID] {
			n.send(ctx, userID, r, alert)
		}
	}
	return nil
}

// alertText is the notification's title and body: in a direct
// conversation the peer's current name, from the conversation list, and
// the text; in a group the topic and the text after the sender's name.
func (n *Notifier) alertText(ctx context.Context, userID, conversationID ids.UUID, msg *pb.Message) (string, string, error) {
	var kind, peerName, topic string
	if err := n.db.QueryRowContext(ctx, `
		SELECT kind, peer_name, topic FROM user_conversation WHERE user_id = ? AND conversation_id = ?`,
		userID, conversationID).Scan(&kind, &peerName, &topic); err != nil {
		return "", "", fmt.Errorf("conversation entry: %w", err)
	}
	if kind != "group" {
		return peerName, msg.GetText(), nil
	}
	var sender string
	err := n.db.QueryRowContext(ctx, `SELECT name FROM user WHERE user_id = ?`, msg.GetSenderUserId()).Scan(&sender)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("sender name: %w", err)
	}
	return topic, sender + ": " + msg.GetText(), nil
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

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
