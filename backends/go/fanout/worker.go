// Package fanout turns committed outbox rows, read from Kafka, into entries
// in the event logs of everyone concerned, and pushes them to connected
// devices: a conversation's events to the recipients its row names, a
// user's profile changes to the user and every direct conversation partner,
// a user's device changes to the user and, for device keys, to everyone in
// a conversation with the user, and a user's contact changes to both users.
package fanout

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"golang.org/x/sync/errgroup"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// The outbox topics: conversations' rows keyed by conversation id, users'
// by user id.
const (
	ConversationTopic = "wrinkle.conversation.outbox"
	UserTopic         = "wrinkle.user.user_outbox"
)

// recipientConcurrency is how many recipients of one row are delivered to
// at a time.
const recipientConcurrency = 16

// Topics are the topics the worker consumes.
var Topics = []string{ConversationTopic, UserTopic}

type Publisher interface {
	Publish(ctx context.Context, userID ids.UUID, payload []byte) error
}

// Notifier is told about messages and deletions appended to a user's log,
// to push them to devices that are not connected. Nil disables it. In a
// group, a user who blocked the sender gets no push; the sender of a
// deletion is zero when it is not known or not needed.
type Notifier interface {
	MessageDelivered(userID ids.UUID, msg *pb.Message, group bool)
	MessageDeleted(userID ids.UUID, deleted *pb.MessageDeletedEvent, senderID ids.UUID)
}

type Worker struct {
	db *sql.DB
	// The conversation keyspace, for the members of groups.
	conversations *sql.DB
	kafka         *kgo.Client
	pub           Publisher
	notifier      Notifier
	log           *slog.Logger
	concurrency   int
	listed        listedCache
	// shards places users on the user keyspace's shards, for batches.
	shards  vtdb.Shards
	batches *batcher
}

// Config is what a worker needs: the brokers and consumer group, the user
// keyspace it delivers into and the conversation keyspace it reads groups
// from, where to publish and, optionally, whom to notify.
type Config struct {
	Brokers []string
	Group   string
	// Concurrency is how many conversations or users are delivered at a time.
	Concurrency   int
	DB            *sql.DB
	Conversations *sql.DB
	// UserKeyspace names DB's keyspace, whose shards batches follow.
	UserKeyspace string
	Publisher    Publisher
	Notifier     Notifier
	Log          *slog.Logger
}

// New connects the worker to the brokers.
func New(ctx context.Context, cfg Config) (*Worker, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(Topics...),
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
		return nil, fmt.Errorf("kafka at %v: %w", cfg.Brokers, err)
	}
	shards, err := vtdb.LoadShards(ctx, cfg.DB, cfg.UserKeyspace)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("shards of %s: %w", cfg.UserKeyspace, err)
	}
	return &Worker{
		db: cfg.DB, conversations: cfg.Conversations, kafka: client, pub: cfg.Publisher, notifier: cfg.Notifier,
		log: cfg.Log, concurrency: max(cfg.Concurrency, 1), shards: shards,
	}, nil
}

// Close leaves the consumer group. Polling blocks rebalances until the
// fetched records are handled, so closing has to allow one.
func (w *Worker) Close() { w.kafka.CloseAllowingRebalance() }

// Run consumes until the context ends. A record is committed only after
// every recipient's log has it; a failing record is retried, not skipped.
func (w *Worker) Run(ctx context.Context) error {
	w.batches = newBatcher(ctx, w)
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
		if !ok {
			// Fetch the batch again; records already delivered are skipped
			// by the logs' source key.
			w.kafka.SetOffsets(batchStarts(fetches))
		}
		w.kafka.AllowRebalance()
		if !ok {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		if err := w.kafka.CommitUncommittedOffsets(ctx); err != nil && ctx.Err() == nil {
			w.log.Warn("commit failed", "err", err)
		}
	}
}

// handleFetches handles keys (conversations, users) in parallel and a key's
// records in order: that is the ordering the contract promises, and a
// user's log is serialized by its row lock whatever the interleaving. It
// reports whether every record was handled.
func (w *Worker) handleFetches(ctx context.Context, fetches kgo.Fetches) bool {
	var failed atomic.Bool
	var wg sync.WaitGroup
	sem := make(chan struct{}, w.concurrency)
	fetches.EachPartition(func(p kgo.FetchTopicPartition) {
		for _, records := range byKey(p.Records) {
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

// batchStarts returns the offset of each partition's first record in the
// fetches.
func batchStarts(fetches kgo.Fetches) map[string]map[int32]kgo.EpochOffset {
	starts := map[string]map[int32]kgo.EpochOffset{}
	fetches.EachPartition(func(p kgo.FetchTopicPartition) {
		if len(p.Records) == 0 {
			return
		}
		if starts[p.Topic] == nil {
			starts[p.Topic] = map[int32]kgo.EpochOffset{}
		}
		first := p.Records[0]
		starts[p.Topic][p.Partition] = kgo.EpochOffset{Epoch: first.LeaderEpoch, Offset: first.Offset}
	})
	return starts
}

// byKey groups records by key, the conversation or user id, keeping each
// group in offset order.
func byKey(records []*kgo.Record) [][]*kgo.Record {
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
	if rec.Topic == UserTopic {
		return w.handleUserRecord(ctx, rec)
	}
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
	if batchable(o) {
		return w.deliverBatched(ctx, o)
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(recipientConcurrency)
	for _, userID := range o.recipients {
		g.Go(func() error {
			if err := w.deliver(gctx, userID, o); err != nil {
				return fmt.Errorf("deliver to user %s: %w", userID, err)
			}
			return nil
		})
	}
	return g.Wait()
}

func (w *Worker) handleUserRecord(ctx context.Context, rec *kgo.Record) error {
	o, err := parseUserOutboxRow(rec.Value)
	if err != nil {
		w.log.Error("skipping user outbox record", "offset", rec.Offset, "err", err)
		return nil
	}
	switch body := o.event.GetBody().(type) {
	case *pb.Event_Profile:
		switch {
		case body.Profile.GetUser() == nil:
		case body.Profile.GetUser().GetDeleted():
			return w.deliverDeletion(ctx, o)
		default:
			return w.deliverProfile(ctx, o)
		}
	case *pb.Event_Device:
		if body.Device.GetDevice() != nil {
			return w.deliverDevice(ctx, o)
		}
	case *pb.Event_Contact:
		return w.deliverContact(ctx, o)
	case *pb.Event_Palette:
		// To the user's own devices only.
		if _, err := w.append(ctx, o.userID, o.userID, o.outboxID, o.event, nil); err != nil {
			return fmt.Errorf("deliver to user %s: %w", o.userID, err)
		}
		return nil
	}
	w.log.Error("skipping user outbox record without a known body", "kind", o.kind, "offset", rec.Offset)
	return nil
}
