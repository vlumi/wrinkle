package fanout

import (
	"sync"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
)

// maxListed bounds the cache; it starts over when full.
const maxListed = 1 << 20

type listKey struct{ userID, conversationID ids.UUID }

// listedCache remembers conversation list entries known to exist. Entries
// are never removed, so a remembered one stays true.
type listedCache struct {
	mu      sync.Mutex
	entries map[listKey]struct{}
}

func (c *listedCache) has(userID, conversationID ids.UUID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[listKey{userID, conversationID}]
	return ok
}

func (c *listedCache) add(userID, conversationID ids.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= maxListed {
		c.entries = map[listKey]struct{}{}
	}
	c.entries[listKey{userID, conversationID}] = struct{}{}
}
