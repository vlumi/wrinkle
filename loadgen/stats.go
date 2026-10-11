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
	decryptErrors atomic.Int64 // received messages that did not open
	received      atomic.Int64 // receipts of messages sent in the window
	sentInWindow  atomic.Int64
	groupSent     atomic.Int64 // of sentInWindow, to groups
	expected      atomic.Int64 // receipts due for sentInWindow
	// Attachments of messages sent in the window: uploaded by senders,
	// downloaded, checked and decrypted by receivers.
	uploads        atomic.Int64
	uploadErrors   atomic.Int64
	downloads      atomic.Int64
	downloadErrors atomic.Int64
	downloading    atomic.Int64 // in progress, any message

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

// recordSent counts a send due to reach the given number of devices.
func (s *stats) recordSent(at time.Time, receivers int64, group bool) {
	if !s.inWindow(at) {
		return
	}
	s.sentInWindow.Add(1)
	s.expected.Add(receivers)
	if group {
		s.groupSent.Add(1)
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

func (s *stats) expectedReceipts() int64 {
	return s.expected.Load()
}

// recordUpload counts an attachment upload started at the time, and
// returns the errors so far.
func (s *stats) recordUpload(at time.Time, ok bool) int64 {
	if !ok {
		return s.uploadErrors.Add(1)
	}
	if s.inWindow(at) {
		s.uploads.Add(1)
	}
	return s.uploadErrors.Load()
}

// recordDownload counts an attachment download of a message sent at the
// time, and returns the errors so far.
func (s *stats) recordDownload(sentAt time.Time, ok bool) int64 {
	if !ok {
		return s.downloadErrors.Add(1)
	}
	if s.inWindow(sentAt) {
		s.downloads.Add(1)
	}
	return s.downloadErrors.Load()
}
