# Roadmap

What is done, what is open and why, and the gaps that are known and accepted for now. How it got here, the questions settled on the way and the figures since superseded are in [docs/history.md](docs/history.md); what a public deployment would need, and its cost, in [docs/production.md](docs/production.md). Features are tracked as issues; the number follows the name.

## Done

- **Contract and infrastructure.** The Protobuf contract for devices (`proto/wrinkle/v1`) and between gateway and services (`proto/wrinkle/backend/v1`); SQL schemas and two Vitess keyspaces; Docker Compose with Vitess, Kafka, Debezium, Valkey, Garage, Prometheus and Grafana; the outbox streamed from the binlog, each row deleted in the transaction that inserts it; `make ci` as the local gate. [Architecture: Processes](docs/architecture.md#processes), [Data model](docs/architecture.md#data-model).
- **Backends.** Go and Java implement everything, as a monolith or split into gateways and services instances over the internal RPC; Elixir and Rust implement the gateway and run beside Go or Java services. All four pass the conformance tests. [Implementations](docs/architecture.md#implementations).
- **Clients and measurement.** A web client (React, TypeScript), an iOS client (SwiftUI, local storage), a Go client library, and a load generator with the measurement protocol in [docs/measuring.md](docs/measuring.md); every backend exposes the same metric names ([docs/metrics.md](docs/metrics.md)).
- **Scaling out.** Services instances and gateways in any number and language behind two HAProxy balancers, natively (`make lb-up`) or as a cluster of containers (`make cluster-up`); the services balancer spreads requests, not connections. [Shapes](docs/architecture.md#shapes), [Scaling](docs/architecture.md#scaling).
- **Hardening.** Per-connection limits, a Connect deadline, keepalive and close codes in every gateway, from the contract; graceful drain; debug endpoints only on the host; presence that expires with its process. [Connections](docs/architecture.md#connections).
- **Accounts and profiles (#1).** Passwords, a token per device, random UUIDs for users and conversations, names unique ignoring case, profiles with icons and avatars, devices listed and signed out remotely, account deletion; TLS for devices on the network through an HAProxy edge; object storage for blobs. [Accounts](docs/architecture.md#accounts), [Blobs](docs/architecture.md#blobs).
- **Groups (#2).** Topic, admins, up to 256 members, every change a notice in the group's sequence, read events to the senders of the messages read. [Groups](docs/architecture.md#groups).
- **Deleting one's own messages (#4).** A tombstone that keeps the seq; copies in members' logs and the list preview scrubbed by the fan-out; a background push so iOS withdraws a shown notification. [Deleting a message](docs/architecture.md#deleting-a-message).
- **End-to-end encryption (#6).** A key per conversation and epoch, wrapped for every member device; rotation on membership and device changes; history shared to a new device with the user's approval; safety numbers. Test vectors shared by Go, TypeScript and Swift. [End-to-end encryption](docs/architecture.md#end-to-end-encryption).
- **Reactions (#3).** One sealed set per user and message, replaced as a whole. [Reactions](docs/architecture.md#reactions).
- **Stickers (#5).** Public, permanent blobs made from a user's images; a palette of up to 200 per user, synced across devices. [Stickers](docs/architecture.md#stickers).
- **Attachments (#7).** Images and files encrypted on the device with a key per file, thumbnails made on the device, blob ids in clear for their lifecycle; the load generator can include them. [Attachments](docs/architecture.md#attachments).
- **Contacts and blocking (#10).** Mutual contacts by request and acceptance; groups take only the adder's contacts; a block refuses the blocked user one-to-one and hides them in shared groups on the blocker's devices. [Contacts and blocking](docs/architecture.md#contacts-and-blocking).
- **Notes.** Each user's own conversation, shared among their devices, created on first open and pinned at the top of the list. [Groups](docs/architecture.md#groups).
- **Push notifications.** `RegisterPushToken` in every backend; APNs from the Go fan-out to devices that are not connected, with the sealed content for the iOS notification service extension to decrypt. [Sending a message](docs/architecture.md#sending-a-message).
- **Measurements.** Every shape measured on the current code, three repetitions each: all six sustain 500 messages per second end to end encrypted at 86 to 118 ms p50, and the database sets the limit above it. [docs/results/2026-10-15-shapes.md](docs/results/2026-10-15-shapes.md).

## Open

In the order they would be done. Each item says why it comes where it does and how it would be built; the features and experiments after the numbered items are not ordered.

### 1. Coalesced acks

**Why:** the design forwards a device's acks at most once a minute per device, and on disconnect; every gateway writes them as they come, one write at a time per connection, keeping only the highest cursor that arrived meanwhile. Acks are about 30 % of the database's transactions, so this is the cheapest relief for the database ceiling, and it brings all four gateways to the design.

**Plan:** in each gateway (Go's `ackCursor`, Java's `AckCoalescer`, and their Elixir and Rust counterparts) keep the highest cursor per connection and write it when the connection closes, when the gateway drains, and otherwise at most once per interval (60 s by default). An ack lost to a crashed gateway costs the device a re-sync of up to a minute of events it already has, which clients already ignore. `TestAckIsRememberedPerDevice` reads the cursor back from a second connection while the first stays open, so the conformance runs set a short interval; a new test closes the connection and expects the cursor at once. Measure again at 500 and 1000 messages per second; the load generator acks once a second per device, so the saving shows directly.

### 2. Database ceiling

**Why:** above about 500 messages per second the shared database limits every backend on a laptop. It is the Vitess layer in one process: at 1000 messages per second, about 3400 transactions and 15000 statements per second, vtcombo uses about 2.7 cores and mysqld 1.7, and the fan-out consumes about 700 records per second whatever its pool size or concurrency. Disk, Debezium and Kafka were ruled out ([history](docs/history.md)). Until it moves, no backend can be compared above 500 messages per second.

**Plan**, cheapest first, measuring after each step:

1. Coalesced acks (item 1).
2. Fewer statements per fan-out append: one multi-row insert into `user_event` and one update of `user_conversation` per recipient shard and batch of records, instead of statements per recipient and record, in Go and Java.
3. A plain MySQL profile of the infrastructure (`make infra-up PROFILE=mysql`): the same schema unsharded on one MySQL, with Debezium's MySQL connector reading its binlog. It measures Vitess's share, gives the backends headroom, and is the deployment the managed clouds offer ([docs/production.md](docs/production.md)). It also removes the stranded-offsets problem below.
4. A real Vitess topology, vtgate and a vttablet with its own mysqld per shard, as separate processes, when the question is sharding rather than the backends.

### 3. Retention and history in cold storage

**Why:** the capacity estimates assume two weeks of history on the server ([docs/capacity.md](docs/capacity.md)); the prototype keeps everything, and clients assume they can scroll back. Keeping history for good makes storage grow without bound: at the estimated 1.7 billion messages a day and 300 bytes each, the conversation store grows by about 0.5 TB a day, and the user logs, a copy per participant, by about 1 TB more. On MySQL shards of 100 to 250 GB that is several new shards a day, holding rows that are almost never read.

**Decision:** keep message history for good, in a cold tier; keep the user logs short. The log is the delivery mechanism, not the archive: a device that has been away longer than the log's retention rebuilds from the conversations, which hold the history. That removes the per-participant copy, about half of the storage, from what is kept for good.

**Not replacing MySQL with HBase.** HBase is atomic per row only. The send path locks the conversation's row, checks membership and the key epoch, takes the next sequence number and inserts the message and the outbox row in one transaction; the claims for names, pairs and groups rely on unique keys; contacts, devices and keys are small mutable rows read by primary key. Each would need redesigning around single-row check-and-mutate, and the change-data path, which reads the outbox from the binlog in commit order, has no HBase counterpart in Debezium, so the fan-out guarantee would have to be rebuilt as a relay, which [the architecture](docs/architecture.md#choices) rejects. What HBase is good at, cheap appends and range scans of immutable rows by key, is exactly the cold history, so it goes there.

**Why HBase for the cold tier.** Old messages still change: a delete leaves a tombstone, reactions are replaced, and editing is planned; a store with point updates by key handles that, where immutable segments in object storage would need rewriting. The HBase API is also Bigtable's, a managed path on Google Cloud. Cassandra or ScyllaDB would fit the same role; the tier sits behind one interface in each services implementation, so the choice can be revisited.

**Plan:**

1. **Bounded user logs.** A contract change: Sync with a cursor older than the log's oldest retained event fails with a new error code (`ERROR_CODE_CURSOR_EXPIRED`), and the client rebuilds: ListConversations, then GetMessages for what it shows. A trimmer in the services deletes events older than the retention (30 days by default) in batches per shard, keeping `user.log_tail`. Go and Java services, the web, iOS and Go clients, and a conformance test that trims a log and expects the error.
2. **HBase in the infrastructure.** One standalone HBase container (local filesystem, no HDFS, a memory cap like the others), a table `message` with the row key `conversation_id ‖ seq` (16 bytes and 8 bytes big-endian, so a conversation's history is one contiguous range and random conversation ids spread regions), pre-split on the id's first byte; a column family for the message (sender, client id, epoch, sealed content, blob ids, deleted) and one for reactions, a column per user.
3. **A watermark per conversation.** `conversation.cold_seq`: messages up to it are in HBase, those after it in MySQL. A mover in the services takes conversations whose oldest MySQL message is older than the hot window (30 days by default): under the conversation's row lock it writes the messages and their reactions up to the new watermark to HBase (idempotent puts), raises `cold_seq`, deletes the rows and commits. A failure after the puts leaves duplicates of rows still in MySQL, which the next run overwrites with the same values.
4. **Reads and changes across the watermark.** GetMessages reads MySQL down to `cold_seq + 1` and continues in HBase below it, in one response. Delete, react and (later) edit lock the conversation's row as now; for a seq at or below `cold_seq` they write the HBase row instead of the MySQL one, and the outbox row is written in MySQL as before, so the fan-out and the events are unchanged. Keys, membership and read marks stay in MySQL; they are small.
5. **Account deletion and blobs.** A deleted account's messages stay with the conversations, as now. Attachments follow their messages: blobs of cold messages stay in object storage, so their retention is a separate decision, left for a public deployment.
6. **Go and Java.** gohbase in Go, the shaded HBase client in Java; the Elixir and Rust gateways are untouched. Unit tests for the key layout and the watermark rules in both; conformance tests run with a hot window of seconds, so they cross the watermark: history pages across it, and deletes and reactions on cold messages.
7. **Measure** GetMessages latency above and below the watermark, the mover's throughput, and the database's statements per message with the logs trimmed.

### 4. Stray blobs and the Vitess restart

**Why:** both leave a running stack in a state only `make infra-reset` fixes, and both are small.

- **Attachment uploads that stay used.** A send releases its uploads on any failure after marking them used, but the release itself can fail, and a retried `DeleteMessage` has nothing to release, since the delete erases the blob ids from the row; in both cases the sweeper never deletes the blobs. Keep the blob ids on the tombstone and hide them in reads, so a retried delete releases them again; have the sweeper also take used uploads whose message is a tombstone. Go and Java.
- **The change-data path does not survive a restart of the Vitess container.** The test server starts an empty database whenever its container restarts, and Debezium's stored offsets then point into history that no longer exists: both connectors retry forever ("failed to find a PRIMARY tablet for VStream"), and every conformance run fails at the readiness probe with "no event delivered". `make infra-up` checks the connectors' task state and, on that failure against an empty database, resets their offsets and restarts them. The plain MySQL profile (item 2) has no such problem.
- **Load-generator attachments are never deleted**, so blob storage grows with each run. The load generator deletes the messages it sent with attachments after the grace, outside the window, which releases the blobs through the normal path.

### 5. Contract clean-up

- **`MessageFailedEvent`**: drop it. Sends are answered synchronously and the fan-out retries until an append succeeds, so there is no asynchronous failure to report and no backend produces it ([Connect and sync](docs/architecture.md#connect-and-sync)). Reserve its field number and name so they are not reused.

### 6. Parity

- **Pushes from the Java fan-out.** The Java backend stores push tokens but sends nothing to APNs, so in a pool of Go and Java services instances the partitions a Java instance consumes get no notifications. Port Go's notifier: the provider token, the payload with the sealed content, presence and badge, and clearing rejected tokens; Java's HTTP client speaks HTTP/2.
- **Prometheus scrapes only port 8080.** Add the side-by-side processes' and the cluster's ports as scrape targets, labeled by process, so the dashboards cover every shape.
- **Images are not built by `make ci`**, to keep it quick; a broken Dockerfile shows at `make cluster-up`. A separate `make ci-images` that builds them all, run before pushes that touch a Dockerfile or a backend's build.

### Features

Not ordered; each is an issue or becomes one when started.

- **Message editing:** a replacement body by the same path as reactions, one sealed edit per message replacing the last, with the edit's own event. After item 3, so an edit of a cold message is designed with it. Also deleting only on one's own devices.
- **Stickers:** packs and the sticker editor (#8); animated stickers (GIF and WebP upload, but clients redraw them as still images); moderation, reporting and takedown.
- **Attachments:** video thumbnails and playback; downloads served through a CDN.
- **Public channels (#9):** rooms with thousands of readers need a read path per room instead of a log append per member; the cold tier's history by conversation is that read path for everything but the newest messages.
- **Encryption:** no forward secrecy between rotations, and a stolen device key reads every epoch it was given; MLS (RFC 9420) would add both, with a library on each client. A new device learns that history was shared only when it opens a conversation or reconnects; a notice event would let it fetch at once. The web client needs a secure context (HTTPS) for WebCrypto, which `make web` now provides on the network.
- **Presence after a Valkey restart:** devices that stayed connected have no presence entry until they reconnect, so they may get notifications while connected. Gateways rewrite their devices' entries when they reconnect to Valkey.

### For a public deployment

- **Limits per user across devices and per address**, including sign-in attempts per account and per address, need shared state (Valkey, or the balancer's stick tables); with password recovery ([docs/production.md](docs/production.md)).

### Experiments

- **Services in Elixir and Rust.** Left out because the services are SQL and a Kafka consumer, where the languages differ least; a gateway is where they differ most. A full implementation in each would complete the matrix, and would grow with item 3.
- **A second Go gateway without a goroutine per connection**, on a poller-driven WebSocket library, to see how far the idle memory per connection falls when the model changes; see [docs/results/2026-10-10-idle-memory.md](docs/results/2026-10-10-idle-memory.md).

## Known gaps

Behavior that is understood and accepted for now; each would be a change to a flow, not a fix.

- A conversation list entry the fan-out creates while the peer renames may keep the old name until the peer's next change: the entry reads the peer's profile when it is created, and the rename's fan-out pages through the lists that exist when it runs.
- Profile changes reach direct partners only; a group's member names refresh when a client opens the group.
- A user deleted right after being added to a group, before the fan-out has put the group in their list, stays a member.
- No push notification to a message's sender for a reaction: the services cannot say what the reaction was.
- A key wrapped by a device that has since signed out, and whose key a device never saw, is refused as unverifiable.
- The services balancer checks an instance by connecting to its RPC port: a stopped instance drops out, a hung one does not.
- Hashing a password takes about 19 MiB and tens of milliseconds, so the services run at most as many hashes at once as they have cores; a burst of sign-ins, as the load generator makes before a run, raises the services' resident memory, and measurements force a garbage collection before sampling.
