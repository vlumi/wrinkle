package main

import (
	"bufio"
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
}

// scrape reads the backend's Prometheus text metrics. Live memory is what the
// runtime reports after a collection: Go's allocated heap and stacks, the
// JVM's memory pools. A runtime that does not report resident memory (the JVM on
// macOS) is measured with ps instead, found by the port it listens on.
func scrape(metricsURL string) sample {
	if metricsURL == "" {
		return sample{}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if gc, err := url.JoinPath(metricsURL, "..", "debug", "gc"); err == nil {
		if r, err := client.Post(gc, "text/plain", nil); err == nil {
			r.Body.Close()
		}
	}
	resp, err := client.Get(metricsURL)
	if err != nil {
		return sample{}
	}
	defer resp.Body.Close()
	s := sample{ok: true}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		name, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if i := strings.IndexByte(name, '{'); i >= 0 {
			name = name[:i]
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		switch name {
		case "process_cpu_seconds_total":
			s.cpu = v
		case "process_resident_memory_bytes":
			s.rss = v
		case "go_memstats_heap_alloc_bytes", "go_memstats_stack_inuse_bytes", "jvm_memory_used_bytes":
			s.live += v
		case "wrinkle_connections":
			s.connections += v
		}
	}
	if s.rss == 0 {
		s.rss = rssOfListener(metricsURL)
	}
	return s
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
