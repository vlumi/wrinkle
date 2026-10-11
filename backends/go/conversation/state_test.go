package conversation

import (
	"bytes"
	"testing"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
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

func TestMembershipChangeRotatesOnlyAfterTheFirstEpoch(t *testing.T) {
	st := state{}
	st.membershipChanged()
	if st.rotate {
		t.Fatal("rotate before any epoch")
	}
	st.keyEpoch = 1
	st.membershipChanged()
	if !st.rotate {
		t.Fatal("no rotate after epoch 1")
	}
}

func TestBlobIDs(t *testing.T) {
	a, b := ids.New(), ids.New()
	if list, err := parseBlobIDs([][]byte{a.Bytes(), b.Bytes()}); err != nil || len(list) != 2 || list[0] != a || list[1] != b {
		t.Fatalf("parse: %v %v", list, err)
	}
	for _, bad := range [][][]byte{{a.Bytes(), a.Bytes()}, {a.Bytes()[:15]}, make([][]byte, maxBlobIDs+1)} {
		if _, err := parseBlobIDs(bad); apierror.Code(err) != pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
			t.Fatalf("parse %x: %v", bad, err)
		}
	}
	stored := encodeBlobIDs([][]byte{a.Bytes(), b.Bytes()})
	if got := decodeBlobIDs(stored); len(got) != 2 || !bytes.Equal(got[0], a.Bytes()) || !bytes.Equal(got[1], b.Bytes()) {
		t.Fatalf("decode %x", got)
	}
	if got := decodeBlobIDs(nil); got != nil {
		t.Fatalf("decode empty: %x", got)
	}
}
