package stickers

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Palette returns the user's saved stickers in their order.
func (s *Service) Palette(ctx context.Context, userID ids.UUID) ([]*pb.Sticker, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sticker_id FROM palette_sticker WHERE user_id = ? ORDER BY position`, userID)
	if err != nil {
		return nil, err
	}
	order, err := vtdb.Collect(rows, func(rows *sql.Rows) (id ids.UUID, err error) { return id, rows.Scan(&id) })
	if err != nil {
		return nil, err
	}
	return s.load(ctx, order)
}

// Save adds a sticker at the end of the palette; one already saved stays.
func (s *Service) Save(ctx context.Context, userID ids.UUID, sticker []byte) error {
	stickerID, err := parseID(sticker, "sticker_id")
	if err != nil {
		return err
	}
	known, err := s.load(ctx, []ids.UUID{stickerID})
	if err != nil {
		return err
	}
	if len(known) == 0 {
		return apierror.NotFound("no such sticker")
	}
	return s.change(ctx, userID, func(p *palette) (bool, error) {
		if slices.Contains(p.order, stickerID) {
			return false, nil
		}
		if len(p.order) >= maxPalette {
			return false, apierror.FailedPrecondition("the palette is full, at %d stickers", maxPalette)
		}
		position := 0
		if n := len(p.order); n > 0 {
			position = p.positions[n-1] + 1
		}
		_, err := p.tx.ExecContext(ctx, `
			INSERT INTO palette_sticker (user_id, sticker_id, position, saved_at) VALUES (?, ?, ?, ?)`,
			userID, stickerID, position, p.now)
		p.order = append(p.order, stickerID)
		return true, err
	})
}

// Remove takes a sticker out of the palette, if it is there.
func (s *Service) Remove(ctx context.Context, userID ids.UUID, sticker []byte) error {
	stickerID, err := parseID(sticker, "sticker_id")
	if err != nil {
		return err
	}
	return s.change(ctx, userID, func(p *palette) (bool, error) {
		i := slices.Index(p.order, stickerID)
		if i < 0 {
			return false, nil
		}
		_, err := p.tx.ExecContext(ctx, `
			DELETE FROM palette_sticker WHERE user_id = ? AND sticker_id = ?`, userID, stickerID)
		p.order = slices.Delete(p.order, i, i+1)
		return true, err
	})
}

// Move puts a saved sticker before another, or at the end, and numbers the
// palette from 0.
func (s *Service) Move(ctx context.Context, userID ids.UUID, sticker, before []byte) error {
	stickerID, err := parseID(sticker, "sticker_id")
	if err != nil {
		return err
	}
	var beforeID ids.UUID
	if len(before) > 0 {
		if beforeID, err = parseID(before, "before_sticker_id"); err != nil {
			return err
		}
	}
	return s.change(ctx, userID, func(p *palette) (bool, error) {
		i := slices.Index(p.order, stickerID)
		if i < 0 {
			return false, apierror.NotFound("the sticker is not in the palette")
		}
		if !beforeID.IsZero() && !slices.Contains(p.order, beforeID) {
			return false, apierror.NotFound("the sticker to move before is not in the palette")
		}
		if beforeID == stickerID {
			return false, nil
		}
		order := slices.Delete(slices.Clone(p.order), i, i+1)
		j := len(order)
		if !beforeID.IsZero() {
			j = slices.Index(order, beforeID)
		}
		order = slices.Insert(order, j, stickerID)
		if slices.Equal(order, p.order) {
			return false, nil
		}
		for k, id := range order {
			if p.positions[slices.Index(p.order, id)] == k {
				continue
			}
			if _, err := p.tx.ExecContext(ctx, `
				UPDATE palette_sticker SET position = ? WHERE user_id = ? AND sticker_id = ?`,
				k, userID, id); err != nil {
				return false, err
			}
		}
		p.order = order
		return true, nil
	})
}

// palette is a user's palette as read in a change's transaction.
// savedSticker is a palette_sticker row.
type savedSticker struct {
	id       ids.UUID
	position int
}

type palette struct {
	tx        *sql.Tx
	now       time.Time
	order     []ids.UUID
	positions []int
}

// change runs an edit of the palette in one transaction under the lock on
// the user row, and when the edit reports a change, writes a PaletteEvent
// of the whole palette to the user's outbox.
func (s *Service) change(ctx context.Context, userID ids.UUID, edit func(*palette) (bool, error)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var lastID uint64
	var deletedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT last_outbox_id, deleted_at FROM user WHERE user_id = ? FOR UPDATE`,
		userID).Scan(&lastID, &deletedAt); err != nil {
		return err
	}
	if deletedAt.Valid {
		return apierror.Unauthenticated("the account is deleted")
	}
	p := &palette{tx: tx, now: vtdb.Now()}
	rows, err := tx.QueryContext(ctx, `
		SELECT sticker_id, position FROM palette_sticker WHERE user_id = ? ORDER BY position`, userID)
	if err != nil {
		return err
	}
	saved, err := vtdb.Collect(rows, func(rows *sql.Rows) (e savedSticker, err error) { return e, rows.Scan(&e.id, &e.position) })
	if err != nil {
		return err
	}
	for _, e := range saved {
		p.order = append(p.order, e.id)
		p.positions = append(p.positions, e.position)
	}
	changed, err := edit(p)
	if err != nil || !changed {
		return err
	}
	stickerIDs := make([][]byte, len(p.order))
	for i, id := range p.order {
		stickerIDs[i] = id.Bytes()
	}
	payload, err := proto.Marshal(&pb.Event{
		OccurredAt: timestamppb.New(p.now),
		Body:       &pb.Event_Palette{Palette: &pb.PaletteEvent{StickerIds: stickerIDs}},
	})
	if err != nil {
		return err
	}
	lastID++
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_outbox (user_id, outbox_id, kind, payload, created_at) VALUES (?, ?, 'palette', ?, ?)`,
		userID, lastID, payload, p.now); err != nil {
		return err
	}
	// As with the other outboxes: the binlog keeps the insert.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM user_outbox WHERE user_id = ? AND outbox_id = ?`, userID, lastID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE user SET last_outbox_id = ? WHERE user_id = ?`, lastID, userID); err != nil {
		return err
	}
	return tx.Commit()
}
