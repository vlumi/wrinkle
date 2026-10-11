package fanout

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	backendv1 "github.com/vlumi/wrinkle/proto/go/wrinkle/backend/v1"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// deliverDevice appends a device change to the user's own log; a device
// signed out also has its connections closed, even when the log had the
// event already. A device with a key also goes, with only its id and key,
// to everyone who encrypts for it: the user's direct partners and the
// members of the user's groups.
func (w *Worker) deliverDevice(ctx context.Context, o *userOutboxRow) error {
	event := o.event
	d := event.GetDevice()
	if len(d.GetUserId()) == 0 {
		event = proto.Clone(event).(*pb.Event)
		event.GetDevice().UserId = o.userID.Bytes()
		d = event.GetDevice()
	}
	if _, err := w.append(ctx, o.userID, o.userID, o.outboxID, event, nil); err != nil {
		return fmt.Errorf("deliver to user %s: %w", o.userID, err)
	}
	if d.GetSignedOut() {
		w.publish(ctx, o.userID, &backendv1.Push{Kind: &backendv1.Push_SignedOut{
			SignedOut: &backendv1.SignedOut{DeviceIds: []uint64{d.GetDevice().GetDeviceId()}}}})
	}
	if len(d.GetDevice().GetPublicKey()) == 0 {
		return nil
	}
	keyOnly := &pb.Event{
		OccurredAt: event.GetOccurredAt(),
		Body: &pb.Event_Device{Device: &pb.DeviceEvent{
			UserId:    o.userID.Bytes(),
			Device:    &pb.Device{DeviceId: d.GetDevice().GetDeviceId(), PublicKey: d.GetDevice().GetPublicKey()},
			SignedOut: d.GetSignedOut(),
		}},
	}
	seen := map[ids.UUID]bool{o.userID: true}
	var after ids.UUID
	for {
		page, last, err := w.contacts(ctx, o.userID, after)
		if err != nil {
			return err
		}
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(profileConcurrency)
		for _, id := range page {
			if seen[id] {
				continue
			}
			seen[id] = true
			g.Go(func() error {
				if _, err := w.append(gctx, id, o.userID, o.outboxID, keyOnly, nil); err != nil {
					return fmt.Errorf("deliver to user %s: %w", id, err)
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return err
		}
		if last.IsZero() {
			return nil
		}
		after = last
	}
}

// contacts returns the users of a page of the user's conversations after
// the given one, in id order: direct partners, and the current members of
// groups the user is in. last is the page's last conversation, zero after
// the final page.
func (w *Worker) contacts(ctx context.Context, userID, after ids.UUID) (users []ids.UUID, last ids.UUID, err error) {
	rows, err := w.db.QueryContext(ctx, `
		SELECT conversation_id, peer_user_id FROM user_conversation
		WHERE user_id = ? AND conversation_id > ? AND (peer_user_id IS NOT NULL OR (kind = 'group' AND member))
		ORDER BY conversation_id LIMIT ?`,
		userID, after, profilePage)
	if err != nil {
		return nil, last, err
	}
	var groups []any
	n := 0
	for rows.Next() {
		var conversationID ids.UUID
		var peer []byte
		if err := rows.Scan(&conversationID, &peer); err != nil {
			rows.Close()
			return nil, last, err
		}
		n++
		last = conversationID
		if id, ok := ids.Parse(peer); ok {
			users = append(users, id)
		} else {
			groups = append(groups, conversationID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, last, err
	}
	if n < profilePage {
		last = ids.UUID{}
	}
	if len(groups) == 0 {
		return users, last, nil
	}
	rows, err = w.conversations.QueryContext(ctx, `
		SELECT members FROM conversation WHERE conversation_id IN (?`+strings.Repeat(", ?", len(groups)-1)+`)`, groups...)
	if err != nil {
		return nil, last, err
	}
	defer rows.Close()
	for rows.Next() {
		var members []byte
		if err := rows.Scan(&members); err != nil {
			return nil, last, err
		}
		for i := 0; i+16 <= len(members); i += 16 {
			id, _ := ids.Parse(members[i : i+16])
			users = append(users, id)
		}
	}
	return users, last, rows.Err()
}
