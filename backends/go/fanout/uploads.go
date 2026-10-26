package fanout

import (
	"context"
	"fmt"
	"strings"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
)

// settleUploads ends the wait of a delivered message's attachments, which
// the sweeper would otherwise take for a failed send's, and releases a
// deleted message's, for the sweeper to delete. Both are idempotent, so a
// record delivered again changes nothing.
func (w *Worker) settleUploads(ctx context.Context, o *outboxRow) error {
	if msg := o.event.GetMessage().GetMessage(); len(msg.GetBlobIds()) > 0 {
		sender, ok := ids.Parse(msg.GetSenderUserId())
		if !ok {
			return nil
		}
		blobIDs := make([]ids.UUID, 0, len(msg.GetBlobIds()))
		for _, b := range msg.GetBlobIds() {
			if id, ok := ids.Parse(b); ok {
				blobIDs = append(blobIDs, id)
			}
		}
		return w.updateUploads(ctx, `UPDATE upload SET pending_since = NULL`, sender, blobIDs)
	}
	if len(o.releasedUploads) > 0 {
		return w.updateUploads(ctx, `UPDATE upload SET used = FALSE, pending_since = NULL`, o.releasedBy, o.releasedUploads)
	}
	return nil
}

func (w *Worker) updateUploads(ctx context.Context, update string, userID ids.UUID, blobIDs []ids.UUID) error {
	if len(blobIDs) == 0 {
		return nil
	}
	args := []any{userID}
	for _, id := range blobIDs {
		args = append(args, id)
	}
	_, err := w.db.ExecContext(ctx, update+` WHERE user_id = ? AND purpose = 'attachment' AND blob_id IN (?`+strings.Repeat(`, ?`, len(blobIDs)-1)+`)`, args...)
	if err != nil {
		return fmt.Errorf("uploads of user %s: %w", userID, err)
	}
	return nil
}
