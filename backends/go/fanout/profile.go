package fanout

import (
	"context"
	"database/sql"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	backendv1 "github.com/vlumi/wrinkle/proto/go/wrinkle/backend/v1"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	// profilePage is how many conversation partners are read at a time.
	profilePage = 500
	// profileConcurrency is how many partners of one user are delivered to
	// at a time.
	profileConcurrency = 16
)

// deliverProfile appends a profile change to the user's own log, then to
// each direct conversation partner's, with the partner's list entry
// updated in the same transaction. A deleted user's log gets nothing.
func (w *Worker) deliverProfile(ctx context.Context, o *userOutboxRow) error {
	user := o.event.GetProfile().GetUser()
	if _, err := w.append(ctx, o.userID, o.userID, o.outboxID, o.event, nil); err != nil {
		return fmt.Errorf("deliver to user %s: %w", o.userID, err)
	}
	var after ids.UUID
	for {
		page, err := w.partners(ctx, o.userID, after)
		if err != nil {
			return err
		}
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(profileConcurrency)
		for _, p := range page {
			g.Go(func() error {
				update := func(ctx context.Context, tx *sql.Tx, _ *pb.Event) error {
					_, err := tx.ExecContext(ctx, `
						UPDATE user_conversation SET peer_name = ?, peer_icon = ?, peer_avatar = ?, peer_deleted = ?
						WHERE user_id = ? AND conversation_id = ?`,
						user.GetName(), user.GetIcon(), nullBytes(user.GetAvatar()), user.GetDeleted(), p.peerID, p.conversationID)
					return err
				}
				if _, err := w.append(gctx, p.peerID, o.userID, o.outboxID, o.event, update); err != nil {
					return fmt.Errorf("deliver to user %s: %w", p.peerID, err)
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return err
		}
		if len(page) < profilePage {
			return nil
		}
		after = page[len(page)-1].conversationID
	}
}

type partner struct{ conversationID, peerID ids.UUID }

// partners returns a page of the user's direct conversations after the
// given one, in id order.
func (w *Worker) partners(ctx context.Context, userID, after ids.UUID) ([]partner, error) {
	rows, err := w.db.QueryContext(ctx, `
		SELECT conversation_id, peer_user_id FROM user_conversation
		WHERE user_id = ? AND conversation_id > ? AND peer_user_id IS NOT NULL
		ORDER BY conversation_id LIMIT ?`,
		userID, after, profilePage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var page []partner
	for rows.Next() {
		var p partner
		if err := rows.Scan(&p.conversationID, &p.peerID); err != nil {
			return nil, err
		}
		page = append(page, p)
	}
	return page, rows.Err()
}

// purgePage is how many rows one statement of a deleted user's purge
// removes.
const purgePage = 1000

// deliverDeletion tells the partners of a deleted user as of a profile
// change, closes the user's connections and removes the user's log,
// conversation list and devices; the uploads go back to unused, for the
// sweeper. The user row stays as a tombstone. Each step may run again on
// redelivery.
func (w *Worker) deliverDeletion(ctx context.Context, o *userOutboxRow) error {
	if err := w.deliverProfile(ctx, o); err != nil {
		return err
	}
	w.publish(ctx, o.userID, &backendv1.Push{Kind: &backendv1.Push_SignedOut{SignedOut: &backendv1.SignedOut{AllDevices: true}}})
	for _, table := range []string{"user_event", "user_conversation", "device"} {
		for {
			res, err := w.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE user_id = ? LIMIT ?`, o.userID, purgePage)
			if err != nil {
				return fmt.Errorf("purge %s of user %s: %w", table, o.userID, err)
			}
			if n, err := res.RowsAffected(); err != nil || n < purgePage {
				break
			}
		}
	}
	if _, err := w.db.ExecContext(ctx, `UPDATE upload SET used = FALSE WHERE user_id = ?`, o.userID); err != nil {
		return fmt.Errorf("release uploads of user %s: %w", o.userID, err)
	}
	return nil
}
