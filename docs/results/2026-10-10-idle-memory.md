# Idle memory per connection, 2026-10-10, after tuning

The idle series of [measuring.md](../measuring.md) again, after looking at the Go backend's heap profile and with live memory sampled after a forced collection. Raw runs: [go](2026-10-10-idle-memory-go.jsonl), [java](2026-10-10-idle-memory-java.jsonl). Same machine and infrastructure as the [morning's runs](2026-10-10-go-vs-java.md).

- Commit: 2fc8072
- Backends: Go 1.27.2; OpenJDK 25.0.4.1 with default flags. One process each, default flags.
- Live memory: Go's allocated heap plus goroutine stacks, the JVM's memory pools, both read right after POST /debug/gc. RSS as before (ps for the JVM).

## Numbers

| Connections | Go RSS | Go live | Java RSS | Java live |
|---|---|---|---|---|
| 500 | 41.1 MiB | 16.8 MiB | 279.8 MiB | 109.6 MiB |
| 1000 | 71.5 MiB | 35.8 MiB | 307.5 MiB | 115.6 MiB |
| 2000 | 93.1 MiB | 56.9 MiB | 274.2 MiB | 126.7 MiB |
| 4000 | 180.0 MiB | 117.2 MiB | 345.2 MiB | 151.4 MiB |

Least-squares slopes:

| | Go | Java |
|---|---|---|
| RSS per connection | 39 KiB (46 in the morning) | 16 KiB (13) |
| Live memory per connection | **29 KiB** | **12 KiB** |
| Live baseline | 4 MiB | 104 MiB |

## What the Go profile showed, and what changed

With 2000 idle connections, the heap profile and runtime metrics put the morning's 46 KiB like this:

- Two goroutines per connection at about 8.7 KiB of stack each: the read loop, and one the WebSocket library runs for its timeouts. 17 KiB.
- The HTTP server's buffered reader and writer, 4 KiB each, which the library keeps after the hijack. 8 KiB.
- The outbound queue: a 256-slot channel of slices, 6 KiB. Now 32 slots, and the writer goroutine runs only while something is queued.
- The HTTP request, held alive through its context: headers, URL, a couple of KiB. The connection has its own context now.
- Not per connection at all, but counted by the slope: the Valkey client's default pool of 160 connections with 32 KiB buffers each (12 MiB), which grows with the connect storm, and the gzip writers of the metrics endpoint. Pool is 32 connections with 4 KiB buffers now; metrics are uncompressed.
- GC headroom: RSS sits above live memory by whatever the collector has not reclaimed; sampling after a collection removes that from both backends.

What remains in Go's 29 KiB is mostly the two stacks and the two buffers, neither of which this code owns. Netty has no thread and no heap buffer per idle connection; its per-connection cost is a channel object and pooled direct memory, and the JVM's live figure does not even include the latter. Going lower on Go means a WebSocket library that reads from the poller without a goroutine per connection (gobwas/ws), which the design lists as the epoll-level option.

## A different WebSocket library

The same series with the gateway on [gws](https://github.com/lxzan/gws) instead of coder/websocket, one read goroutine per connection, 1 KiB buffers, and a 1 KiB reader handed in at hijack in place of the HTTP server's 4 KiB one. Raw runs: [go-gws](2026-10-10-idle-memory-go-gws.jsonl); the port is [2026-10-10-gws-gateway.patch](2026-10-10-gws-gateway.patch), which passes the conformance tests.

| Per idle connection | coder/websocket | gws |
|---|---|---|
| Live slope | 29 KiB | 26 KiB |
| RSS slope | 39 KiB | 23 KiB |

At 2000 connections the goroutine count halves and the allocated heap mid-run halves too, but the library's single read goroutine carries a deeper stack (about 11.5 KiB against 8.7) and brings its own per-connection structures and buffers, so the live figure improves by a tenth. The gateway stays on coder/websocket, the mainstream and better-maintained choice; gobwas/ws, the design's epoll-level option, has been dormant since 2024. What would move the number is a gateway without a goroutine per connection, which is a design change rather than a library swap.

## Caveats

- Java's live figure excludes Netty's direct buffers, which the JVM does not report in its memory pools; its RSS slope, 16 KiB, is the safer comparison against Go's 39.
- Still single runs. The points are closer to a line than in the morning, but a quoted figure wants `REPS=3`.
