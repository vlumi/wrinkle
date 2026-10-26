package main

import (
	"reflect"
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
jvm_memory_used_bytes{area="nonheap",id="Compressed Class Space"} 7
jvm_memory_used_bytes{area="heap",id="odd \"} name, {x}"} 11 1700000000000
nodejs_heap_size_used_bytes 1000
nodejs_external_memory_bytes 2000
wrinkle_shard_work_total{job="history_move"} 40
wrinkle_shard_work_total{job="log_trim"} 9
wrinkle_shard_round_duration_seconds_sum{job="history_move"} 0.5
wrinkle_shard_rounds_total{job="history_move",shard="-80",result="ok"} 3
wrinkle_shard_rounds_total{job="history_move",shard="-80",result="skipped"} 1
wrinkle_shard_rounds_total{job="history_move",shard="80-",result="ok"} 2
wrinkle_shard_rounds_total{job="log_trim",shard="80-",result="ok"} 7
wrinkle_connections{unterminated="} 100
not_a_number x
`))
	want := sample{ok: true, cpu: 12.5, rss: 1000, live: 3343, connections: 5,
		moved: 40, moveSeconds: 0.5, moveRounds: map[string]float64{"-80": 4, "80-": 2}, mover: true}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("got %+v, want %+v", s, want)
	}
}
