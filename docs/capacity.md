# Capacity estimates

The sizing the design was made for, and how the measured prototype compares. Assumptions are marked; the rest follows from them.

## Estimates

- **Concurrent connections**: 200M users × 25 % online at peak (assumed) × 2 connected devices (assumed) = **~100M WebSockets**.
- **Gateway nodes**: ~100k connections per node (Netty or Go, epoll) → **~1,000 nodes**, ~1,300 with headroom.
- **Writes**: 50k sends/s + 50k mark-as-read/s at peak.
- **User log appends**: 2 per send or read, one per participant → **~200k/s**.
- **Pushes**: ~3 devices per chat event on average → **~300k/s**, ~230 per gateway node.
- **Messages**: average 40 % of peak (assumed) = 20k/s → 1.7 billion a day → **~24 billion in 2 weeks** of retention.
- **Message size**: ~300 B (assumed): 200 B of text, well above a typical chat message, plus metadata. The API caps text at 10 KB; storage is sized for the average.
- **Storage**: ~7 TB in the conversation store + ~15 TB in the user logs (a copy per participant) + ~5 TB of read events → **~27 TB, ~80 TB with 3 replicas**, the steady state for 2 weeks of retention.
- **Shards**: ~64 per keyspace → ~1.5k writes/s and 100 to 250 GB each.
- **Bandwidth**: 15 MB/s in, ~45 MB/s out at peak: not a constraint.
- **End to end**, send on one device to receipt on a connected recipient, through the change-data path: ~100 to 300 ms.

## Measured against the estimates

From [results/2026-10-15-shapes.md](results/2026-10-15-shapes.md) (commit bfc6a78, six shapes, three repetitions each) and, for the database, the investigation of its ceiling before encryption (ROADMAP.md, "Database ceiling"); scaled to the per-node and per-shard assumptions above. The prototype runs the whole database as one `mysqld` on about five cores with two shards per keyspace, so per-shard figures are read off per `mysqld`.

| Estimate | Measured |
|---|---|
| ~100k connections per gateway node | Resident memory at 4000 idle connections ([shapes](results/2026-10-15-shapes.md)): the Rust gateway 124 MiB, Go 225, Elixir 245, Java 405. By slope, 31 to 50 KiB of resident memory per connection for the four gateways, 11 to 28 KiB live, which is 3 to 5 GB for 100k: every one fits a 16 GB node. Go's cost is two goroutine stacks and the HTTP buffers its WebSocket library keeps ([idle memory](results/2026-10-10-idle-memory.md)); going lower needs a poller-driven library. |
| ~1.5k writes/s per shard | The single `mysqld` ran about 3400 transactions/s at 1000 messages/s, one send, two fan-out appends and the acks per message, at 1.7 cores; the Vitess layer in front of it, all in one vtcombo process, was the limit at 2.7 cores (ROADMAP.md, "Database ceiling"). More than twice the per-shard assumption on one process; a real shard has its own `mysqld` and disk. |
| ~3k user-log appends/s per shard | 1000/s sustained alongside the sends on the same `mysqld`. Same order. |
| ~230 pushes/s per gateway node | 1000/s from one process acting as gateway and fan-out worker, far from any limit. |
| 100 to 300 ms end to end | 86 to 118 ms p50 at 500/s across all six measured shapes from a fresh stack, p99 189 to 268 ms ([shapes](results/2026-10-15-shapes.md)), with Debezium polling every 20 ms. With its default 500 ms it was 441 ms. Skipping the change-data hop, listed as a later optimization, may not be needed. |
| JVM gateway on ZGC | Not tried yet; the Java runs used the default collector and a JVM-sized heap. The live slope, 11 to 12 KiB per connection, is the lowest of the runtimes; the Java gateway's resident memory starts at almost 300 MiB. |

Where the prototype departs from the design, and what it means for extrapolating:

- **Push routing.** The design routes a push directly to the gateway nodes holding the user's devices, found in Presence, one RPC per node, and rejects a subscription per user because of the count at 100M connections. The prototype uses a Valkey pub/sub channel per user. Invisible at the prototype's size; the design's point at scale.
- **Acks.** The design forwards a device's ack at most once a minute and on disconnect. The web client acks every event and the load generator once a second per device, so the measured load includes more ack writes than the design would see.
- **Rate limits** (50 frames a second per connection) are far above what the load generator sends per device, so they did not shape its traffic.
