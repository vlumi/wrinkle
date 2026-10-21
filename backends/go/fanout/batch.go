package fanout

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// maxBatch bounds the appends in one batch transaction.
const maxBatch = 256

// errBatch tells the appends of a failed batch to go one at a time.
var errBatch = errors.New("batch failed")

// batchable reports whether a row's appends can share transactions with
// other rows': a message that is not a notice, the common case, whose list
// update needs no flags.
func batchable(o *outboxRow) bool {
	msg := o.event.GetMessage().GetMessage()
	return msg != nil && msg.GetNotice() == nil
}

// pendingAppend is one recipient's copy of a message waiting for its
// batch.
type pendingAppend struct {
	entry listEntry
	// event is what was stored, nil when nothing was (a deleted or unknown
	// user); err is errBatch when the batch failed.
	event   *pb.Event
	payload []byte
	err     error
	done    chan struct{}
}

// batcher collects appends from rows being delivered at the same time and
// stores them in one transaction per shard of the user keyspace: while one
// shard's transaction runs, the next batch for it gathers, so batches grow
// with the load.
type batcher struct {
	w      *Worker
	ctx    context.Context
	mu     sync.Mutex
	queues map[string]*shardQueue
}

type shardQueue struct {
	pending []*pendingAppend
	running bool
}

func newBatcher(ctx context.Context, w *Worker) *batcher {
	return &batcher{w: w, ctx: ctx, queues: map[string]*shardQueue{}}
}

// add queues an append on its user's shard.
func (b *batcher) add(p *pendingAppend) {
	shard := b.w.shards.Of(p.entry.userID[:])
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queues[shard]
	if q == nil {
		q = &shardQueue{}
		b.queues[shard] = q
	}
	q.pending = append(q.pending, p)
	if !q.running {
		q.running = true
		go b.run(q)
	}
}

// run stores the shard's queued appends, a batch at a time, until none is
// left.
func (b *batcher) run(q *shardQueue) {
	for {
		b.mu.Lock()
		n := min(len(q.pending), maxBatch)
		if n == 0 {
			q.running = false
			b.mu.Unlock()
			return
		}
		batch := q.pending[:n:n]
		q.pending = q.pending[n:]
		b.mu.Unlock()

		ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
		err := b.w.storeBatch(ctx, batch)
		cancel()
		for _, p := range batch {
			if err != nil {
				p.event, p.payload, p.err = nil, nil, errBatch
			}
			close(p.done)
		}
		if err != nil && b.ctx.Err() == nil {
			b.w.log.Debug("batch failed, appending one at a time", "appends", len(batch), "err", err)
		}
	}
}

