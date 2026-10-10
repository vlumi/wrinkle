# Metrics

Every backend exposes Prometheus metrics at `/metrics` on its HTTP port, with these names, so one dashboard and one load generator work for all of them.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wrinkle_connections` | gauge | | Open WebSocket connections on this gateway. |
| `wrinkle_requests_total` | counter | `type`, `code` | Requests handled. `type` is the request name in snake_case (`connect`, `send_message`, ...); `code` is `ok` or the `ErrorCode` name. |
| `wrinkle_request_duration_seconds` | histogram | `type` | From reading a request to queueing its response. |
| `wrinkle_events_pushed_total` | counter | | Events written to connected devices. |
| `wrinkle_fanout_records_total` | counter | | Outbox records consumed from Kafka. |
| `wrinkle_fanout_deliveries_total` | counter | | Events appended to a user's log. |
| `wrinkle_fanout_lag_seconds` | histogram | | From an event being stored to its append to a user's log: the change-data path plus the worker. |

Process metrics come from each runtime's client library and differ in name: `process_resident_memory_bytes` and `process_cpu_seconds_total` on Go; the JVM exporter's equivalents on Java. The load generator reads both forms.
