package conversation

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	movePage = 1000
	// moveBatch bounds the messages one transaction moves, and so how long
	// it holds the conversation's row.
	moveBatch = 200
)

// Mover moves messages older than the hot window to the cold tier,
// conversation by conversation, oldest first on each shard. Any number may
// run: each move happens under the conversation's row lock.
type Mover struct {
	db     *sql.DB
	shards *vtdb.ShardPools
	cold   *Cold
	window time.Duration
}

// NewMover finds the oldest messages of each shard of the keyspace through
// a connection to that shard alone.
func NewMover(ctx context.Context, addr, keyspace string, db *sql.DB, cold *Cold, window time.Duration) (*Mover, error) {
	shards, err := vtdb.OpenShardPools(ctx, addr, keyspace, db)
	if err != nil {
		return nil, err
	}
	return &Mover{db: db, shards: shards, cold: cold, window: window}, nil
}

func (m *Mover) Close() { m.shards.Close() }

// Shards are the keyspace's shards, each a pool to that shard alone.
func (m *Mover) Shards() []vtdb.Shard { return m.shards.All() }

// MoveShard moves the shard's messages older than the hot window and
// returns how many.
func (m *Mover) MoveShard(ctx context.Context, shard vtdb.Shard) (int, error) {
	return m.moveShard(ctx, shard.DB, vtdb.Now().Add(-m.window))
}

type aged struct {
	conversationID ids.UUID
	sentAt         time.Time
}

// moveShard takes the shard's conversations in the order of their oldest
// messages, a page at a time, each once. A conversation stops at its first
// message within the window, so one sent a little out of time order waits
// for the next round; the pages go on after the last one's time.
func (m *Mover) moveShard(ctx context.Context, shard *sql.DB, cutoff time.Time) (int, error) {
	total := 0
	done := map[ids.UUID]bool{}
	// Not the zero time, which DATETIME refuses.
	after := time.Unix(0, 0).UTC()
	for {
		rows, err := shard.QueryContext(ctx, `
			SELECT conversation_id, sent_at FROM message WHERE sent_at < ? AND sent_at >= ?
			ORDER BY sent_at LIMIT ?`,
			cutoff, after, movePage)
		if err != nil {
			return total, err
		}
		page, err := vtdb.Collect(rows, func(rows *sql.Rows) (a aged, err error) { return a, rows.Scan(&a.conversationID, &a.sentAt) })
		if err != nil {
			return total, err
		}
		fresh := false
		for _, a := range page {
			if done[a.conversationID] {
				continue
			}
			done[a.conversationID] = true
			fresh = true
			n, err := m.moveConversation(ctx, a.conversationID, cutoff)
			total += n
			if err != nil {
				return total, err
			}
		}
		if len(page) < movePage || !fresh {
			return total, nil
		}
		after = page[len(page)-1].sentAt
	}
}

// moveConversation moves the conversation's messages older than the
// cutoff, in seq order, a batch per transaction.
func (m *Mover) moveConversation(ctx context.Context, conversationID ids.UUID, cutoff time.Time) (int, error) {
	total := 0
	for {
		n, more, err := m.moveBatch(ctx, conversationID, cutoff)
		total += n
		if err != nil || !more {
			return total, err
		}
	}
}

