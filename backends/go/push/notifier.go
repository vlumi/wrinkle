package push

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Presence tells which of a user's devices are connected to some gateway.
type Presence interface {
	ConnectedDevices(ctx context.Context, userID uint64) (map[uint64]bool, error)
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
// log. The sender's own devices are not notified.
func (n *Notifier) MessageDelivered(userID uint64, peerName string, msg *pb.Message) {
	if n == nil || msg.GetSenderUserId() == userID {
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
		if err := n.notify(ctx, userID, peerName, msg); err != nil {
			n.log.Warn("push failed", "user", userID, "err", err)
		}
	}()
}

type registration struct {
	deviceID uint64
	service  string
	token    string
}

func (n *Notifier) notify(ctx context.Context, userID uint64, peerName string, msg *pb.Message) error {
	rows, err := n.db.QueryContext(ctx, `
		SELECT device_id, push_service, push_token FROM device
		WHERE user_id = ? AND push_token IS NOT NULL`, userID)
	if err != nil {
		return err
	}
	var regs []registration
	for rows.Next() {
		var r registration
		if err := rows.Scan(&r.deviceID, &r.service, &r.token); err != nil {
			rows.Close()
			return err
		}
		regs = append(regs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(regs) == 0 {
		return err
	}
	connected, err := n.presence.ConnectedDevices(ctx, userID)
	if err != nil {
		return err
	}
	var badge int
	if err := n.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(last_seq - read_seq), 0) FROM user_conversation WHERE user_id = ?`,
		userID).Scan(&badge); err != nil {
		return err
	}
	alert := Alert{
		Title:    peerName,
		Body:     truncate(msg.GetText(), maxBodyRunes),
		Badge:    badge,
		ThreadID: strconv.FormatUint(msg.GetConversationId(), 10),
		Data:     map[string]string{"conversation_id": strconv.FormatUint(msg.GetConversationId(), 10)},
	}
	for _, r := range regs {
		if connected[r.deviceID] {
			continue
		}
		sandbox := r.service == pb.PushService_PUSH_SERVICE_APNS_SANDBOX.String()
		err := n.apns.Send(ctx, sandbox, r.token, alert)
		switch {
		case err == nil:
			metrics.PushSent.WithLabelValues("ok").Inc()
		case errors.Is(err, ErrUnregistered):
			metrics.PushSent.WithLabelValues("unregistered").Inc()
			// Only if the device has not registered a new token meanwhile.
			if _, err := n.db.ExecContext(ctx, `
				UPDATE device SET push_service = NULL, push_token = NULL
				WHERE user_id = ? AND device_id = ? AND push_token = ?`,
				userID, r.deviceID, r.token); err != nil {
				n.log.Warn("clearing push token failed", "err", err)
			}
		default:
			metrics.PushSent.WithLabelValues("error").Inc()
			n.log.Warn("push send failed", "user", userID, "device", r.deviceID, "err", err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
