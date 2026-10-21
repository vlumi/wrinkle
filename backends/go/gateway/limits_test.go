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

// scheduled records the waits a cursor asks for, without starting timers.
type scheduled []time.Duration

func (s *scheduled) schedule(wait time.Duration) *time.Timer {
	*s = append(*s, wait)
	return time.NewTimer(time.Hour)
}

func TestAckCursorCoalesces(t *testing.T) {
	var a ackCursor
	var s scheduled
	now := time.Now()
	if !a.offer(5) {
		t.Fatal("first ack did not start a writer")
	}
	if a.offer(9) || a.offer(7) {
		t.Fatal("ack started a second writer")
	}
	if c := a.next(now, 0, s.schedule); c != 9 {
		t.Fatalf("next = %d, want 9", c)
	}
	if a.offer(3) {
		t.Fatal("ack started a second writer while writing")
	}
	if c := a.next(now, 0, s.schedule); c != 3 {
		t.Fatalf("next = %d, want 3", c)
	}
	if c := a.next(now, 0, s.schedule); c != 0 {
		t.Fatalf("writer kept going with nothing pending: %d", c)
	}
	if !a.offer(4) {
		t.Fatal("ack after the writer stopped did not start one")
	}
	if len(s) != 0 {
		t.Fatalf("no interval, yet waits %v", s)
	}
}

func TestAckCursorWaitsForTheInterval(t *testing.T) {
	var a ackCursor
	var s scheduled
	now := time.Now()
	a.offer(1)
	if c := a.next(now, time.Minute, s.schedule); c != 1 {
		t.Fatalf("first ack not written at once: %d", c)
	}
	if c := a.next(now, time.Minute, s.schedule); c != 0 {
		t.Fatalf("writer kept going with nothing pending: %d", c)
	}
	if !a.offer(2) {
		t.Fatal("ack after the writer stopped did not start one")
	}
	if a.offer(4) || a.offer(3) {
		t.Fatal("ack started a second writer")
	}
	// Twenty seconds after the first write, the writer waits forty more.
	if c := a.next(now.Add(20*time.Second), time.Minute, s.schedule); c != 0 {
		t.Fatalf("written within the interval: %d", c)
	}
	if len(s) != 1 || s[0] != 40*time.Second {
		t.Fatalf("waits %v, want [40s]", s)
	}
	if a.offer(5) {
		t.Fatal("ack started a writer while one is scheduled")
	}
	if c := a.next(now.Add(time.Minute), time.Minute, s.schedule); c != 5 {
		t.Fatalf("scheduled write = %d, want 5", c)
	}
}

func TestAckCursorWritesAtOnceOnClose(t *testing.T) {
	var a ackCursor
	var s scheduled
	now := time.Now()
	a.offer(1)
	a.next(now, time.Minute, s.schedule)
	a.offer(2)
	// Still writing the first: the running writer takes the rest.
	if a.close() {
		t.Fatal("close started a second writer")
	}
	if c := a.next(now.Add(time.Second), time.Minute, s.schedule); c != 2 {
		t.Fatalf("after close = %d, want 2 at once", c)
	}

	// Waiting on its timer: close stops it and starts a writer.
	var b ackCursor
	b.offer(1)
	b.next(now, time.Minute, s.schedule)
	b.next(now, time.Minute, s.schedule)
	b.offer(3)
	if c := b.next(now.Add(time.Second), time.Minute, s.schedule); c != 0 {
		t.Fatalf("written within the interval: %d", c)
	}
	if !b.close() {
		t.Fatal("close did not start a writer in place of the timer")
	}
	if c := b.next(now.Add(2*time.Second), time.Minute, s.schedule); c != 3 {
		t.Fatalf("after close = %d, want 3", c)
	}

	// Nothing pending: nothing to write.
	var c ackCursor
	if c.close() {
		t.Fatal("close with nothing pending started a writer")
	}
}
