package presence

import (
	"testing"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
)

func TestKeysUseLowercaseHex(t *testing.T) {
	u := ids.UUID{0xab, 0x01, 15: 0xff}
	if got := channel(u); got != "push:ab0100000000000000000000000000ff" {
		t.Fatalf("channel %q", got)
	}
	if got := presenceKey(u); got != "presence:ab0100000000000000000000000000ff" {
		t.Fatalf("presence key %q", got)
	}
	if got, ok := userOf(channel(u)); !ok || got != u {
		t.Fatalf("user of the channel: %s %v", got, ok)
	}
	for _, c := range []string{"push:AB0100000000000000000000000000FF", "push:ab01", "other:ab0100000000000000000000000000ff"} {
		if _, ok := userOf(c); ok {
			t.Fatalf("parsed %q", c)
		}
	}
}
