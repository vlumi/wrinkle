# TypeScript backend

The TypeScript backend is a full implementation, like Go and Java: one program, [`wrinkle`](wrinkle), that runs any of the three roles (the WebSocket gateway, the services, and the fan-out worker, with APNs push from the fan-out) as a monolith or split into gateways and services instances over the internal gRPC contract. Its services instances serve any gateway, and its gateway any services. Node 26 runs the TypeScript sources directly, with no build step; `tsc` only checks the types. It uses [ws](https://github.com/websockets/ws) for the connections, mysql2 against vtgate (or MySQL itself on the infrastructure's `PROFILE=mysql` stack, connecting as `root` either way), [@confluentinc/kafka-javascript](https://github.com/confluentinc/confluent-kafka-javascript) (librdkafka) for Kafka, the official `redis` client for Valkey, [Connect](https://connectrpc.com/) speaking gRPC for the internal RPC with the same generated code ([protobuf-es](https://github.com/bufbuild/protobuf-es)) as the web client, `@node-rs/argon2` for passwords, and prom-client for metrics. A process runs all its JavaScript on one thread; the database, Kafka and password hashing work on libuv's threads and librdkafka's own. Scaling past one core is more processes, which the split shape and the cluster provide.

The design common to every backend (data model, flows, guarantees, failure handling) is in [docs/architecture.md](../../docs/architecture.md); this page covers how the TypeScript code realizes it. The Go code was the reference for every feature: the same SQL, validation, error codes and outbox rows, so the backends share data, and conformance holds this one to the contract like the others.

## Layout

```text
src/main.ts         flags and WRINKLE_* settings, roles, startup and shutdown; .env loading
src/gateway/        WebSocket connections, limits, coalesced acks, push subscriptions, keepalive, drain
src/services/       the Backend interface; Local (in-process, a handler per request from each feature) and the gRPC server and client
src/accounts/       Register, SignIn, Connect, passwords and device tokens, devices and their keys, profiles, name claims, deletion
src/devicesync/     acks, Sync and the trimmer of the logs, the conversation list, opening conversations and groups on the user's side, push tokens
src/conversation/   direct conversations and notes, groups and notices, key epochs, send, read marks, history and its cold tier, reactions, deletion
src/contacts/       each user's side toward another (a request or a block), the list, and the block and contact checks
src/uploads/        upload slots for blobs, the attachments a message uses, and the sweeper of unused and abandoned uploads
src/stickers/       stickers made from uploads, palettes and their events
src/fanout/         the Kafka consumer: both outboxes to the recipients' logs and lists, batched per shard, then published
src/push/           APNs (token auth, HTTP/2) and the notifier for devices not connected
src/presence/       Valkey: device-to-instance hash, instance liveness keys, per-user push channels, leases
src/internal/       ids, contract errors, the keyspace pools and their time helpers, blob storage, shards and a pool per shard, background work per shard, metrics, logging, HTTP
src/gen/            the generated contract (make proto), as JavaScript with type declarations
wrinkle             runs src/main.ts with node --expose-gc
split.sh            a TypeScript gateway beside a TypeScript services process, for conformance and measurement
Dockerfile          the image for the cluster, built from the repository root
```

## Roles and startup

[main.ts](src/main.ts) takes `--roles`, a comma-separated list of `gateway`, `services` and `fanout`, as the Go binary does: `gateway,fanout` (the default) is the monolith; `gateway` with `--services <addr>` forwards every request over gRPC and opens no database pools; `services,fanout` is a services instance. Wherever the services run, as the `services` role or in-process in the monolith, they also run the uploads sweeper, the trimmer of the user logs and the mover of old history to the cold tier; a fan-out-only process runs none of them. Every role connects to Valkey: the gateway for presence, the fan-out for pushes, the services for the leases of their background work. SIGTERM or SIGINT drains the gateway, stops the fan-out, and exits.

| Flag | Variable | Default | Meaning |
|---|---|---|---|
| `--roles` | `WRINKLE_ROLES` | `gateway,fanout` | the roles of this process |
| `--listen` | `WRINKLE_LISTEN` | `127.0.0.1:8080` | HTTP: `/v1/ws` (gateway), `/metrics`, `/healthz`, `POST /debug/gc` on loopback |
| `--mysql` | `WRINKLE_MYSQL` | `127.0.0.1:15003` | vtgate, or MySQL on the `mysql` profile |
| `--kafka` | `WRINKLE_KAFKA` | `localhost:19092` | brokers, comma-separated |
| `--valkey` | `WRINKLE_VALKEY` | `localhost:6379` | Valkey |
| `--hbase-rest` | `WRINKLE_HBASE_REST` | `http://127.0.0.1:8086` | the cold tier's HBase, by its REST server |
| `--services` | `WRINKLE_SERVICES` | | gateway: the services RPC to forward to |
| `--services-listen` | `WRINKLE_SERVICES_LISTEN` | `127.0.0.1:9000` | services: the gRPC address |
| `--node` | `WRINKLE_NODE` | the host name | this node's name in presence |
| `--db-conns` | `WRINKLE_DB_CONNS` | 64 | connections per keyspace pool |
| `--fanout-concurrency` | `WRINKLE_FANOUT_CONCURRENCY` | 64 | conversations or users delivered at a time |
| `--ack-interval-ms` | `WRINKLE_ACK_INTERVAL_MS` | 60000 | least time between two ack writes of a connection; 0 writes each as it comes |
| `--log-retention-s` | `WRINKLE_LOG_RETENTION_S` | 2592000 (30 days) | how long the user logs keep events |
| `--log-trim-interval-s` | `WRINKLE_LOG_TRIM_INTERVAL_S` | 60 | time between two trims of each shard of the user logs |
| `--history-hot-s` | `WRINKLE_HISTORY_HOT_S` | 2592000 (30 days) | how long messages stay in MySQL before moving to the cold tier |
| `--cold-move-interval-s` | `WRINKLE_COLD_MOVE_INTERVAL_S` | 60 | time between two moves of each shard to the cold tier |
| `--upload-unused-s` | `WRINKLE_UPLOAD_UNUSED_S` | 86400 | how long an upload may go unused, or an attachment wait for its message, before the sweeper deletes it |
| `--upload-sweep-interval-s` | `WRINKLE_UPLOAD_SWEEP_INTERVAL_S` | 600 | time between two sweeps of the uploads |
| `--blob-url`, `--blob-key-id`, `--blob-secret`, `--blob-region` | `WRINKLE_BLOB_*` | the local Garage, with the key and secret from `.env` (`make infra-up` writes them) | blob storage, as for Go |
| `--apns-key`, `--apns-key-id`, `--apns-team-id`, `--apns-topic` | `WRINKLE_APNS_KEY`, `WRINKLE_APNS_KEY_ID`, `WRINKLE_TEAM_ID`, `WRINKLE_APNS_TOPIC` | off | APNs from the fan-out, as for Go |

`.env` in the working directory, or else at the repository root, is read first; variables already set win. `WRINKLE_LOG_LEVEL` (`debug`, `info`, `warn`, `error`) sets the logging, as `key=value` lines on stderr. An unhandled rejection or uncaught exception is logged and exits with status 1. The launcher, [wrinkle](wrinkle), which the image also runs, sets `UV_THREADPOOL_SIZE` to the cores plus libuv's default four, unless already set, so password hashes leave threads for DNS and file work.

## Gateway

[conn.ts](src/gateway/conn.ts) holds one connection, [server.ts](src/gateway/server.ts) all of them. The limits are the contract's: `ws`'s `maxPayload` closes an oversize frame with 1009; a token bucket per connection (100, refilled at 50 a second) and 32 requests in flight answer the excess with `RESOURCE_EXHAUSTED` and drop other frames; Connect must complete within 5 s of the connection opening or of a successful Register or SignIn's response, or the connection closes with 1008; a failed one does not restart the deadline, so it cannot hold the connection open. A connection may make 5 Register or SignIn requests, counted before they are forwarded and given back when answered `INVALID_ARGUMENT`; more get `RESOURCE_EXHAUSTED`. Requests run as promises against the `Backend` with a 10 s timeout (`UNAVAILABLE` beyond it). Pushed events go out undecoded: a push that is only an event becomes a frame by a new tag and length in front of the stored bytes. More than 32 frames waiting in a connection's send queue closes it with 1013 when an event arrives; replies always queue, since the requests in flight bound them. Acks are written once per interval and when the connection ends ([limits.ts](src/gateway/limits.ts)).

[registry.ts](src/gateway/registry.ts) keeps a user's `push:<user>` channel subscribed on one Valkey connection while the user has a connection here; subscribing and unsubscribing a user run one at a time, so an attach racing the last detach ends subscribed, and Connect waits up to 5 s for the subscription (`UNAVAILABLE` otherwise). A `signed_out` push closes the listed devices' connections with 4001. Presence and the instance key are as in Go: `presence:<user>` names this instance for each device, and `gateway:<instance>` lives 90 s, refreshed every 30 s by the same timer that pings every connection and closes those idle for 90 s with 1001. A drain answers new upgrades and `/healthz` with 503, closes every connection with 1001, waits up to 5 s for requests and ack writes in flight, then 3 s for the closing handshakes, and withdraws the instance.

## Services

Each feature's `feature.ts` maps its requests to handlers; [local.ts](src/services/local.ts) applies the state rules (Register, SignIn and Connect only before Connect, everything else after) and turns errors into the contract's codes, internal ones logged and answered as `INTERNAL`. [rpc.ts](src/services/rpc.ts) serves the same `Backend` over gRPC on an HTTP/2 server allowing 2^20 streams, and its client is what a split gateway uses. Every feature ports the Go service of the same name; the Go README's Services section describes the requests, and the differences are only these:

- Times are written at millisecond precision, since JavaScript's `Date` has no microseconds. Reads keep what the database holds ([db.ts](src/internal/db.ts) reads DATETIME columns as strings and keeps their microseconds), and the fan-out writes the event times of Go-written outboxes as they came.
- Strings arrive decoded, so Go's checks for invalid UTF-8 become checks that a string is well formed.
- Password hashes run on libuv's thread pool, as many at once as there are cores, as in Go: a semaphore in [password.ts](src/accounts/password.ts) holds the rest, each waiting no longer than its request's deadline (`UNAVAILABLE` after). The deadline reaches it through `AsyncLocalStorage` ([deadline.ts](src/internal/deadline.ts)): the gateway's 10 s in-process, the gRPC call's in a services process.
- The cold tier ([cold.ts](src/conversation/cold.ts), [mover.ts](src/conversation/mover.ts)) goes through HBase's REST server with the built-in `fetch`, since Node has no maintained native client: puts as JSON with base64 keys, columns and values, and every read through a scanner, since binary keys do not fit a URL; a point read is a scan of one key. The REST scanner reads only forward, so a page of the newest n messages in a cold range is a forward scan of its last n seqs, turned around; this relies on a conversation's cold seqs being contiguous, which they are, since messages are only tombstoned and the mover moves every seq up to the watermark. A failing get, scan or put is logged and answered `UNAVAILABLE`, as in Go. The mover runs with the services, beside the trimmer, rather than in the fan-out, and so does the uploads sweeper.
- The trimmer and the mover run through [shardwork.ts](src/internal/shardwork.ts) as Go's do through `shardwork`: the shards are found a minute after start, or the interval when shorter (again after each interval while that fails), then each shard has its own loop, a round at once and then every interval, under the shard's lease in Valkey (`lease:<job>:<keyspace>/<shard>`, 30 s, renewed every 10 s and dropped after the round). A lost lease, or one whose renewals have failed for as long as it lasts, aborts the round (counted as `lost`) between pages and transactions, since mysql2 cannot cancel a query; stopping waits for the rounds and drops their leases.

## Fan-out

[worker.ts](src/fanout/worker.ts) is a member of the shared consumer group `wrinkle-fanout` on both outbox topics, with the cooperative sticky assignor the Go and Java workers also offer. Batches of up to 256 records per partition, up to 32 partitions at a time; within a batch, keys (conversations, users) run in parallel, up to `--fanout-concurrency` across all partitions, and a key's records in order. A record is retried five times with growing pauses, each failure logged at debug; if it still fails, it is logged as an error, the batch is left unresolved, so the client seeks the partition back, and the worker waits a second. Offsets are committed only after every record of a batch is delivered; a batch whose partition moved to another member is not committed, and the new owner's redelivery is skipped by the logs' source keys.

Delivery ([deliver.ts](src/fanout/deliver.ts)) and the kinds of records (profiles, devices, contacts, palettes, deletions of accounts) follow Go's. A message that is not a notice goes through the batcher ([batch.ts](src/fanout/batch.ts)): each recipient's copy is queued on the recipient's shard of the user keyspace, found from the `xxhash` vindex ([shards.ts](src/internal/shards.ts), with XXH64 in [xxhash.ts](src/internal/xxhash.ts)) and the shards vtgate lists at start, and one transaction per shard stores up to 256 queued copies with a multi-row statement each for the events, the heads and the list entries; a batch that fails is appended copy by copy. A shard's first batch starts on the next turn of the event loop, so the copies of one record and of the records handled in the same turn share it.

Push ([push/](src/push/)) ports Go's: an ES256 provider token signed with `node:crypto` and reused for 45 minutes, one HTTP/2 session per APNs origin, at most 64 sends in flight (beyond that counted as `dropped`), rejected tokens cleared unless the device registered a new one.

## Building, running, testing

```sh
make ts-deps            # npm ci in backends/ts, only when the lockfile changed (it compiles librdkafka)
make ts-lint ts-test    # tsc, eslint, prettier; node:test unit tests
make conformance-ts-mono conformance-ts-gw
make measure-ts-mono measure-ts-gw
```

Unit tests ([`*.test.ts`](src/)) hold the vectors every backend shares: argon2id and device tokens, SigV4 presigning, the vindex keyspace ids and XXH64; and malformed hashes, name, icon and label validation, Debezium record parsing, membership flags, direct members, topics, notice and client group ids, blob ids, rotation only after the first epoch, contact views and sides, the sweeper's verdict on a pending upload, hash slots and their bounded wait, DATETIME values keeping their microseconds, the ack writer and the token bucket, the wrapping of a pushed event into a frame, the worker's grouping by key and its concurrency limit, shard leases (a round run once that drops its lease, a shard leased elsewhere skipped, a lost lease stopping the round, as does one unrenewed for as long as it lasts), the trimmer's tail per user, APNs signing, addressing, background sends, unregistered tokens and token reuse, the sealed content in alerts and its omission when too large, and the cold tier's row keys and cells and where a page goes on in it. Everything else is covered by the conformance tests.

In the image, the Kafka client builds librdkafka from source, since it has no prebuilt binary for this Node; the [Dockerfile](Dockerfile) builds it outside the package's scoped path, because GNU `ar`'s script mode cannot read the `@` in it.

## Measured

On 2026-10-24 at commit 7e6a395, from a fresh stack, medians of three ([2026-10-24-shapes.md](../../docs/results/2026-10-24-shapes.md)): the monolith delivered 500 messages per second at 62 ms p50 and 115 ms p99 end to end and 1000 at 103 / 418 ms, the lowest of any shape at that rate, with 676 ms of CPU per thousand messages at 500 for every role, about two thirds of Go's, and keeps 9 KiB of live memory per idle connection; the split gateway (`ts-gw`) delivered 500/s at 65 / 117 ms with 321 ms of gateway CPU per thousand messages, and 1000/s at 1251 / 2032 ms, every message but its sends waiting 1.2 s at the median, and held 4000 idle connections in 303 MiB with 5 KiB of live memory each, the least of the runtimes. Past the limit it keeps working on every send it accepted, so latency grows into tens of seconds rather than sends failing.

## Gaps

- A batch transaction has no timeout of its own: mysql2 cannot cancel a query, so a hung one stalls its shard's queue until the connection fails.
- Split, the gateway's sends queue at 1000 messages a second while the monolith's do not; whether in the internal RPC or the services process's one thread is not established ([ROADMAP.md](../../ROADMAP.md), Parity).
- The gateway answers a request that takes over 10 s with an error, but the work behind it goes on, and the database pool queues for a connection without a limit: past the database's limit, sends the client gave up on still run.
- As for every gateway: no limits per user or per address, which need shared state ([ROADMAP.md](../../ROADMAP.md)).
