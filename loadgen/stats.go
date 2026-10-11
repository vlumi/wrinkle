package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// stats collects what happened inside the measurement window: sends and
// receipts count only when the message was sent inside it.
type stats struct {
	connectErrors atomic.Int64
	sendErrors    atomic.Int64
	received      atomic.Int64 // receipts of messages sent in the window
	sentInWindow  atomic.Int64

	mu          sync.Mutex
	windowStart time.Time
	windowEnd   time.Time
	latencies   []time.Duration // end to end, messages sent in the window
	sendRTTs    []time.Duration // send request round trips in the window
}

func (s *stats) startWindow() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.windowStart = time.Now()
}

func (s *stats) endWindow() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.windowEnd = time.Now()
}

func (s *stats) inWindow(t time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.windowStart.IsZero() && !t.Before(s.windowStart) && (s.windowEnd.IsZero() || t.Before(s.windowEnd))
}

func (s *stats) recordSent(at time.Time) {
	if s.inWindow(at) {
		s.sentInWindow.Add(1)
	}
}

func (s *stats) recordSendRTT(at time.Time, rtt time.Duration) {
	if !s.inWindow(at) {
		return
	}
	s.mu.Lock()
	s.sendRTTs = append(s.sendRTTs, rtt)
	s.mu.Unlock()
}

func (s *stats) recordReceipt(sentAt time.Time, latency time.Duration) {
	if !s.inWindow(sentAt) {
		return
	}
	s.received.Add(1)
	s.mu.Lock()
	s.latencies = append(s.latencies, latency)
	s.mu.Unlock()
}

func (s *stats) expectedReceipts(devicesPerUser int) int64 {
	return s.sentInWindow.Load() * int64(devicesPerUser)
}
