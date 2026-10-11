package conversation

import (
	"bytes"
	"testing"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
)

func TestMembers(t *testing.T) {
	a, b, c := ids.UUID{0x80}, ids.UUID{0x01}, ids.UUID{0x7f}
	encoded := encodeMembers([]ids.UUID{a, b, c})
	if want := append(append(b.Bytes(), c.Bytes()...), a.Bytes()...); !bytes.Equal(encoded, want) {
		t.Fatalf("encoded %x, want %x sorted unsigned", encoded, want)
	}
	members, err := decodeMembers(encoded)
	if err != nil {
		t.Fatal(err)
	}
	st := state{members: members}
	if !st.has(a) || !st.has(b) || !st.has(c) || st.has(ids.UUID{0x02}) {
		t.Fatalf("membership of %v", members)
	}
	if left := without(members, c); len(left) != 2 || left[0] != b || left[1] != a || len(members) != 3 {
		t.Fatalf("without: %v from %v", left, members)
	}
	if _, err := decodeMembers(make([]byte, 17)); err == nil {
		t.Fatal("decoded 17 bytes")
	}
}

func TestCheckTopic(t *testing.T) {
	for topic, ok := range map[string]bool{
		"Hikes": true, "": false, " a": false, "a ": false, "a\tb": false,
		string(bytes.Repeat([]byte("é"), 100)): true, string(bytes.Repeat([]byte("t"), 201)): false, "\xff": false,
	} {
		if got := CheckTopic(topic) == nil; got != ok {
			t.Errorf("CheckTopic(%q) ok = %v", topic, got)
		}
	}
}
