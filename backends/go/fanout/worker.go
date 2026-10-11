// Package fanout turns committed outbox rows, read from Kafka, into entries
// in each participant's event log, and pushes them to connected devices.
package fanout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const Topic = "wrinkle.conversation.outbox"

type Publisher interface {
	Publish(ctx context.Context, userID uint64, payload []byte) error
}

// Notifier is told about messages appended to a user's log, to push them to
// devices that are not connected. Nil disables it.
type Notifier interface {
	MessageDelivered(userID uint64, peerName string, msg *pb.Message)
}

type Worker struct {
	db          *sql.DB
	kafka       *kgo.Client
	pub         Publisher
	notifier    Notifier
	log         *slog.Logger
	concurrency int
	names       nameCache
}

// New makes a worker that delivers up to concurrency conversations at a time.
func New(ctx context.Context, brokers []string, group string, concurrency int, db *sql.DB, pub Publisher, notifier Notifier, log *slog.Logger) (*Worker, error) {
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
	return &Worker{db: db, kafka: client, pub: pub, notifier: notifier, log: log, concurrency: max(concurrency, 1)}, nil
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
		ok := w.handleFetches(ctx, fetches)
		w.kafka.AllowRebalance()
		if !ok {
			// Leave the offsets where they are and poll again.
			time.Sleep(time.Second)
			continue
		}
		if err := w.kafka.CommitUncommittedOffsets(ctx); err != nil && ctx.Err() == nil {
			w.log.Warn("commit failed", "err", err)
		}
	}
}

// handleFetches handles conversations in parallel and a conversation's
// records in order: that is the ordering the contract promises, and a
// user's log is serialized by its row lock whatever the interleaving. It
// reports whether every record was handled.
func (w *Worker) handleFetches(ctx context.Context, fetches kgo.Fetches) bool {
	var failed atomic.Bool
	var wg sync.WaitGroup
	sem := make(chan struct{}, w.concurrency)
	fetches.EachPartition(func(p kgo.FetchTopicPartition) {
		for _, records := range byConversation(p.Records) {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				for _, rec := range records {
					if failed.Load() {
						return
					}
					if err := w.handleWithRetry(ctx, rec); err != nil {
						failed.Store(true)
						w.log.Error("giving up on record", "partition", rec.Partition, "offset", rec.Offset, "err", err)
						return
					}
				}
			}()
		}
	})
	wg.Wait()
	return !failed.Load()
}

// byConversation groups records by key, the conversation id, keeping each
// group in offset order.
func byConversation(records []*kgo.Record) [][]*kgo.Record {
	index := map[string]int{}
	var groups [][]*kgo.Record
	for _, rec := range records {
		key := string(rec.Key)
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], rec)
	}
	return groups
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

func (w *Worker) handle(ctx context.Context, rec *kgo.Record) error {
	if len(rec.Value) == 0 {
		return nil
	}
	metrics.FanoutRecords.Inc()
	o, err := parseOutboxRow(rec.Value)
	if err != nil {
		// A malformed row never gets better; log it and move on.
		w.log.Error("skipping outbox record", "offset", rec.Offset, "err", err)
		return nil
	}
	if o.event.GetBody() == nil {
		w.log.Error("skipping outbox record without a body", "kind", o.kind, "offset", rec.Offset)
		return nil
	}
	errs := make(chan error, 2)
	for _, userID := range []uint64{o.userA, o.userB} {
		go func() {
			if err := w.deliver(ctx, userID, o); err != nil {
				errs <- fmt.Errorf("deliver to user %d: %w", userID, err)
				return
			}
			errs <- nil
		}()
	}
	var joined error
	for range 2 {
		joined = errors.Join(joined, <-errs)
	}
	return joined
}
