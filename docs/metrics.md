# Metrics

Every backend exposes Prometheus metrics at `/metrics` on its HTTP port, with these names, so one dashboard and one load generator work for all of them. Split, each process reports what it does: a gateway the connection and request metrics, a services instance the fan-out and push metrics.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wrinkle_connections` | gauge | | Open WebSocket connections on this gateway. |
| `wrinkle_requests_total` | counter | `type`, `code` | Requests handled. `type` is the request name in snake_case (`connect`, `send_message`, ...); `code` is `ok` or the `ErrorCode` name. |
| `wrinkle_request_duration_seconds` | histogram | `type` | From reading a request to queueing its response. |
| `wrinkle_events_pushed_total` | counter | | Events queued to connected devices; events dropped because a device fell behind are not counted. |
| `wrinkle_fanout_records_total` | counter | | Outbox records consumed from Kafka. |
| `wrinkle_fanout_deliveries_total` | counter | | Events appended to a user's log. |
| `wrinkle_push_sent_total` | counter | `result` | Push notifications to devices that are not connected: `ok`, `unregistered` (token cleared), `error`, `dropped` (too many in flight). Only from the Go, Java and TypeScript fan-out, and only with an APNs key set. |
| `wrinkle_fanout_lag_seconds` | histogram | | From an event being stored to its append to a user's log: the change-data path plus the worker. |
| `wrinkle_shard_rounds_total` | counter | `job`, `shard`, `result` | Rounds of the per-shard jobs, `job` being `log_trim` or `history_move`, on `shard` (`-` unsharded), each shown at zero from its loop's start: `ok`, `error`, `skipped` (another instance held the shard's lease) or `lost` (the lease was lost midway, and the round stopped). |
| `wrinkle_shard_round_duration_seconds` | histogram | `job` | The time of each round this instance held the lease for. |
| `wrinkle_shard_work_total` | counter | `job` | What the rounds did: events trimmed from the logs, messages moved to the cold tier. Its rate is the job's throughput. |
| `wrinkle_cold_read_duration_seconds` | histogram | `kind` | Reads from HBase: `range`, the part of a page of history below the watermark, or `message`, one message (a delete or reaction of a cold one, or a conversation's last message). |

Process metrics:

| Runtime | CPU | Resident memory | Live memory (after `POST /debug/gc`) |
|---|---|---|---|
| Go | `process_cpu_seconds_total` | `process_resident_memory_bytes` | `go_memstats_heap_alloc_bytes` + `go_memstats_stack_inuse_bytes` |
| Java | `process_cpu_seconds_total` | `ps` on macOS (the JVM exporter reports it only on Linux) | `jvm_memory_used_bytes`, all pools |
| Elixir | `process_cpu_seconds_total` | `ps` | `erlang_vm_memory_bytes_total`, processes and system (counts allocator carriers, so it can exceed RSS) |
| Rust | `process_cpu_seconds_total` | `process_resident_memory_bytes` | none: no collector |
| TypeScript (Node) | `process_cpu_seconds_total` | `process_resident_memory_bytes` | `nodejs_heap_size_used_bytes` + `nodejs_external_memory_bytes` (buffers outside the heap, the sockets' among them) |

Where a backend lacks resident memory, the load generator measures the process listening on the metrics port with `ps`.
