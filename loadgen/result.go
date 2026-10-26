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
	AttachShare    float64 `json:"attachment_share,omitempty"`
	AttachSize     int     `json:"attachment_size,omitempty"`
	ConnectErrors  int64   `json:"connect_errors"`
	Sent           int64   `json:"sent"`
	GroupSent      int64   `json:"group_sent,omitempty"`
	SendErrors     int64   `json:"send_errors"`
	DecryptErrors  int64   `json:"decrypt_errors"`
	Uploads        int64   `json:"uploads,omitempty"`
	UploadErrors   int64   `json:"upload_errors,omitempty"`
	Downloads      int64   `json:"downloads,omitempty"`
	DownloadErrors int64   `json:"download_errors,omitempty"`
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
	// History reads, with --history: pages of 50, newest (all hot), among
	// the cold messages, and across the watermark; the mover's messages
	// moved for them, and per second of its rounds.
	History        int     `json:"history,omitempty"`
	HistoryReads   int     `json:"history_reads,omitempty"`
	HistoryErrors  int64   `json:"history_errors,omitempty"`
	HotP50Ms       float64 `json:"hot_p50_ms,omitempty"`
	HotP99Ms       float64 `json:"hot_p99_ms,omitempty"`
	ColdP50Ms      float64 `json:"cold_p50_ms,omitempty"`
	ColdP99Ms      float64 `json:"cold_p99_ms,omitempty"`
	AcrossP50Ms    float64 `json:"across_p50_ms,omitempty"`
	AcrossP99Ms    float64 `json:"across_p99_ms,omitempty"`
	Moved          float64 `json:"moved,omitempty"`
	MovedPerRoundS float64 `json:"moved_per_round_s,omitempty"`
}

func (r *result) addHistory(cfg config, h *historyStats) {
	hot, cold, across := h.summary()
	r.History, r.HistoryReads, r.HistoryErrors = cfg.history, len(hot)+len(cold)+len(across), h.errors
	r.HotP50Ms, r.HotP99Ms = ms(percentile(hot, 0.50)), ms(percentile(hot, 0.99))
	r.ColdP50Ms, r.ColdP99Ms = ms(percentile(cold, 0.50)), ms(percentile(cold, 0.99))
	r.AcrossP50Ms, r.AcrossP99Ms = ms(percentile(across, 0.50)), ms(percentile(across, 0.99))
	r.Moved = h.moved
	if h.moveSeconds > 0 {
		r.MovedPerRoundS = h.moved / h.moveSeconds
	}
}

func (r result) historyString() string {
	return fmt.Sprintf(
		"connections=%d history=%d per conversation, target=%.0f reads/s, reads=%d (%.1f/s) errors=%d\n"+
			"hot p50=%.1fms p99=%.1fms; cold p50=%.1fms p99=%.1fms; across p50=%.1fms p99=%.1fms\n"+
			"moved %.0f messages, %.0f per second of the mover's rounds",
		r.Connections, r.History, r.TargetRate, r.HistoryReads, float64(r.HistoryReads)/r.DurationS, r.HistoryErrors,
		r.HotP50Ms, r.HotP99Ms, r.ColdP50Ms, r.ColdP99Ms, r.AcrossP50Ms, r.AcrossP99Ms, r.Moved, r.MovedPerRoundS)
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
	if cfg.attachShare > 0 {
		r.AttachShare, r.AttachSize = cfg.attachShare, cfg.attachSize
		r.Uploads, r.UploadErrors = s.uploads.Load(), s.uploadErrors.Load()
		r.Downloads, r.DownloadErrors = s.downloads.Load(), s.downloadErrors.Load()
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
	if r.AttachShare > 0 {
		groups += fmt.Sprintf("\nattachments of %d bytes: %.0f%% of sends, uploads=%d (errors=%d) downloads=%d (errors=%d)",
			r.AttachSize, 100*r.AttachShare, r.Uploads, r.UploadErrors, r.Downloads, r.DownloadErrors)
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
