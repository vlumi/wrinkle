package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"
)

// result is one run, as a line of the results file.
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
	GroupSize      int     `json:"group_size,omitempty"`
	GroupShare     float64 `json:"group_share,omitempty"`
	ConnectErrors  int64   `json:"connect_errors"`
	Sent           int64   `json:"sent"`
	GroupSent      int64   `json:"group_sent,omitempty"`
	SendErrors     int64   `json:"send_errors"`
	DecryptErrors  int64   `json:"decrypt_errors"`
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
	BackendLiveB   float64 `json:"backend_live_bytes"`
	BackendConns   float64 `json:"backend_connections"`
}

// result summarizes the window, with the backend's samples from its start
// and end.
func (s *stats) result(cfg config, before, after sample) result {
	s.mu.Lock()
	defer s.mu.Unlock()
	lat := slices.Clone(s.latencies)
	slices.Sort(lat)
	rtt := slices.Clone(s.sendRTTs)
	slices.Sort(rtt)
	window := s.windowEnd.Sub(s.windowStart).Seconds()
	r := result{
		Time:           time.Now().UTC().Format(time.RFC3339),
		Label:          cfg.label,
		Commit:         cfg.commit,
		Connections:    cfg.connections,
		DevicesPerUser: cfg.devicesPerUser,
		TargetRate:     cfg.rate,
		DurationS:      window,
		WarmupS:        cfg.warmup.Seconds(),
		MarkRead:       cfg.markRead,
		GroupSent:      s.groupSent.Load(),
		ConnectErrors:  s.connectErrors.Load(),
		Sent:           s.sentInWindow.Load(),
		SendErrors:     s.sendErrors.Load(),
		DecryptErrors:  s.decryptErrors.Load(),
		Received:       s.received.Load(),
		Expected:       s.expectedReceipts(),
		SendP50Ms:      ms(percentile(rtt, 0.50)),
		SendP99Ms:      ms(percentile(rtt, 0.99)),
		P50Ms:          ms(percentile(lat, 0.50)),
		P90Ms:          ms(percentile(lat, 0.90)),
		P99Ms:          ms(percentile(lat, 0.99)),
		MaxMs:          ms(percentile(lat, 1)),
		BackendRSSB:    after.rss,
		BackendLiveB:   after.live,
		BackendConns:   after.connections,
	}
	if cfg.groupShare > 0 {
		r.GroupSize, r.GroupShare = cfg.groupSize, cfg.groupShare
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
	groups := ""
	if r.GroupShare > 0 {
		groups = fmt.Sprintf(" groups of %d: %.0f%% of sends (%d)", r.GroupSize, 100*r.GroupShare, r.GroupSent)
	}
	return fmt.Sprintf(
		"connections=%d devices/user=%d target=%.0f/s sent=%d (%.1f/s) errors=%d undecryptable=%d received=%d/%d%s\n"+
			"end-to-end p50=%.1fms p90=%.1fms p99=%.1fms max=%.1fms; send rtt p50=%.1fms p99=%.1fms\n"+
			"backend cpu=%.2fs (%.1f ms per 1000 msgs) rss=%.1f MiB live=%.1f MiB connections=%.0f",
		r.Connections, r.DevicesPerUser, r.TargetRate, r.Sent, r.SentRate, r.SendErrors, r.DecryptErrors, r.Received, r.Expected, groups,
		r.P50Ms, r.P90Ms, r.P99Ms, r.MaxMs, r.SendP50Ms, r.SendP99Ms,
		r.BackendCPUS, r.CPUMsPer1000, r.BackendRSSB/(1<<20), r.BackendLiveB/(1<<20), r.BackendConns)
}

// appendResult adds the result to a JSON lines file.
func appendResult(path string, r result) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}
