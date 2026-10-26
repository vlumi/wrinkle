package devicesync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/page"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	defaultSyncLimit = 100
	maxSyncLimit     = 1000
)

// Sync returns events after the cursor, oldest first, or fails when the
// retention dropped some of them.
func (s *Service) Sync(ctx context.Context, userID ids.UUID, cursor uint64, limit uint32) (*pb.SyncResponse, error) {
	if limit == 0 {
		limit = defaultSyncLimit
	}
	limit = min(limit, maxSyncLimit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT payload FROM user_event WHERE user_id = ? AND position > ?
		ORDER BY position LIMIT ?`,
		userID, cursor, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &pb.SyncResponse{Cursor: cursor}
	size := 0
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		if len(resp.Events) == int(limit) || (len(resp.Events) > 0 && size+len(payload) > page.MaxBytes) {
			resp.HasMore = true
			break
		}
		size += len(payload)
		event := &pb.Event{}
		if err := proto.Unmarshal(payload, event); err != nil {
			return nil, fmt.Errorf("unmarshal event for user %s: %w", userID, err)
		}
		resp.Events = append(resp.Events, event)
		resp.Cursor = event.GetCursor()
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// After the events: the trimmer raises the tail in the transaction that
	// drops them, so events read before the drop are all there, and a drop
	// that came first shows here.
	var tail uint64
	if err := s.db.QueryRowContext(ctx, `SELECT log_tail FROM user WHERE user_id = ?`, userID).Scan(&tail); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if cursor < tail {
		return nil, apierror.CursorExpired("cursor %d is before the log's oldest kept event, after %d", cursor, tail)
	}
	return resp, nil
}
