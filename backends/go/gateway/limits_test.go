package gateway

import (
	"testing"
	"time"
)

func TestTokenBucket(t *testing.T) {
	start := time.Now()
	b := newTokenBucket(start)
	for i := range bucketCapacity {
		if !b.take(start) {
			t.Fatalf("token %d refused within the burst", i)
		}
	}
	if b.take(start) {
		t.Fatal("token given beyond the burst")
	}
	// 100 ms refills 5 tokens.
	later := start.Add(100 * time.Millisecond)
	for i := range 5 {
		if !b.take(later) {
			t.Fatalf("refilled token %d refused", i)
		}
	}
	if b.take(later) {
		t.Fatal("token given beyond the refill")
	}
	// A long pause refills up to the capacity, no more.
	much := later.Add(time.Hour)
	n := 0
	for b.take(much) {
		n++
	}
	if n != bucketCapacity {
		t.Fatalf("got %d tokens after a pause, want %d", n, bucketCapacity)
	}
}

func TestAckCursorCoalesces(t *testing.T) {
	var a ackCursor
	if !a.offer(5) {
		t.Fatal("first ack did not start a writer")
	}
	if a.offer(9) || a.offer(7) {
		t.Fatal("ack started a second writer")
	}
	if c, ok := a.next(); !ok || c != 9 {
		t.Fatalf("next = %d, %v; want 9, true", c, ok)
	}
	if a.offer(3) {
		t.Fatal("ack started a second writer while writing")
	}
	if c, ok := a.next(); !ok || c != 3 {
		t.Fatalf("next = %d, %v; want 3, true", c, ok)
	}
	if _, ok := a.next(); ok {
		t.Fatal("writer kept going with nothing pending")
	}
	if !a.offer(4) {
		t.Fatal("ack after the writer stopped did not start one")
	}
}
