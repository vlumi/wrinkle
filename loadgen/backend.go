package main

import (
	"bufio"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// sample is what the backend's metrics say about itself at one moment.
type sample struct {
	ok          bool
	cpu         float64 // process CPU seconds
	rss         float64 // resident memory bytes
	live        float64 // heap and stacks in use, bytes
	connections float64
	// The history mover's messages moved, its rounds' seconds and its
	// rounds by shard, when the process runs it.
	moved, moveSeconds float64
	moveRounds         map[string]float64
	mover              bool
}

// scrape asks the backend to collect garbage, then reads its Prometheus
// text metrics. A runtime that does not report resident memory (the JVM on
// macOS) is measured with ps instead, found by the port it listens on.
func scrape(metricsURL string) sample {
	if metricsURL == "" {
		return sample{}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	collectGarbage(client, metricsURL)
	s := read(client, metricsURL)
	if s.ok && s.rss == 0 {
		s.rss = rssOfListener(metricsURL)
	}
	return s
}

// read reads the metrics as they are.
func read(client *http.Client, metricsURL string) sample {
	if metricsURL == "" {
		return sample{}
	}
	resp, err := client.Get(metricsURL)
	if err != nil {
		return sample{}
	}
	defer resp.Body.Close()
	return parseMetrics(resp.Body)
}

// collectGarbage asks the backend to collect, through the debug endpoint
// beside its metrics, so live memory is what survives a collection. A
// backend without the endpoint is sampled as it is.
func collectGarbage(client *http.Client, metricsURL string) {
	gc, err := url.JoinPath(metricsURL, "..", "debug", "gc")
	if err != nil {
		return
	}
	if r, err := client.Post(gc, "text/plain", nil); err == nil {
		r.Body.Close()
	}
}

// parseMetrics reads the metrics a sample needs. Live memory is what each
// runtime reports in use: Go's allocated heap and stacks, the JVM's memory
// pools, the BEAM's process and system memory.
func parseMetrics(r io.Reader) sample {
	s := sample{ok: true}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		name, v, ok := parseSample(line)
		if !ok {
			continue
		}
		switch name {
		case "process_cpu_seconds_total":
			s.cpu = v
		case "process_resident_memory_bytes":
			s.rss = v
		case "go_memstats_heap_alloc_bytes", "go_memstats_stack_inuse_bytes", "jvm_memory_used_bytes", "erlang_vm_memory_bytes_total",
			"nodejs_heap_size_used_bytes", "nodejs_external_memory_bytes":
			s.live += v
		case "wrinkle_connections":
			s.connections += v
		case "wrinkle_shard_work_total":
			if strings.Contains(line, `job="history_move"`) {
				s.moved, s.mover = v, true
			}
		case "wrinkle_shard_round_duration_seconds_sum":
			if strings.Contains(line, `job="history_move"`) {
				s.moveSeconds = v
			}
		case "wrinkle_shard_rounds_total":
			if strings.Contains(line, `job="history_move"`) {
				if s.moveRounds == nil {
					s.moveRounds = map[string]float64{}
				}
				s.moveRounds[label(line, "shard")] += v
			}
		}
	}
	return s
}

// parseSample splits a Prometheus text sample line, name{labels} value
// [timestamp], into the metric name and value. Label values are quoted and
// may hold spaces, commas, braces and escaped quotes.
func parseSample(line string) (name string, value float64, ok bool) {
	end := strings.IndexAny(line, "{ \t")
	if end <= 0 {
		return "", 0, false
	}
	name, rest := line[:end], line[end:]
	if rest[0] == '{' {
		brace := labelsEnd(rest)
		if brace < 0 {
			return "", 0, false
		}
		rest = rest[brace+1:]
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", 0, false
	}
	return name, v, true
}

// label returns the value of the sample line's label, unescaped only as
// far as shard names need: not at all.
func label(line, name string) string {
	_, rest, ok := strings.Cut(line, name+`="`)
	if !ok {
		return ""
	}
	value, _, _ := strings.Cut(rest, `"`)
	return value
}

// labelsEnd returns the index of the brace closing the label set that s
// starts with, skipping quoted values, or -1.
func labelsEnd(s string) int {
	quoted := false
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case !quoted && c == '}':
			return i
		}
	}
	return -1
}

// rssOfListener returns the resident memory of the process listening on the
// URL's port, in bytes, or 0.
func rssOfListener(metricsURL string) float64 {
	u, err := url.Parse(metricsURL)
	if err != nil || u.Port() == "" {
		return 0
	}
	pid, err := exec.Command("lsof", "-ti", "tcp:"+u.Port(), "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(pid)), "\n")
	rss, err := exec.Command("ps", "-o", "rss=", "-p", first).Output()
	if err != nil {
		return 0
	}
	kib, err := strconv.ParseFloat(strings.TrimSpace(string(rss)), 64)
	if err != nil {
		return 0
	}
	return kib * 1024
}
