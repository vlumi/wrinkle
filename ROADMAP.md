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
- **Push notifications.** `RegisterPushToken` in every backend; APNs from the Go fan-out to devices that are not connected, with the sealed content for the iOS notification service extension to decrypt. [Sending a message](docs/architecture.md#sending-a-message).

## Open

### Backends

- **Services in Elixir and Rust.** Left out because the services are SQL and a Kafka consumer, where the languages differ least; a gateway is where they differ most. A full implementation in each would complete the matrix.
- **Pushes from the Java fan-out.** The Java backend stores push tokens but sends nothing to APNs, so in a pool of Go and Java services instances the partitions a Java instance consumes get no notifications.
- **A second Go gateway without a goroutine per connection**, on a poller-driven WebSocket library, to see how far the idle memory per connection falls when the model changes; see [docs/results/2026-10-10-idle-memory.md](docs/results/2026-10-10-idle-memory.md).

### Contract

- **Coalesced acks.** The design forwards a device's acks at most once a minute per device; every gateway writes them as they come, one write at a time per connection, keeping only the highest cursor that arrived meanwhile. Acks are about 30 % of the database's transactions (see Database ceiling).
- **`MessageFailedEvent` has no producer.** Sends are answered synchronously and the fan-out retries until an append succeeds, so there is no asynchronous failure to report, and no backend produces it ([Connect and sync](docs/architecture.md#connect-and-sync)). Drop it from the contract, or give it a producer when one appears.

### Features

- **Messages:** editing (a replacement body by the same path), and deleting only on one's own devices.
- **Stickers:** packs and the sticker editor (#8); animated stickers (GIF and WebP upload, but clients redraw them as still images); moderation, reporting and takedown.
- **Attachments:** video thumbnails and playback; downloads served through a CDN.
- **Public channels (#9):** rooms with thousands of readers need a read path per room instead of a log append per member.
- **Encryption:** no forward secrecy between rotations, and a stolen device key reads every epoch it was given; MLS (RFC 9420) would add both, with a library on each client. A new device learns that history was shared only when it opens a conversation or reconnects; a notice event would let it fetch at once. The web client needs a secure context (HTTPS) for WebCrypto, so a page served over plain HTTP on the network cannot send.
- **Presence after a Valkey restart:** devices that stayed connected have no presence entry until they reconnect, so they may get notifications while connected. Gateways could rewrite their devices' entries when they reconnect to Valkey.

### Operations

- **Limits per user across devices and per address**, including sign-in attempts per account and per address, need shared state (Valkey, or the balancer's stick tables); with password recovery, left for a public deployment ([docs/production.md](docs/production.md)).
- **The change-data path does not survive a restart of the Vitess container.** The test server starts an empty database whenever its container restarts (rebuilding the image after a change under `infra/vitess/` does it; so does Docker Desktop restarting), and Debezium's stored offsets then point into history that no longer exists: both connectors retry forever ("failed to find a PRIMARY tablet for VStream"), no event is delivered, and every conformance run fails at the readiness probe with "no event delivered". `make infra-reset` is the fix. `make infra-up` could detect the state (a connector task failing on VStream against an empty database) and say so, or reset the connectors' offsets itself.
- **Attachment uploads that stay used.** A send releases its uploads on any failure after marking them used, but the release itself can fail, and a retried `DeleteMessage` has nothing to release, since the delete erases the blob ids from the row; in both cases the sweeper never deletes the blobs. The fix keeps the blob ids on the tombstone and hides them in reads, in both backends.
- **Load-generator attachments are never deleted**, so blob storage grows with each run until `make infra-reset`.
- **Images are not built by `make ci`**, to keep it quick; a broken Dockerfile shows at `make cluster-up`.
- **Prometheus scrapes only port 8080.** Neither the side-by-side processes nor the cluster are scraped; the monitoring page links the native processes' metrics.

### Database ceiling

Above about 500 messages per second the shared database limits every backend on a laptop. It is the Vitess layer in one process: at 1000 messages per second, about 3400 transactions and 15000 statements per second, vtcombo uses about 2.7 cores and mysqld 1.7, and the fan-out consumes about 700 records per second whatever its pool size or concurrency. Disk, Debezium and Kafka were ruled out ([history](docs/history.md)). Options, cheapest first:

- Fewer statements per message: coalesce acks in the gateways (above) and fold the fan-out's statements.
- A plain MySQL profile of the infrastructure, same schema, no sharding, to measure Vitess's share and give the backends headroom.
- A real Vitess topology, vtgate and a vttablet with its own mysqld per shard, as separate processes.

### Measurements

- **Re-measure every shape** after encryption, groups, reactions, stickers and attachments: the results in [docs/results/](docs/results/) predate them (commit 0b06d92), and the figures quoted in the README, the architecture, the capacity page and the backend READMEs come from those runs. The write-up for a public repository follows the re-measurement.

## Known gaps

Behavior that is understood and accepted for now; each would be a change to a flow, not a fix.

- A conversation list entry the fan-out creates while the peer renames may keep the old name until the peer's next change: the entry reads the peer's profile when it is created, and the rename's fan-out pages through the lists that exist when it runs.
- Profile changes reach direct partners only; a group's member names refresh when a client opens the group.
- A user deleted right after being added to a group, before the fan-out has put the group in their list, stays a member.
- No push notification to a message's sender for a reaction: the services cannot say what the reaction was.
- A key wrapped by a device that has since signed out, and whose key a device never saw, is refused as unverifiable.
- The services balancer checks an instance by connecting to its RPC port: a stopped instance drops out, a hung one does not.
- Hashing a password takes about 19 MiB and tens of milliseconds, so the services run at most as many hashes at once as they have cores; a burst of sign-ins, as the load generator makes before a run, raises the services' resident memory, and measurements force a garbage collection before sampling.
