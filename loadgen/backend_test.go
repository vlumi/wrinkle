package main

import (
	"strings"
	"testing"
)

func TestParseMetrics(t *testing.T) {
	s := parseMetrics(strings.NewReader(`# HELP process_cpu_seconds_total CPU.
process_cpu_seconds_total 12.5
process_resident_memory_bytes 1000
go_memstats_heap_alloc_bytes 300
go_memstats_stack_inuse_bytes 20
jvm_memory_used_bytes{area="heap"} 5
wrinkle_connections{node="a"} 2
wrinkle_connections{node="b"} 3
not_a_number x
`))
	want := sample{ok: true, cpu: 12.5, rss: 1000, live: 325, connections: 5}
	if s != want {
		t.Fatalf("got %+v, want %+v", s, want)
	}
}
