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

// ackCursor coalesces a connection's acks: one write at a time, at most one
// per interval, the highest cursor pending, and whatever is pending written
// at once when the connection ends.
type ackCursor struct {
	mu      sync.Mutex
	pending uint64
	started time.Time // when the last write started
	running bool      // a writer runs, or its timer is set
	closed  bool
	timer   *time.Timer
}

// offer records a cursor and reports whether the caller should start a
// writer; otherwise the running or scheduled one picks it up.
func (a *ackCursor) offer(cursor uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending = max(a.pending, cursor)
	if a.running {
		return false
	}
	a.running = true
	return true
}

// close marks the connection ended, so what is pending is written without
// waiting, and reports whether the caller should start a writer to do it.
func (a *ackCursor) close() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	if a.timer != nil && a.timer.Stop() {
		a.timer = nil
		return true
	}
	if a.running || a.pending == 0 {
		return false
	}
	a.running = true
	return true
}

// next hands the writer the cursor to write now, or 0 when it should stop:
// nothing is pending, or the interval since the last write has not passed,
// in which case schedule is called to start a writer when it has.
func (a *ackCursor) next(now time.Time, interval time.Duration, schedule func(time.Duration) *time.Timer) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.timer = nil
	if a.pending == 0 {
		a.running = false
		return 0
	}
	if wait := a.started.Add(interval).Sub(now); !a.closed && !a.started.IsZero() && wait > 0 {
		a.timer = schedule(wait)
		return 0
	}
	cursor := a.pending
	a.pending = 0
	a.started = now
	return cursor
}
