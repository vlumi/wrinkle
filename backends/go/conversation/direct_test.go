package conversation

import (
	"testing"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
)

func TestDirectMembers(t *testing.T) {
	var c directMembers
	conv, a, b := ids.New(), ids.New(), ids.New()
	if members, _ := c.get(conv); members != nil {
		t.Fatalf("unknown conversation has members %v", members)
	}
	c.add(conv, kindDirect, []ids.UUID{a, b})
	if members, direct := c.get(conv); len(members) != 2 || !direct {
		t.Fatalf("get = %v, %v", members, direct)
	}
	notes := ids.New()
	c.add(notes, kindNotes, []ids.UUID{a})
	if _, direct := c.get(notes); direct {
		t.Fatal("notes taken for a direct conversation")
	}
}
