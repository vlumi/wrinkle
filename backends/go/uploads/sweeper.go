package uploads

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/vlumi/wrinkle/backends/go/internal/blobs"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
)

const (
	sweepFirst    = time.Minute
	sweepInterval = 10 * time.Minute
	// unusedFor is how long a slot may go unused, or an attachment wait for
	// its message's delivery, before its blob is deleted.
	unusedFor = 24 * time.Hour
	sweepPage = 500
)

// Sweeper deletes blobs nothing refers to: uploads never used, released,
// or taken by a send whose message was never delivered. Any number may
// run: a row is deleted by one of them, and only that one deletes the blob.
type Sweeper struct {
	db    *sql.DB
	blobs *blobs.Store
	log   *slog.Logger
}

func NewSweeper(db *sql.DB, blobs *blobs.Store, log *slog.Logger) *Sweeper {
	return &Sweeper{db: db, blobs: blobs, log: log}
}

// Run sweeps a minute after starting, then every ten minutes, until the
// context ends.
func (s *Sweeper) Run(ctx context.Context) error {
	timer := time.NewTimer(sweepFirst)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if n, err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("sweeping uploads failed", "err", err)
		} else if n > 0 {
			s.log.Info("swept unused uploads", "blobs", n)
		}
		timer.Reset(sweepInterval)
	}
}

type unused struct{ userID, blobID ids.UUID }

// Sweep deletes a page of unused uploads and one of pending ones, and
// returns how many blobs it deleted.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	cutoff := vtdb.Now().Add(-unusedFor)
	deleted := 0
	for _, query := range []string{
		`SELECT user_id, blob_id FROM upload WHERE used = FALSE AND created_at < ? LIMIT ?`,
		`SELECT user_id, blob_id FROM upload WHERE pending_since < ? LIMIT ?`,
	} {
		n, err := s.sweep(ctx, query, cutoff)
		deleted += n
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (s *Sweeper) sweep(ctx context.Context, query string, cutoff time.Time) (int, error) {
	rows, err := s.db.QueryContext(ctx, query, cutoff, sweepPage)
	if err != nil {
		return 0, err
	}
	page, err := vtdb.Collect(rows, func(rows *sql.Rows) (u unused, err error) { return u, rows.Scan(&u.userID, &u.blobID) })
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, u := range page {
		res, err := s.db.ExecContext(ctx, `
			DELETE FROM upload WHERE user_id = ? AND blob_id = ? AND (used = FALSE OR pending_since < ?)`,
			u.userID, u.blobID, cutoff)
		if err != nil {
			return deleted, err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			// Taken into use or delivered meanwhile, or another sweeper got it.
			continue
		}
		if err := s.blobs.Delete(ctx, u.blobID, vtdb.Now()); err != nil {
			// The row is gone; the blob stays behind unreferenced.
			s.log.Warn("deleting blob failed", "blob", u.blobID, "err", err)
			continue
		}
		deleted++
	}
	return deleted, nil
}
