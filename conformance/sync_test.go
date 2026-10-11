package conformance

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestSyncReturnsWhatPushDelivered(t *testing.T) {
	a, b, conv := pair(t)
	start := b.cursor
	var pushed []*pb.Event
	sendMessages(t, a, conv, 3, "x")
	for i := 1; i <= 3; i++ {
		pushed = append(pushed, nextEvent(t, b, isMessage))
	}
	resp, err := b.Sync(ctx(t), start, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetEvents()) != 3 || resp.GetHasMore() {
		t.Fatalf("expected 3 events and no more, got %d, has_more=%v", len(resp.GetEvents()), resp.GetHasMore())
	}
	for i, ev := range resp.GetEvents() {
		if !proto.Equal(ev, pushed[i]) {
			t.Fatalf("synced event %d differs from the pushed one:\n%v\n%v", i, ev, pushed[i])
		}
	}
	if resp.GetCursor() != pushed[2].GetCursor() {
		t.Fatalf("sync cursor %d, want %d", resp.GetCursor(), pushed[2].GetCursor())
	}
	empty, err := b.Sync(ctx(t), resp.GetCursor(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.GetEvents()) != 0 || empty.GetCursor() != resp.GetCursor() || empty.GetHasMore() {
		t.Fatalf("sync at the head should be empty: %v", empty)
	}
}

func TestSyncPaginates(t *testing.T) {
	a, b, conv := pair(t)
	start := b.cursor
	sendMessages(t, a, conv, 5, "x")
	for range 5 {
		nextEvent(t, b, isMessage)
	}
	var seen []uint64
	cursor := start
	for pages := 0; ; pages++ {
		resp, err := b.Sync(ctx(t), cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.GetEvents()) > 2 {
			t.Fatalf("page has %d events, limit was 2", len(resp.GetEvents()))
		}
		for _, ev := range resp.GetEvents() {
			if ev.GetCursor() <= cursor {
				t.Fatalf("event cursor %d not after %d", ev.GetCursor(), cursor)
			}
			seen = append(seen, ev.GetMessage().GetMessage().GetSeq())
		}
		cursor = resp.GetCursor()
		if !resp.GetHasMore() {
			if pages != 2 {
				t.Fatalf("expected 3 pages, got %d", pages+1)
			}
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("paged through %v, want 5 messages", seen)
	}
	for i, seq := range seen {
		if seq != uint64(i+1) {
			t.Fatalf("messages out of order: %v", seen)
		}
	}
}

func TestSyncFillsGapAfterReconnect(t *testing.T) {
	a, b, conv := pair(t)
	cursor := b.cursor
	b.Close()
	sendMessages(t, a, conv, 3, "while away")
	messageEvent(t, a, conv, 3)
	back := connectDevice(t, b.user.GetName(), b.deviceID)
	resp, err := back.Sync(ctx(t), cursor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetEvents()) != 3 {
		t.Fatalf("expected 3 missed events, got %d", len(resp.GetEvents()))
	}
	for i, ev := range resp.GetEvents() {
		if ev.GetMessage().GetMessage().GetSeq() != uint64(i+1) {
			t.Fatalf("missed events out of order: %v", resp.GetEvents())
		}
	}
}
