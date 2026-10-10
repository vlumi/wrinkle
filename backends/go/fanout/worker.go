// Package fanout turns committed outbox rows, read from Kafka, into entries
// in each participant's event log, and pushes them to connected devices.
package fanout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const Topic = "wrinkle.conversation.outbox"

type Publisher interface {
	Publish(ctx context.Context, userID uint64, payload []byte) error
}

type Worker struct {
	db    *sql.DB
	kafka *kgo.Client
	pub   Publisher
	log   *slog.Logger

	namesMu sync.Mutex
	names   map[uint64]string
}

func New(ctx context.Context, brokers []string, group string, db *sql.DB, pub Publisher, log *slog.Logger) (*Worker, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(Topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		// A worker that dies without leaving holds its partitions until
		// the session expires; keep that short.
		kgo.SessionTimeout(10*time.Second),
		kgo.HeartbeatInterval(3*time.Second),
	)
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("kafka at %v: %w", brokers, err)
	}
	return &Worker{db: db, kafka: client, pub: pub, log: log, names: map[uint64]string{}}, nil
}

// Close leaves the consumer group. Polling blocks rebalances until the
// fetched records are handled, so closing has to allow one.
func (w *Worker) Close() { w.kafka.CloseAllowingRebalance() }

// Run consumes until the context ends. A record is committed only after
// every recipient's log has it; a failing record is retried, not skipped.
func (w *Worker) Run(ctx context.Context) error {
	for {
		fetches := w.kafka.PollFetches(ctx)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			w.kafka.AllowRebalance()
			return ctx.Err()
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			w.log.Warn("fetch error", "topic", topic, "partition", partition, "err", err)
		})
		var failed bool
		fetches.EachRecord(func(rec *kgo.Record) {
			if failed {
				return
			}
			if err := w.handleWithRetry(ctx, rec); err != nil {
				failed = true
				w.log.Error("giving up on record", "partition", rec.Partition, "offset", rec.Offset, "err", err)
			}
		})
		w.kafka.AllowRebalance()
		if failed {
			// Leave the offsets where they are and poll again.
			time.Sleep(time.Second)
			continue
		}
		if err := w.kafka.CommitUncommittedOffsets(ctx); err != nil && ctx.Err() == nil {
			w.log.Warn("commit failed", "err", err)
		}
	}
}

func (w *Worker) handleWithRetry(ctx context.Context, rec *kgo.Record) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = w.handle(ctx, rec); err == nil {
			return nil
		}
		w.log.Warn("delivery failed", "attempt", attempt+1, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
		}
	}
	return err
}

// row is the outbox row as Debezium emits it after unwrapping: unsigned
// bigints as decimal strings, bytes as base64.
type row struct {
	ConversationID string `json:"conversation_id"`
	OutboxID       string `json:"outbox_id"`
	UserA          string `json:"user_a"`
	UserB          string `json:"user_b"`
	Kind           string `json:"kind"`
	Payload        []byte `json:"payload"`
}

type outboxRow struct {
	conversationID, outboxID, userA, userB uint64
	kind                                   string
	event                                  *pb.Event
}

func parse(value []byte) (*outboxRow, error) {
	var r row
	if err := json.Unmarshal(value, &r); err != nil {
		return nil, err
	}
	var o outboxRow
	var err error
	for _, f := range []struct {
		dst *uint64
		src string
	}{{&o.conversationID, r.ConversationID}, {&o.outboxID, r.OutboxID}, {&o.userA, r.UserA}, {&o.userB, r.UserB}} {
		if *f.dst, err = strconv.ParseUint(f.src, 10, 64); err != nil {
			return nil, fmt.Errorf("outbox row id %q: %w", f.src, err)
		}
	}
	o.kind = r.Kind
	o.event = &pb.Event{}
	if err := proto.Unmarshal(r.Payload, o.event); err != nil {
		return nil, fmt.Errorf("outbox payload: %w", err)
	}
	return &o, nil
}

func (w *Worker) handle(ctx context.Context, rec *kgo.Record) error {
	if len(rec.Value) == 0 {
		return nil
	}
	metrics.FanoutRecords.Inc()
	o, err := parse(rec.Value)
	if err != nil {
		// A malformed row never gets better; log it and move on.
		w.log.Error("skipping outbox record", "offset", rec.Offset, "err", err)
		return nil
	}
	if o.event.GetBody() == nil {
		w.log.Error("skipping outbox record without a body", "kind", o.kind, "offset", rec.Offset)
		return nil
	}
	for _, userID := range []uint64{o.userA, o.userB} {
		if err := w.deliver(ctx, userID, o); err != nil {
			return fmt.Errorf("deliver to user %d: %w", userID, err)
		}
	}
	return nil
}

