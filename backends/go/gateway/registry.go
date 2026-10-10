package gateway

import (
	"context"
	"sync"

	"github.com/vlumi/wrinkle/backends/go/presence"
)

// registry tracks the connections of each user on this node and keeps the
// push subscription in step: one channel per user with a connection.
type registry struct {
	sub *presence.Subscription

	mu     sync.Mutex
	byUser map[uint64]map[*conn]struct{}
}

func newRegistry(sub *presence.Subscription) *registry {
	return &registry{sub: sub, byUser: map[uint64]map[*conn]struct{}{}}
}

func (r *registry) add(ctx context.Context, c *conn) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	conns, ok := r.byUser[c.userID]
	if !ok {
		conns = map[*conn]struct{}{}
		r.byUser[c.userID] = conns
	}
	conns[c] = struct{}{}
	if len(conns) == 1 {
		return r.sub.Add(ctx, c.userID)
	}
	return nil
}

func (r *registry) remove(ctx context.Context, c *conn) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	conns, ok := r.byUser[c.userID]
	if !ok {
		return nil
	}
	delete(conns, c)
	if len(conns) == 0 {
		delete(r.byUser, c.userID)
		return r.sub.Remove(ctx, c.userID)
	}
	return nil
}

func (r *registry) conns(userID uint64) []*conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := make([]*conn, 0, len(r.byUser[userID]))
	for c := range r.byUser[userID] {
		list = append(list, c)
	}
	return list
}
