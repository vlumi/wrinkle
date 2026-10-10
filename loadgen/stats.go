package main

import (
	"fmt"
	"slices"
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

func newStats() *stats { return &stats{} }

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

func (s *stats) sent(at time.Time) {
	if s.inWindow(at) {
		s.sentInWindow.Add(1)
	}
}

func (s *stats) sendLatency(at time.Time, rtt time.Duration) {
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

type result struct {
	Time           string  `json:"time"`
	Label          string  `json:"label"`
	Commit         string  `json:"commit"`
	Connections    int     `json:"connections"`
	DevicesPerUser int     `json:"devices_per_user"`
	TargetRate     float64 `json:"target_rate"`
	DurationS      float64 `json:"duration_s"`
	WarmupS        float64 `json:"warmup_s"`
	MarkRead       bool    `json:"mark_read"`
	ConnectErrors  int64   `json:"connect_errors"`
	Sent           int64   `json:"sent"`
	SendErrors     int64   `json:"send_errors"`
	Received       int64   `json:"received"`
	Expected       int64   `json:"expected"`
	SentRate       float64 `json:"sent_rate"`
	SendP50Ms      float64 `json:"send_p50_ms"`
	SendP99Ms      float64 `json:"send_p99_ms"`
	P50Ms          float64 `json:"p50_ms"`
	P90Ms          float64 `json:"p90_ms"`
	P99Ms          float64 `json:"p99_ms"`
	MaxMs          float64 `json:"max_ms"`
	BackendCPUS    float64 `json:"backend_cpu_s"`
	CPUMsPer1000   float64 `json:"backend_cpu_ms_per_1000_msgs"`
	BackendRSSB    float64 `json:"backend_rss_bytes"`
	BackendConns   float64 `json:"backend_connections"`
}

func (s *stats) result(cfg config, before, after sample) result {
	s.mu.Lock()
	defer s.mu.Unlock()
	lat := slices.Clone(s.latencies)
	slices.Sort(lat)
	rtt := slices.Clone(s.sendRTTs)
	slices.Sort(rtt)
	window := s.windowEnd.Sub(s.windowStart).Seconds()
	r := result{
		Connections:    cfg.connections,
		DevicesPerUser: cfg.devicesPerUser,
		TargetRate:     cfg.rate,
		DurationS:      window,
		WarmupS:        cfg.warmup.Seconds(),
		MarkRead:       cfg.markRead,
		ConnectErrors:  s.connectErrors.Load(),
		Sent:           s.sentInWindow.Load(),
		SendErrors:     s.sendErrors.Load(),
		Received:       s.received.Load(),
		Expected:       s.sentInWindow.Load() * int64(cfg.devicesPerUser),
		SendP50Ms:      ms(percentile(rtt, 0.50)),
		SendP99Ms:      ms(percentile(rtt, 0.99)),
		P50Ms:          ms(percentile(lat, 0.50)),
		P90Ms:          ms(percentile(lat, 0.90)),
		P99Ms:          ms(percentile(lat, 0.99)),
		MaxMs:          ms(percentile(lat, 1)),
		BackendRSSB:    after.rss,
		BackendConns:   after.connections,
	}
	if window > 0 {
		r.SentRate = float64(r.Sent) / window
	}
	if before.ok && after.ok {
		r.BackendCPUS = after.cpu - before.cpu
		if r.Sent > 0 {
			r.CPUMsPer1000 = r.BackendCPUS * 1000 / float64(r.Sent) * 1000
		}
	}
	return r
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return sorted[i]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func (r result) String() string {
	return fmt.Sprintf(
		"connections=%d devices/user=%d target=%.0f/s sent=%d (%.1f/s) errors=%d received=%d/%d\n"+
			"end-to-end p50=%.1fms p90=%.1fms p99=%.1fms max=%.1fms; send rtt p50=%.1fms p99=%.1fms\n"+
			"backend cpu=%.2fs (%.1f ms per 1000 msgs) rss=%.1f MiB connections=%.0f",
		r.Connections, r.DevicesPerUser, r.TargetRate, r.Sent, r.SentRate, r.SendErrors, r.Received, r.Expected,
		r.P50Ms, r.P90Ms, r.P99Ms, r.MaxMs, r.SendP50Ms, r.SendP99Ms,
		r.BackendCPUS, r.CPUMsPer1000, r.BackendRSSB/(1<<20), r.BackendConns)
}
