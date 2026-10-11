package fanout

import (
	"context"
	"database/sql"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
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
// each conversation partner's, with the partner's list entry updated in
// the same transaction.
func (w *Worker) deliverProfile(ctx context.Context, o *userOutboxRow) error {
	user := o.event.GetProfile().GetUser()
	if user == nil {
		w.log.Error("skipping user outbox record without a profile", "kind", o.kind)
		return nil
	}
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
						UPDATE user_conversation SET peer_name = ?, peer_icon = ?
						WHERE user_id = ? AND conversation_id = ?`,
						user.GetName(), user.GetIcon(), p.peerID, p.conversationID)
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

// partners returns a page of the user's conversations after the given one,
// in id order.
func (w *Worker) partners(ctx context.Context, userID, after ids.UUID) ([]partner, error) {
	rows, err := w.db.QueryContext(ctx, `
		SELECT conversation_id, peer_user_id FROM user_conversation
		WHERE user_id = ? AND conversation_id > ? ORDER BY conversation_id LIMIT ?`,
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
