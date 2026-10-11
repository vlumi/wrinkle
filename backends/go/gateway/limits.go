package gateway

import (
	"sync"
	"time"
)

const (
	bucketCapacity = 100
	bucketRefill   = 50 // tokens per second
	maxInFlight    = 32
)

// tokenBucket limits the frames a connection sends. Only the read loop
// uses it, so it needs no lock.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newTokenBucket(now time.Time) tokenBucket {
	return tokenBucket{tokens: bucketCapacity, last: now}
}

// take reports whether a token was available at now, consuming it.
func (b *tokenBucket) take(now time.Time) bool {
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(bucketCapacity, b.tokens+elapsed.Seconds()*bucketRefill)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ackCursor coalesces a connection's acks: one write in flight at a time,
// and acks arriving meanwhile replace the pending cursor with the highest.
type ackCursor struct {
	mu      sync.Mutex
	pending uint64
	writing bool
}

// offer records a cursor and reports whether the caller should start
// writing; otherwise the running writer picks it up.
func (a *ackCursor) offer(cursor uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending = max(a.pending, cursor)
	if a.writing {
		return false
	}
	a.writing = true
	return true
}

// next hands the writer the pending cursor, or reports that there is none
// and the writer should stop.
func (a *ackCursor) next() (uint64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cursor := a.pending
	a.pending = 0
	if cursor == 0 {
		a.writing = false
		return 0, false
	}
	return cursor, true
}