// moveBatch writes the next batch to the cold tier, then, with the
// conversation's row locked, raises the watermark over it and deletes it
// from MySQL in one transaction. The puts come before the lock, so sends
// to the conversation never wait on the cold tier; under the lock the
// batch is read again, and a message deleted or reacted to meanwhile is put
// again. A failure after the puts leaves copies of rows still in MySQL,
// which the next move puts again.
func (m *Mover) moveBatch(ctx context.Context, conversationID ids.UUID, cutoff time.Time) (int, bool, error) {
	var coldSeq uint64
	if err := m.db.QueryRowContext(ctx, `
		SELECT cold_seq FROM conversation WHERE conversation_id = ?`, conversationID).Scan(&coldSeq); err != nil {
		return 0, false, err
	}
	staged, err := readBatch(ctx, m.db, conversationID, coldSeq, cutoff, moveBatch)
	if err != nil || len(staged) == 0 {
		return 0, false, err
	}
	if err := m.cold.Put(ctx, conversationID, staged); err != nil {
		return 0, false, err
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var locked uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT cold_seq FROM conversation WHERE conversation_id = ? FOR UPDATE`,
		conversationID).Scan(&locked); err != nil {
		return 0, false, err
	}
	if locked != coldSeq {
		// Another mover went first; read from its watermark.
		return 0, true, nil
	}
	n := len(staged)
	upTo := staged[n-1].seq
	current, err := readBatch(ctx, tx, conversationID, coldSeq, time.Time{}, n)
	if err != nil {
		return 0, false, err
	}
	if len(current) != n || current[n-1].seq != upTo {
		return 0, false, fmt.Errorf("batch of conversation %s changed from %d to %d messages", conversationID, n, len(current))
	}
	var changed []coldMessage
	for i := range current {
		if !current[i].equal(staged[i]) {
			changed = append(changed, current[i])
		}
	}
	if len(changed) > 0 {
		if err := m.cold.Put(ctx, conversationID, changed); err != nil {
			return 0, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation SET cold_seq = ? WHERE conversation_id = ?`, upTo, conversationID); err != nil {
		return 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM reaction WHERE conversation_id = ? AND seq > ? AND seq <= ?`, conversationID, coldSeq, upTo); err != nil {
		return 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM message WHERE conversation_id = ? AND seq > ? AND seq <= ?`, conversationID, coldSeq, upTo); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return n, n == moveBatch, nil
}

// readBatch reads up to limit messages after the watermark, in seq order,
// with their reactions, stopping at the first sent at or after the cutoff;
// a zero cutoff reads them all.
func readBatch(ctx context.Context, q vtdb.Querier, conversationID ids.UUID, coldSeq uint64, cutoff time.Time, limit int) ([]coldMessage, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT seq, sender_user_id, client_message_id, body, sent_at, outbox_id, blob_ids FROM message
		WHERE conversation_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		conversationID, coldSeq, limit)
	if err != nil {
		return nil, err
	}
	batch, err := vtdb.Collect(rows, func(rows *sql.Rows) (c coldMessage, err error) {
		return c, rows.Scan(&c.seq, &c.sender, &c.clientID, &c.body, &c.sentAt, &c.outboxID, &c.blobIDs)
	})
	if err != nil {
		return nil, err
	}
	n := 0
	for n < len(batch) && (cutoff.IsZero() || batch[n].sentAt.Before(cutoff)) {
		n++
	}
	if n == 0 {
		return nil, nil
	}
	batch = batch[:n]
	if err := addColdReactions(ctx, q, conversationID, coldSeq, batch[n-1].seq, batch); err != nil {
		return nil, err
	}
	return batch, nil
}

// addColdReactions reads the reactions to the messages, which are sorted by
// seq and span (from, to], into their cold rows.
func addColdReactions(ctx context.Context, q vtdb.Querier, conversationID ids.UUID, from, to uint64, messages []coldMessage) error {
	rows, err := q.QueryContext(ctx, `
		SELECT seq, user_id, body, updated_at FROM reaction
		WHERE conversation_id = ? AND seq > ? AND seq <= ? ORDER BY seq, updated_at, user_id`,
		conversationID, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	bySeq := map[uint64][]*pb.Reaction{}
	for rows.Next() {
		var seq uint64
		var userID, body []byte
		var updatedAt time.Time
		if err := rows.Scan(&seq, &userID, &body, &updatedAt); err != nil {
			return err
		}
		sealed := &pb.Sealed{}
		if err := proto.Unmarshal(body, sealed); err != nil {
			return fmt.Errorf("unmarshal reaction: %w", err)
		}
		bySeq[seq] = append(bySeq[seq], &pb.Reaction{UserId: userID, Sealed: sealed, UpdatedAt: timestamppb.New(updatedAt)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range messages {
		if messages[i].reactions, err = encodeReactions(bySeq[messages[i].seq]); err != nil {
			return err
		}
	}
	return nil
}
