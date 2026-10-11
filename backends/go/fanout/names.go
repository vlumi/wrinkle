package fanout

import (
	"context"
	"fmt"
	"sync"
)

// nameCache holds user names, which never change once a user exists.
type nameCache struct {
	mu    sync.Mutex
	names map[uint64]string
}

func (c *nameCache) get(userID uint64) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	name, ok := c.names[userID]
	return name, ok
}

func (c *nameCache) put(userID uint64, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.names == nil {
		c.names = map[uint64]string{}
	}
	c.names[userID] = name
}

func (w *Worker) peerName(ctx context.Context, userID uint64) (string, error) {
	if name, ok := w.names.get(userID); ok {
		return name, nil
	}
	var name string
	if err := w.db.QueryRowContext(ctx, `SELECT name FROM user WHERE user_id = ?`, userID).Scan(&name); err != nil {
		return "", fmt.Errorf("name of user %d: %w", userID, err)
	}
	w.names.put(userID, name)
	return name, nil
}
