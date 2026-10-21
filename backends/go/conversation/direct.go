package conversation

import (
	"sync"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
)

// maxDirect bounds the cache; it starts over when full.
const maxDirect = 1 << 20

// directMembers remembers the members of direct conversations and notes,
// which never change, so a send's block check needs no read of the row.
type directMembers struct {
	mu      sync.Mutex
	entries map[ids.UUID]directEntry
}

type directEntry struct {
	members []ids.UUID
	direct  bool // a direct conversation rather than notes
}

// get returns the conversation's members and whether it is a direct
// conversation, or no members when it is not remembered.
func (c *directMembers) get(conversationID ids.UUID) ([]ids.UUID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[conversationID]
	return e.members, e.direct
}

func (c *directMembers) add(conversationID ids.UUID, kind string, members []ids.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= maxDirect {
		c.entries = map[ids.UUID]directEntry{}
	}
	c.entries[conversationID] = directEntry{members: members, direct: kind == kindDirect}
}
