package devicesync

import (
	"context"
	"database/sql"
	"time"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
)

const trimPage = 1000

// Trimmer drops events older than the retention from the user logs, a page
// at a time on each shard, oldest first. Any number may run: raising a tail
// and dropping what is under it can be done twice.
type Trimmer struct {
	db        *sql.DB
	shards    *vtdb.ShardPools
	retention time.Duration
}

// NewTrimmer finds the oldest events of each shard of the keyspace through a
// connection to that shard alone, so a page never spans shards; without
// vtgate the keyspace is one shard.
func NewTrimmer(ctx context.Context, addr, keyspace string, db *sql.DB, retention time.Duration) (*Trimmer, error) {
	shards, err := vtdb.OpenShardPools(ctx, addr, keyspace, db)
	if err != nil {
		return nil, err
	}
	return &Trimmer{db: db, shards: shards, retention: retention}, nil
}

func (t *Trimmer) Close() { t.shards.Close() }

// Shards are the keyspace's shards, each a pool to that shard alone.
func (t *Trimmer) Shards() []vtdb.Shard { return t.shards.All() }

// TrimShard drops the shard's events older than the retention and returns
// how many.
func (t *Trimmer) TrimShard(ctx context.Context, shard vtdb.Shard) (int, error) {
	cutoff := vtdb.Now().Add(-t.retention)
	total := 0
	for {
		n, err := t.trimPage(ctx, shard.DB, cutoff)
		total += n
		if err != nil || n < trimPage {
			return total, err
		}
	}
}

type trimmed struct {
	userID ids.UUID
	tail   uint64
}

// trimPage drops the shard's oldest events, up to a page of them. A user's
// log is cut below the newest of its events in the page, so it stays
// contiguous even where an event's time is a little out of order.
func (t *Trimmer) trimPage(ctx context.Context, shard *sql.DB, cutoff time.Time) (int, error) {
	rows, err := shard.QueryContext(ctx, `
		SELECT user_id, position FROM user_event WHERE occurred_at < ?
		ORDER BY occurred_at LIMIT ?`,
		cutoff, trimPage)
	if err != nil {
		return 0, err
	}
	page, err := vtdb.Collect(rows, func(rows *sql.Rows) (e trimmed, err error) { return e, rows.Scan(&e.userID, &e.tail) })
	if err != nil {
		return 0, err
	}
	var users []trimmed
	index := map[ids.UUID]int{}
	for _, e := range page {
		if i, ok := index[e.userID]; ok {
			users[i].tail = max(users[i].tail, e.tail)
			continue
		}
		index[e.userID] = len(users)
		users = append(users, e)
	}
	for _, u := range users {
		if err := t.trimUser(ctx, u); err != nil {
			return 0, err
		}
	}
	return len(page), nil
}

// trimUser raises the user's tail and drops the events up to it in one
// transaction, so a sync that misses them sees the new tail.
func (t *Trimmer) trimUser(ctx context.Context, u trimmed) error {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE user SET log_tail = GREATEST(log_tail, ?) WHERE user_id = ?`, u.tail, u.userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM user_event WHERE user_id = ? AND position <= ?`, u.userID, u.tail); err != nil {
		return err
	}
	return tx.Commit()
}
