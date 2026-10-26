package uploads

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/vlumi/wrinkle/backends/go/internal/blobs"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
)

const sweepPage = 500

// SweeperConfig sets how long a slot may go unused, or an attachment wait
// for its message's delivery, before its blob is deleted, and how often
// the sweeper looks; HotWindow is how long messages stay in MySQL, where
// the sweeper finds a pending attachment's message.
type SweeperConfig struct {
	UnusedFor, Interval, HotWindow time.Duration
}

// Sweeper deletes blobs nothing refers to: uploads never used, released,
// or taken by a send that failed. Any number may run: a row is deleted by
// one of them, and only that one deletes the blob.
type Sweeper struct {
	db, conversations *sql.DB
	blobs             *blobs.Store
	cfg               SweeperConfig
	log               *slog.Logger
}

func NewSweeper(db, conversations *sql.DB, blobs *blobs.Store, cfg SweeperConfig, log *slog.Logger) *Sweeper {
	return &Sweeper{db: db, conversations: conversations, blobs: blobs, cfg: cfg, log: log}
}

// Run sweeps a minute after starting, or the interval when shorter, then
// every interval, until the context ends.
func (s *Sweeper) Run(ctx context.Context) error {
	timer := time.NewTimer(min(time.Minute, s.cfg.Interval))
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
		timer.Reset(s.cfg.Interval)
	}
}

type pending struct {
	userID, blobID ids.UUID
	conversationID []byte
	clientID       sql.NullString
	since          time.Time
}

// Sweep deletes a page of unused uploads, settles a page of pending ones,
// deleting those of failed sends, and returns how many blobs it deleted.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	now := vtdb.Now()
	cutoff := now.Add(-s.cfg.UnusedFor)
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, blob_id FROM upload WHERE used = FALSE AND created_at < ? LIMIT ?`, cutoff, sweepPage)
	if err != nil {
		return 0, err
	}
	unused, err := vtdb.Collect(rows, func(rows *sql.Rows) (p pending, err error) { return p, rows.Scan(&p.userID, &p.blobID) })
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, u := range unused {
		ok, err := s.remove(ctx, u, `used = FALSE`)
		if err != nil {
			return deleted, err
		}
		if ok {
			deleted++
		}
	}
	rows, err = s.db.QueryContext(ctx, `
		SELECT user_id, blob_id, conversation_id, client_message_id, pending_since FROM upload
		WHERE pending_since < ? LIMIT ?`, cutoff, sweepPage)
	if err != nil {
		return deleted, err
	}
	waiting, err := vtdb.Collect(rows, func(rows *sql.Rows) (p pending, err error) {
		return p, rows.Scan(&p.userID, &p.blobID, &p.conversationID, &p.clientID, &p.since)
	})
	if err != nil {
		return deleted, err
	}
	for _, p := range waiting {
		sent, err := s.sent(ctx, p)
		if err != nil {
			return deleted, err
		}
		switch pendingVerdict(sent, p.since, now, s.cfg.HotWindow) {
		case keep:
			if _, err := s.db.ExecContext(ctx, `
				UPDATE upload SET pending_since = NULL WHERE user_id = ? AND blob_id = ? AND pending_since = ?`,
				p.userID, p.blobID, p.since); err != nil {
				return deleted, err
			}
		case drop:
			ok, err := s.remove(ctx, p, `pending_since = ?`, p.since)
			if err != nil {
				return deleted, err
			}
			if ok {
				deleted++
			}
		}
	}
	return deleted, nil
}

type verdict int

const (
	keep verdict = iota
	drop
)

// pendingVerdict decides an attachment upload still pending after the
// cutoff. Its message, if the send stored one, was sent after the upload
// was taken, so while that is well within the hot window the message
// would be in MySQL: not finding it there means the send failed. Older,
// the message may have moved to the cold tier, so the upload is kept.
func pendingVerdict(sent bool, since, now time.Time, hot time.Duration) verdict {
	if !sent && since.After(now.Add(-hot/2)) {
		return drop
	}
	return keep
}

// sent tells whether the conversation keyspace has the message the upload
// was taken for. Uploads taken before the message was recorded on them
// count as sent.
func (s *Sweeper) sent(ctx context.Context, p pending) (bool, error) {
	conversationID, ok := ids.Parse(p.conversationID)
	if !ok || !p.clientID.Valid {
		return true, nil
	}
	var one int
	err := s.conversations.QueryRowContext(ctx, `
		SELECT 1 FROM message WHERE conversation_id = ? AND sender_user_id = ? AND client_message_id = ?`,
		conversationID, p.userID, p.clientID.String).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// remove deletes the upload's row while the condition still holds, then
// its blob, and reports whether it deleted the blob.
func (s *Sweeper) remove(ctx context.Context, p pending, condition string, args ...any) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM upload WHERE user_id = ? AND blob_id = ? AND `+condition,
		append([]any{p.userID, p.blobID}, args...)...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		// Taken into use or delivered meanwhile, or another sweeper got it.
		return false, nil
	}
	if err := s.blobs.Delete(ctx, p.blobID, vtdb.Now()); err != nil {
		// The row is gone; the blob stays behind unreferenced.
		s.log.Warn("deleting blob failed", "blob", p.blobID, "err", err)
		return false, nil
	}
	return true, nil
}
