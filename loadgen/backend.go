package main

import (
	"bufio"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// sample is what the backend's metrics say about itself at one moment.
type sample struct {
	ok          bool
	cpu         float64 // process CPU seconds
	rss         float64 // resident memory bytes
	connections float64
}

// scrape reads the backend's Prometheus text metrics. Names for CPU and
// memory differ between runtimes (docs/metrics.md); both are recognized.
func scrape(url string) sample {
	if url == "" {
		return sample{}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
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
		case "wrinkle_connections":
			s.connections += v
		}
	}
	return s
}
