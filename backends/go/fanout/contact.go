package fanout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vlumi/wrinkle/backends/go/contacts"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// deliverContact handles a change of a user's side toward another: the
// side is copied to the other's contact_peer, the other's request ends
// when the side ended or became a block, through the other's outbox, and
// both users get how they now stand, each view read under the lock on the
// log it goes to, so the last event in a log is the current state. Every
// step may run again on redelivery.
func (w *Worker) deliverContact(ctx context.Context, o *userOutboxRow) error {
	c := o.event.GetContact().GetContact()
	otherID, ok := ids.Parse(c.GetUser().GetUserId())
	if !ok || otherID == o.userID {
		w.log.Error("skipping contact change without another user", "user", o.userID, "outbox", o.outboxID)
		return nil
	}
	user, err := w.profile(ctx, o.userID)
	if err != nil {
		return err
	}
	other, err := w.profile(ctx, otherID)
	if err != nil {
		return err
	}
	if other.GetName() != "" && !other.GetDeleted() {
		side := contacts.PayloadSide(c.GetState())
		current, err := contacts.CopyPeer(ctx, w.db, otherID, o.userID, side, o.outboxID, o.event.GetOccurredAt().AsTime())
		if err != nil {
			return fmt.Errorf("copy contact of user %s to %s: %w", o.userID, otherID, err)
		}
		// Only the latest side ends a request: a redelivered older row must
		// not end one made after it.
		if current && side != contacts.Request {
			if err := contacts.EndRequest(ctx, w.db, otherID, o.userID); err != nil {
				return fmt.Errorf("end request of user %s: %w", otherID, err)
			}
		}
	}
	for _, to := range []struct {
		userID, otherID ids.UUID
		other           *pb.User
	}{{o.userID, otherID, other}, {otherID, o.userID, user}} {
		build := func(ctx context.Context, tx *sql.Tx, event *pb.Event) error {
			own, peer, err := contacts.Sides(ctx, tx, to.userID, to.otherID)
			if err != nil {
				return err
			}
			event.Body = &pb.Event_Contact{Contact: &pb.ContactEvent{Contact: contacts.Contact(to.other, own, peer)}}
			return nil
		}
		if _, err := w.appendBuilt(ctx, to.userID, o.userID, o.outboxID, o.event, build, nil); err != nil {
			return fmt.Errorf("deliver to user %s: %w", to.userID, err)
		}
	}
	return nil
}

// profile reads a user's current profile; one that does not exist has
// only its id.
func (w *Worker) profile(ctx context.Context, userID ids.UUID) (*pb.User, error) {
	user := &pb.User{UserId: userID.Bytes()}
	var deletedAt sql.NullTime
	err := w.db.QueryRowContext(ctx, `SELECT name, icon, avatar, deleted_at FROM user WHERE user_id = ?`, userID).
		Scan(&user.Name, &user.Icon, &user.Avatar, &deletedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	user.Deleted = deletedAt.Valid
	return user, nil
}