// deliverBatched appends a message to each recipient's log through the
// batcher, falling back to one transaction per recipient for those whose
// batch failed (a redelivered row, a deadlock), and then pushes it.
func (w *Worker) deliverBatched(ctx context.Context, o *outboxRow) error {
	appends := make([]*pendingAppend, 0, len(o.recipients))
	for _, userID := range o.recipients {
		entry := listEntry{userID: userID, o: o}
		if o.direct() {
			entry.peerID = o.peerOf(userID)
			var err error
			if entry.peer, err = w.peerForNewEntry(ctx, userID, o.conversationID, entry.peerID); err != nil {
				return err
			}
		}
		p := &pendingAppend{entry: entry, done: make(chan struct{})}
		appends = append(appends, p)
		w.batches.add(p)
	}
	var fallback []ids.UUID
	for _, p := range appends {
		select {
		case <-p.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if p.err != nil {
			fallback = append(fallback, p.entry.userID)
			continue
		}
		if p.event != nil {
			w.delivered(ctx, p.entry.userID, p.event, p.payload)
			w.messageDelivered(p.entry.userID, o, p.event)
		}
	}
	for _, userID := range fallback {
		if err := w.deliver(ctx, userID, o); err != nil {
			return err
		}
	}
	return nil
}

// storeBatch appends each pending message to its recipient's log and list,
// in one transaction: it locks the users' rows in id order, so batches
// never wait on each other in a cycle, numbers the events after each log's
// head, and writes the events, the heads and the list entries a statement
// each.
func (w *Worker) storeBatch(ctx context.Context, batch []*pendingAppend) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	users := make([]any, 0, len(batch))
	seen := map[ids.UUID]bool{}
	for _, p := range batch {
		if !seen[p.entry.userID] {
			seen[p.entry.userID] = true
			users = append(users, p.entry.userID)
		}
	}
	marks := placeholders(len(users))
	rows, err := tx.QueryContext(ctx, `
		SELECT user_id, log_head, deleted_at FROM user WHERE user_id IN (`+marks+`) ORDER BY user_id FOR UPDATE`, users...)
	if err != nil {
		return err
	}
	heads := map[ids.UUID]uint64{}
	for rows.Next() {
		var id ids.UUID
		var head uint64
		var deletedAt sql.NullTime
		if err := rows.Scan(&id, &head, &deletedAt); err != nil {
			rows.Close()
			return err
		}
		if !deletedAt.Valid {
			heads[id] = head
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var events, lists []any
	var stored []*pendingAppend
	for _, p := range batch {
		head, ok := heads[p.entry.userID]
		if !ok {
			continue
		}
		event := proto.Clone(p.entry.o.event).(*pb.Event)
		event.Cursor = head + 1
		heads[p.entry.userID] = event.Cursor
		payload, err := proto.Marshal(event)
		if err != nil {
			return err
		}
		p.event, p.payload = event, payload
		stored = append(stored, p)
		events = append(events, p.entry.userID, event.Cursor, p.entry.o.conversationID, p.entry.o.outboxID, payload, event.GetOccurredAt().AsTime())
		row, err := p.entry.listRow(event)
		if err != nil {
			return err
		}
		lists = append(lists, row...)
	}
	if len(stored) > 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_event (user_id, position, source_id, outbox_id, payload, occurred_at)
			VALUES `+rowsOf(len(stored), 6), events...); err != nil {
			return err
		}
		var cases strings.Builder
		var args, in []any
		for id, head := range heads {
			cases.WriteString(" WHEN ? THEN ?")
			args = append(args, id, head)
			in = append(in, id)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE user SET log_head = CASE user_id`+cases.String()+` END
			WHERE user_id IN (`+placeholders(len(in))+`)`, append(args, in...)...); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_conversation
				(user_id, conversation_id, kind, peer_user_id, peer_name, peer_icon, peer_avatar, peer_deleted,
				 topic, member_count, member, admin, last_seq,
				 last_sender_user_id, last_client_message_id, last_body, last_sent_at,
				 read_seq, peer_read_seq, updated_at)
			VALUES `+rowsOf(len(stored), 20)+`
			ON DUPLICATE KEY UPDATE
				last_seq = GREATEST(last_seq, VALUES(last_seq)),
				topic = IF(VALUES(last_seq) >= last_seq, VALUES(topic), topic),
				member_count = IF(VALUES(last_seq) >= last_seq, VALUES(member_count), member_count),
				last_sender_user_id = IF(VALUES(last_seq) >= last_seq, VALUES(last_sender_user_id), last_sender_user_id),
				last_client_message_id = IF(VALUES(last_seq) >= last_seq, VALUES(last_client_message_id), last_client_message_id),
				last_body = IF(VALUES(last_seq) >= last_seq, VALUES(last_body), last_body),
				last_sent_at = IF(VALUES(last_seq) >= last_seq, VALUES(last_sent_at), last_sent_at),
				read_seq = GREATEST(read_seq, VALUES(read_seq)),
				updated_at = GREATEST(updated_at, VALUES(updated_at))`, lists...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// listRow is the values of the user's list entry for a message that is not
// a notice, as recordMessage inserts them.
func (e listEntry) listRow(event *pb.Event) ([]any, error) {
	msg := event.GetMessage().GetMessage()
	var ownSeq uint64
	if bytes.Equal(msg.GetSenderUserId(), e.userID[:]) {
		ownSeq = msg.GetSeq()
	}
	var peerColumn any
	if e.o.direct() {
		peerColumn = e.peerID
	}
	body, err := proto.Marshal(&pb.Message{Content: msg.GetContent(), BlobIds: msg.GetBlobIds()})
	if err != nil {
		return nil, err
	}
	peer := e.peer
	return []any{
		e.userID, e.o.conversationID, e.o.conversationKind, peerColumn, peer.GetName(), peer.GetIcon(), vtdb.NullBytes(peer.GetAvatar()), peer.GetDeleted(),
		e.o.topic, e.o.memberCount, true, false, msg.GetSeq(),
		msg.GetSenderUserId(), msg.GetClientMessageId(), body, msg.GetSentAt().AsTime(),
		ownSeq, 0, event.GetOccurredAt().AsTime(),
	}, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// rowsOf is n rows of a multi-row VALUES list of the given width.
func rowsOf(n, width int) string {
	row := "(" + placeholders(width) + ")"
	return strings.TrimSuffix(strings.Repeat(row+", ", n), ", ")
}