// deliver appends the event to one user's log, in one transaction with the
// conversation list update, then pushes it.
func (w *Worker) deliver(ctx context.Context, userID uint64, o *outboxRow) error {
	peerID := o.userA
	if userID == o.userA {
		peerID = o.userB
	}
	var peerName string
	if msg := o.event.GetMessage(); msg != nil {
		var err error
		if peerName, err = w.peerName(ctx, peerID); err != nil {
			return err
		}
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var head uint64
	err = tx.QueryRowContext(ctx, `SELECT log_head FROM user WHERE user_id = ? FOR UPDATE`, userID).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		w.log.Warn("no such user, dropping event", "user", userID)
		return nil
	}
	if err != nil {
		return err
	}
	var existing uint64
	err = tx.QueryRowContext(ctx, `
		SELECT position FROM user_event WHERE user_id = ? AND conversation_id = ? AND outbox_id = ?`,
		userID, o.conversationID, o.outboxID).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	event := proto.Clone(o.event).(*pb.Event)
	event.Cursor = head + 1
	payload, err := proto.Marshal(event)
	if err != nil {
		return err
	}
	occurredAt := event.GetOccurredAt().AsTime()
	if _, err := tx.ExecContext(ctx, `UPDATE user SET log_head = ? WHERE user_id = ?`, event.Cursor, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_event (user_id, position, conversation_id, outbox_id, payload, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		userID, event.Cursor, o.conversationID, o.outboxID, payload, occurredAt); err != nil {
		return err
	}

	switch body := event.GetBody().(type) {
	case *pb.Event_Message:
		msg := body.Message.GetMessage()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_conversation
				(user_id, conversation_id, peer_user_id, peer_name, last_seq,
				 last_sender_user_id, last_client_message_id, last_body, last_sent_at,
				 read_seq, peer_read_seq, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?)
			ON DUPLICATE KEY UPDATE
				last_seq = GREATEST(last_seq, ?),
				last_sender_user_id = IF(? >= last_seq, ?, last_sender_user_id),
				last_client_message_id = IF(? >= last_seq, ?, last_client_message_id),
				last_body = IF(? >= last_seq, ?, last_body),
				last_sent_at = IF(? >= last_seq, ?, last_sent_at),
				updated_at = GREATEST(updated_at, ?)`,
			userID, o.conversationID, peerID, peerName, msg.GetSeq(),
			msg.GetSenderUserId(), msg.GetClientMessageId(), msg.GetText(), msg.GetSentAt().AsTime(),
			occurredAt,
			msg.GetSeq(),
			msg.GetSeq(), msg.GetSenderUserId(),
			msg.GetSeq(), msg.GetClientMessageId(),
			msg.GetSeq(), msg.GetText(),
			msg.GetSeq(), msg.GetSentAt().AsTime(),
			occurredAt); err != nil {
			return err
		}
	case *pb.Event_Read:
		column := "peer_read_seq"
		if body.Read.GetUserId() == userID {
			column = "read_seq"
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE user_conversation SET `+column+` = GREATEST(`+column+`, ?)
			WHERE user_id = ? AND conversation_id = ?`,
			body.Read.GetSeq(), userID, o.conversationID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	metrics.FanoutDeliveries.Inc()
	metrics.FanoutLag.Observe(time.Since(occurredAt).Seconds())
	if err := w.pub.Publish(ctx, userID, payload); err != nil {
		// The log has it; devices catch up on their next sync.
		w.log.Warn("push failed", "user", userID, "err", err)
	}
	return nil
}

func (w *Worker) peerName(ctx context.Context, userID uint64) (string, error) {
	w.namesMu.Lock()
	name, ok := w.names[userID]
	w.namesMu.Unlock()
	if ok {
		return name, nil
	}
	if err := w.db.QueryRowContext(ctx, `SELECT name FROM user WHERE user_id = ?`, userID).Scan(&name); err != nil {
		return "", fmt.Errorf("name of user %d: %w", userID, err)
	}
	w.namesMu.Lock()
	w.names[userID] = name
	w.namesMu.Unlock()
	return name, nil
}
