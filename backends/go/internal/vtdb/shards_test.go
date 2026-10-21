package vtdb

import (
	"encoding/hex"
	"testing"
)

func TestKeyspaceIDMatchesVitess(t *testing.T) {
	// Pairs read from vtgate's xxhash vindex function.
	for id, want := range map[string]string{
		"004eb6acc9704010b1387b332394a2db": "ac758b75e4c3665c",
		"005dff9967944895b74f28fa37d8661a": "cd0363ddcc380d5b",
	} {
		b, _ := hex.DecodeString(id)
		if got := hex.EncodeToString(KeyspaceID(b)); got != want {
			t.Errorf("KeyspaceID(%s) = %s, want %s", id, got, want)
		}
	}
}

func TestShardsOf(t *testing.T) {
	s := Shards{names: []string{"-80", "80-"}, starts: [][]byte{{}, {0x80}}}
	id, _ := hex.DecodeString("004eb6acc9704010b1387b332394a2db") // keyspace id ac...
	if got := s.Of(id); got != "80-" {
		t.Errorf("Of = %q, want 80-", got)
	}
	if got := (Shards{}).Of(id); got != "" {
		t.Errorf("unsharded Of = %q, want one shard", got)
	}
}
