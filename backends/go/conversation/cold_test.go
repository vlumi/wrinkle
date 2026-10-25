package conversation

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/tsuna/gohbase/hrpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestColdKey(t *testing.T) {
	conv := ids.UUID{0xab, 15: 0x01}
	key := coldKey(conv, 0x0102)
	if want := append(conv.Bytes(), 0, 0, 0, 0, 0, 0, 1, 2); !bytes.Equal(key, want) {
		t.Fatalf("key %x, want %x", key, want)
	}
	// A conversation's rows sort by seq, and stay within its id.
	if bytes.Compare(coldKey(conv, 255), coldKey(conv, 256)) >= 0 {
		t.Fatal("seq 255 sorts after 256")
	}
	next := ids.UUID{0xab, 15: 0x02}
	if bytes.Compare(coldKey(conv, ^uint64(0)), coldKey(next, 0)) >= 0 {
		t.Fatal("a conversation's last row sorts after the next conversation's first")
	}
}

func TestColdRoundTrip(t *testing.T) {
	conv := ids.UUID{0x42}
	body, err := encodeBody(&pb.Message{Content: &pb.Message_Sealed{Sealed: &pb.Sealed{Epoch: 3, Nonce: []byte("n"), Ciphertext: []byte("c")}}})
	if err != nil {
		t.Fatal(err)
	}
	reaction := &pb.Reaction{UserId: ids.UUID{0x07}.Bytes(), Sealed: &pb.Sealed{Epoch: 3, Nonce: []byte("r")}, UpdatedAt: timestamppb.New(time.UnixMicro(5).UTC())}
	reactions, err := encodeReactions([]*pb.Reaction{reaction})
	if err != nil {
		t.Fatal(err)
	}
	m := coldMessage{
		seq: 9, sender: ids.UUID{0x01}, clientID: "c1", body: body,
		sentAt: time.UnixMicro(1_700_000_000_123_456).UTC(), outboxID: 77,
		blobIDs: slices.Repeat([]byte{0xbb}, 32), reactions: reactions,
	}
	var cells []*hrpc.Cell
	for q, v := range m.cells()[coldFamily] {
		cells = append(cells, &hrpc.Cell{Row: coldKey(conv, m.seq), Family: []byte(coldFamily), Qualifier: []byte(q), Value: v})
	}
	got, err := coldMessageOf(cells)
	if err != nil {
		t.Fatal(err)
	}
	if got.seq != m.seq || got.sender != m.sender || got.clientID != m.clientID || !bytes.Equal(got.body, m.body) ||
		!got.sentAt.Equal(m.sentAt) || got.outboxID != m.outboxID || !bytes.Equal(got.blobIDs, m.blobIDs) {
		t.Fatalf("read back %+v, wrote %+v", got, m)
	}
	msg, err := got.message(conv)
	if err != nil {
		t.Fatal(err)
	}
	if msg.GetSeq() != 9 || msg.GetSealed().GetEpoch() != 3 || len(msg.GetBlobIds()) != 2 ||
		len(msg.GetReactions()) != 1 || !proto.Equal(msg.GetReactions()[0], reaction) {
		t.Fatalf("message %v", msg)
	}

	// A deleted message keeps no reactions.
	deleted, err := encodeBody(&pb.Message{DeletedAt: timestamppb.Now()})
	if err != nil {
		t.Fatal(err)
	}
	got.body = deleted
	if msg, err := got.message(conv); err != nil || msg.GetDeletedAt() == nil || len(msg.GetReactions()) != 0 {
		t.Fatalf("deleted message %v %v", msg, err)
	}
	if b, err := encodeReactions(nil); err != nil || len(b) != 0 {
		t.Fatalf("no reactions encode to %x %v", b, err)
	}
}

func TestColdRange(t *testing.T) {
	page := func(seqs ...uint64) []*pb.Message {
		var p []*pb.Message
		for _, s := range seqs {
			p = append(p, &pb.Message{Seq: s})
		}
		return p
	}
	const all = ^uint64(0)
	for _, tc := range []struct {
		name                       string
		page                       []*pb.Message
		joined, upTo, before, cold uint64
		limit                      uint32
		hi                         uint64
		more, again                bool
	}{
		{"all hot, page full", page(12, 11), 1, all, all, 10, 2, 10, false, false},
		{"goes on below the lowest", page(12, 11), 1, all, all, 10, 5, 10, true, false},
		{"none in MySQL", nil, 1, all, all, 10, 5, 10, true, false},
		{"before the watermark", nil, 1, all, 4, 10, 5, 3, true, false},
		{"until a member left", nil, 1, 6, all, 10, 5, 6, true, false},
		{"nothing cold", page(3, 2, 1), 1, all, all, 0, 5, 0, false, false},
		{"joined after the watermark", page(12), 11, all, all, 10, 5, 10, false, false},
		{"before the first", nil, 1, all, 1, 10, 5, 0, false, false},
		{"moved meanwhile", page(12, 11, 10), 1, all, all, 10, 5, 0, false, true},
	} {
		hi, more, again := coldRange(tc.page, tc.joined, tc.upTo, tc.before, tc.cold, tc.limit)
		if more != tc.more || again != tc.again || (more && hi != tc.hi) {
			t.Errorf("%s: hi %d more %v again %v, want %d %v %v", tc.name, hi, more, again, tc.hi, tc.more, tc.again)
		}
	}
}
