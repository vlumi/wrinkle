package devicesync

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	defaultSyncLimit = 100
	maxSyncLimit     = 1000
)

// Sync returns events after the cursor, oldest first.
func (s *Service) Sync(ctx context.Context, userID, cursor uint64, limit uint32) (*pb.SyncResponse, error) {
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
	for rows.Next() {
		if len(resp.Events) == int(limit) {
			resp.HasMore = true
			break
		}
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		event := &pb.Event{}
		if err := proto.Unmarshal(payload, event); err != nil {
			return nil, fmt.Errorf("unmarshal event for user %d: %w", userID, err)
		}
		resp.Events = append(resp.Events, event)
		resp.Cursor = event.GetCursor()
	}
	return resp, rows.Err()
}
