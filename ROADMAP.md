# Roadmap

What is done, what is open and why, and the gaps that are known and accepted for now. How it got here, the questions settled on the way and the figures since superseded are in [docs/history.md](docs/history.md); what a public deployment would need, and its cost, in [docs/production.md](docs/production.md). Features are tracked as issues; the number follows the name.

## Done

- **Contract and infrastructure.** The Protobuf contract for devices (`proto/wrinkle/v1`) and between gateway and services (`proto/wrinkle/backend/v1`); SQL schemas and two Vitess keyspaces; Docker Compose with Vitess, Kafka, Debezium, Valkey, Garage, Prometheus and Grafana; the outbox streamed from the binlog, each row deleted in the transaction that inserts it; `make ci` as the local gate. [Architecture: Processes](docs/architecture.md#processes), [Data model](docs/architecture.md#data-model).
- **Backends.** Go, Java and TypeScript implement everything, as a monolith or split into gateways and services instances over the internal RPC; Elixir and Rust implement the gateway and run beside any of those services. All five pass the conformance tests. [Implementations](docs/architecture.md#implementations).
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
- **Push notifications.** `RegisterPushToken` in every backend; APNs from the Go, Java and TypeScript fan-out to devices that are not connected, with the sealed content for the iOS notification service extension to decrypt. [Sending a message](docs/architecture.md#sending-a-message).
- **Measurements.** Every shape measured on the current code, three repetitions each: five of eight sustain 1000 messages per second end to end encrypted, the Go, TypeScript and Elixir gateways 500, and the database sets the limit above 1000. [docs/results/2026-10-24-shapes.md](docs/results/2026-10-24-shapes.md).
- **Coalesced acks.** Every gateway writes a connection's acks at most once a minute, and at once when the connection ends. [Acks](docs/architecture.md#acks).
- **TypeScript backend.** All three roles on Node 26, as a monolith or split, with batched fan-out and APNs push; in the cluster and the side-by-side setup with the others. [backends/ts](backends/ts/README.md).
- **History in a cold tier.** Messages older than 30 days move to HBase per conversation behind a watermark; history, reactions and deletes work across it in Go, Java and TypeScript. [History and the cold tier](docs/architecture.md#history-and-the-cold-tier).
- **Background work per shard.** The log trimmer and the history mover run each shard on its own loop, every minute by default, each shard's round leased in Valkey to one instance at a time, so services instances share the shards. [Processes](docs/architecture.md#processes).
- **`make ci-images`**: builds every image, the cluster's and the stack's, before a push that touches a Dockerfile or a backend's build; `make ci` still builds none, to stay quick.
- **Monitoring every process.** Prometheus scrapes the process on port 8080, the side-by-side gateways and services instances, and every replica in the cluster, found by name, each labeled by process; the dashboard shows them by process, with the per-shard jobs and the cold tier's reads. Metrics for the jobs and the cold tier's reads in Go, Java and TypeScript ([docs/metrics.md](docs/metrics.md)).
- **Stray blobs.** Attachment uploads are settled by the fan-out, which handles every committed message and deletion at least once: a send's uploads stay pending until their message is delivered, and the sweeper deletes ones still pending a day later; a deletion's outbox row names the uploads the fan-out releases. The load generator deletes its messages with attachments after each run. [Attachments](docs/architecture.md#attachments).
- **The change stream after a Vitess restart.** The test server starts an empty database, with a new server id, whenever its container restarts, which stranded Debezium's stored positions; `make infra-up` now resets them when they name a server that no longer exists, and the connectors start from the current position. [Troubleshooting](docs/local-network.md#troubleshooting).
- **Contract clean-up.** `MessageFailedEvent`, which nothing produced, dropped; its field number and name reserved.
- **Bounded user logs.** The user logs keep events for 30 days, and a device whose cursor is older starts again from the conversations; the history stays with them. [Connect and sync](docs/architecture.md#connect-and-sync).

## Open

In the order they would be done. Each item says why it comes where it does and how it would be built; the features and experiments after the numbered items are not ordered.

### 1. Database ceiling

**Why:** the shared database limits every backend on a laptop. It is the Vitess layer in one process: before steps 1 and 2 below, at 1000 messages per second, about 3400 transactions and 15000 statements per second, vtcombo used about 2.7 cores and mysqld 1.7. Disk, Debezium and Kafka were ruled out ([history](docs/history.md)). After steps 1 and 2 the limit moved from between 500 and 1000 messages per second to between 1000 and 1500 ([2026-10-21](docs/results/2026-10-21-shapes.md), [2026-10-24](docs/results/2026-10-24-shapes.md)); until it moves further, no backend can be compared above 1000.

**Plan**, cheapest first, measuring after each step:

1. Coalesced acks (done): 0.1 ack writes per message remain in a diagnostic count at 500 messages a second, and the gateways use 16 to 23 % less CPU per message.
2. Fewer statements per fan-out append (done): a message's copies are stored in one transaction per recipient shard with whatever else is queued for it, with multi-row statements. At 500 messages a second the fan-out's statements per message fell from about 12 to 2, and the database's from 24 to 14, in a diagnostic count. With step 1, the highest sustained rate doubled to 1000 messages a second.
3. A plain MySQL profile of the infrastructure (`make infra-up PROFILE=mysql`; built, not yet measured): the same schema unsharded on one MySQL 8.4, a database per keyspace, with Debezium's MySQL connector reading its binlog into the same topics. The backends need no change beyond connecting as `root`: without vtgate each keyspace is one shard. Every shape and both clients pass `make ci` on it. Next, `make measure-all` on it, labeled `-mysql`, against the 2026-10-24 Vitess results: it measures Vitess's share, gives the backends headroom, and is the deployment the managed clouds offer ([docs/production.md](docs/production.md)).
4. A real Vitess topology, vtgate and a vttablet with its own mysqld per shard, as separate processes, when the question is sharding rather than the backends.

### 2. Retention and history in cold storage

**Why:** the capacity estimates assume two weeks of history on the server ([docs/capacity.md](docs/capacity.md)); the prototype kept everything, and clients assume they can scroll back. Keeping history for good makes storage grow without bound: at the estimated 1.7 billion messages a day and 300 bytes each, the conversation store grows by about 0.5 TB a day, and the user logs, a copy per participant, by about 1 TB more. On MySQL shards of 100 to 250 GB that is several new shards a day, holding rows that are almost never read.

**Decision:** keep message history for good, in a cold tier; keep the user logs short. The log is the delivery mechanism, not the archive: a device that has been away longer than the log's retention rebuilds from the conversations, which hold the history. That removes the per-participant copy, about half of the storage, from what is kept for good.

**Not replacing MySQL with HBase.** HBase is atomic per row only. The send path locks the conversation's row, checks membership and the key epoch, takes the next sequence number and inserts the message and the outbox row in one transaction; the claims for names, pairs and groups rely on unique keys; contacts, devices and keys are small mutable rows read by primary key. Each would need redesigning around single-row check-and-mutate, and the change-data path, which reads the outbox from the binlog in commit order, has no HBase counterpart in Debezium, so the fan-out guarantee would have to be rebuilt as a relay, which [the architecture](docs/architecture.md#choices) rejects. What HBase is good at, cheap appends and range scans of immutable rows by key, is exactly the cold history, so it goes there.

**Why HBase for the cold tier.** Old messages still change: a delete leaves a tombstone, reactions are replaced, and editing is planned; a store with point updates by key handles that, where immutable segments in object storage would need rewriting. The HBase API is also Bigtable's, a managed path on Google Cloud. Cassandra or ScyllaDB would fit the same role; the tier sits behind one interface in each services implementation, so the choice can be revisited.

**Plan:**

1. **Bounded user logs** (done). Sync with a cursor before the log's tail fails with `ERROR_CODE_CURSOR_EXPIRED`, and the device starts again from Connect's log head, listing its conversations and contacts again. A trimmer in Go, Java and TypeScript drops events older than the retention (30 days by default) a page at a time per shard, raising `user.log_tail` in the transaction that deletes them; the web and iOS clients start over on the error; a conformance test, with a retention of 20 seconds, expects it for the readiness probe's cursor. [Connect and sync](docs/architecture.md#connect-and-sync).
2. **HBase in the infrastructure** (done). HBase 2.6 standalone in the stack (`infra/hbase/`: master, region server and ZooKeeper in one JVM on the local filesystem, 512 MB of heap in a 1 GB cap). The region server advertises `hbase.localhost`, an alias of the container on the network and loopback on the host, where its ports are published, so backends on the host and in the cluster both reach it with ZooKeeper at `hbase.localhost:2181`. The table `message` ([schema/hbase/schema.rb](schema/hbase/schema.rb), created by `make infra-up`) has the row key `conversation_id ‖ seq` (16 bytes and 8 bytes big-endian, so a conversation's history is one contiguous range and random conversation ids spread the rows), 16 regions split on the id's first byte; family `m` holds a cell per column of the message table and one with the message's reactions. `make smoke` round-trips a row. HBase's REST server runs beside it, for TypeScript.
3. **A watermark per conversation** (done). `conversation.cold_seq`: messages up to it are in HBase, those after it in MySQL. A mover in each full backend, beside the log trimmer, pages through each shard by `sent_at` (index `message_age`) and moves each conversation's messages older than the hot window (30 days by default), 200 a transaction: it puts them and their reactions to HBase, then under the conversation's row lock rereads them, puts again any that changed meanwhile, raises `cold_seq` and deletes the rows. A failure after the puts leaves copies that the next move puts again.
4. **Reads and changes across the watermark** (done). GetMessages reads MySQL, then the watermark, then HBase below the lowest seq MySQL gave, in one response; a page the watermark passed meanwhile is read again. Delete and react lock the conversation's row as before and, for a seq at or below `cold_seq`, rewrite the HBase row before the commit, putting it back if the transaction fails; the outbox row is written in MySQL as before. A resend is recognized, and a group read event finds the senders, only within the hot window. [History and the cold tier](docs/architecture.md#history-and-the-cold-tier).
5. **Account deletion and blobs.** A deleted account's messages stay with the conversations, as now. Attachments follow their messages: blobs of cold messages stay in object storage, so their retention is a separate decision, left for a public deployment.
6. **Go, Java and TypeScript** (done). gohbase in Go; in Java the shaded client 2.6.7 built on Hadoop 2.10, with its own user, since Hadoop's lookup of the current one fails on JDK 25 and the Hadoop 3 build replaces the JVM's DNS resolver; TypeScript through the REST server, whose scanners read forward only, which works because a conversation's seqs in HBase have no gaps. Unit tests for the key layout and the watermark rules in each; a conformance test with a hot window of 20 seconds pages history across the watermark, and reacts to and deletes cold messages, checking HBase that they moved.
7. **Measure** GetMessages latency above and below the watermark, the mover's throughput, and the database's statements per message with the logs trimmed. The protocol's history series ([docs/measuring.md](docs/measuring.md), from 2026-10-26) covers the first two, with the metrics of the jobs and the cold tier's reads; not yet run on an idle machine.

### 3. The fan-out's blast radius

**Why:** a fan-out worker handles a fetch's conversations in parallel, then commits every partition only if all succeeded; one record that fails its retries sends the whole fetch back. With a user shard down, almost every fetch holds a record for it, so the worker delivers to no one: sharding the users does not contain an outage of one shard. Found in the 2026-10-26 review.

**Design:** park instead of retry. A record whose appends fail for one user shard after the retries has those recipients' appends produced to a parked topic, keyed by recipient, and the rest of the fetch commits. Later records for a parked recipient go to the parked topic too, so each recipient's log keeps its order; a consumer of the parked topic retries until the shard answers, and a recipient leaves the parked set once its parked records are drained. The parked set must survive a worker restart (a compacted topic or a Valkey set), or a later record could reach a log before an earlier parked one. The log's source key already makes every replay idempotent. Go first, then Java and TypeScript; a failure drill (stop one user shard under load, with the load generator counting per-device gaps and order) shows it.

### 4. Parity

- **The Elixir gateway at 1000 messages per second.** The other three gateways delivered everything at 1000/s in front of the same Go services; the Elixir gateway timed out about 5 % of its sends ([2026-10-21](docs/results/2026-10-21-shapes.md)). Profiled on 2026-10-22: no process in the gateway queues, and the gRPC client's own queue stays empty; the sends wait in the services for a connection to the conversation keyspace, as they do for every shape past the limit, and the database work per message is the Go gateway's. On a busy machine, from fresh stacks, even the Go gateway failed at 1000/s, and the Elixir gateway failed worse; turning off the BEAM schedulers' busy waiting, now the default in its launchers, closed part of that. Measured again in one session on an idle machine ([2026-10-24](docs/results/2026-10-24-shapes.md)), it delivered 98.3 % at 1000/s, and the Go gateway delivered everything but with a median p99 of 1.6 s; their send round trips, 1.5 s and 0.3 s at the median against the Rust gateway's 59 ms in front of the same services, put the wait on the gateways' side of the RPC. Open: where on that side.
- **The TypeScript gateway at 1000 messages per second.** It delivered everything, but its send round trip rose to 1.2 s at the median, while the TypeScript monolith answered in 50 ms ([2026-10-24](docs/results/2026-10-24-shapes.md)). Open: whether the internal RPC on Node's gRPC or the services process's one thread, which serves every gateway's calls and is not measured, holds the sends.


### Features

Not ordered; each is an issue or becomes one when started.

- **Message editing:** a replacement body by the same path as reactions, one sealed edit per message replacing the last, with the edit's own event. After item 2, so an edit of a cold message is designed with it. Also deleting only on one's own devices.
- **Stickers:** packs and the sticker editor (#8); animated stickers (GIF and WebP upload, but clients redraw them as still images); moderation, reporting and takedown.
- **Attachments:** video thumbnails and playback; downloads served through a CDN.
- **Public channels (#9):** rooms with thousands of readers need a read path per room instead of a log append per member; the cold tier's history by conversation is that read path for everything but the newest messages.
- **Encryption:** no forward secrecy between rotations, and a stolen device key reads every epoch it was given; MLS (RFC 9420) would add both, with a library on each client. A new device learns that history was shared only when it opens a conversation or reconnects; a notice event would let it fetch at once. The web client needs a secure context (HTTPS) for WebCrypto, which `make web` now provides on the network.
- **Presence after a Valkey restart:** devices that stayed connected have no presence entry until they reconnect, so they may get notifications while connected. Gateways rewrite their devices' entries when they reconnect to Valkey.

### For a public deployment

- **Limits per user across devices and per address**, including sign-in attempts per account and per address, need shared state (Valkey, or the balancer's stick tables); with password recovery ([docs/production.md](docs/production.md)).

### Experiments

- **Services in Elixir and Rust.** Left out because the services are SQL and a Kafka consumer, where the languages differ least; a gateway is where they differ most. A full implementation in each would complete the matrix, and would grow with item 2.
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
