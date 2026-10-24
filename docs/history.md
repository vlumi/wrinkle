# History

A dated log of how Wrinkle got to where it is: the phases, the questions settled on the way, and the figures since superseded. Each phase left something that ran on its own. [ROADMAP.md](../ROADMAP.md) has what is done and open now; the results files in [results/](results/) have the measurements in full.

## 2026-10-10

**Contract and infrastructure (phase 1).** The Protobuf contract: WebSocket frames (request, response, event, ack, ping), requests and responses, the event types. SQL schemas and Vitess keyspaces: conversations by conversation id, users by user id, with vschemas. Docker Compose: Vitess, Kafka (KRaft, single broker), Debezium on Kafka Connect, Valkey; heaps capped so the whole stack fit in about 4 GB (Garage, Prometheus and Grafana came later, and the README now asks for 8 GB). A smoke test of the change-data path: an outbox insert appears on the Kafka topic. `make ci` as the local CI entry point, to which each later phase added its checks (format, lint, unit tests, conformance against every backend).

- Vitess publishes x86 images only ([vitessio/vitess#17225](https://github.com/vitessio/vitess/issues/17225), [#15034](https://github.com/vitessio/vitess/issues/15034)). `infra/vitess/` builds `vttestserver` natively from the Vitess sources at a release tag, with MySQL 8.4 LTS from the multi-arch `mysql` image since there are no arm64 MySQL packages for Debian. The first build takes a few minutes; Compose caches it.
- Debezium's Vitess connector streams from `vttestserver` (vtcombo) over VStream. Its initial snapshot, a VStream copy, fails against vtcombo with duplicate BEGIN events, so the connector runs with `snapshot.mode=never`; the outbox only needs streaming. `BIGINT UNSIGNED` columns arrive as decimal strings in the JSON records.

**Go backend (phase 2).** One binary, roles chosen by configuration: gateway, conversation service, sync service, fan-out worker. The conformance tests in `conformance/`, run against any backend through the contract.

- The roles are `gateway`, `services` and `fanout`. By default the gateway runs the conversation and sync services in-process; with `--services` it forwards to a `services` process over the internal RPC added with the Elixir and Rust gateways.
- Push goes from fan-out workers to gateways through Valkey pub/sub, one channel per user; a gateway subscribes to a user's channel while it holds one of that user's connections. The presence hash (device to node) tells the push notifier which devices are not connected.
- Ids were derived, not allocated: user id from the name, conversation id from the user pair, both within 63 bits, so sign-in and opening a conversation needed no lookup table. Replaced by random UUIDs with accounts on 2026-10-11.
- `MessageFailedEvent` is in the contract but nothing produces it; the synchronous path has no asynchronous failure to report. Still open in the roadmap.

**Web client (phase 3).** Sign in by name, conversation list, conversation view, read marks, connection state, as one page of plain JavaScript. The browser test drives it in headless Chrome in `make ci`.

**Load generator and metrics (phase 4).** A load generator in Go: simulated devices, configurable connection count, message rate, devices per user. Prometheus and Grafana in Compose; the same metric names in every backend. The measurement protocol in `docs/measuring.md`: warm-up, run length, repetitions, how results are recorded (date, commit, machine, configuration); `loadgen/protocol.sh` runs it, `docs/results/` holds the write-ups with the raw JSON lines, metric names are in `docs/metrics.md`. First results for the Go backend.

- The first run found the fan-out worker serial and the protocol leaking Kafka backlog between runs; both fixed the same day. The second run: 500/s sustained with 66 ms p50 end to end, about 46 KiB per idle connection ([2026-10-10-go.md](results/2026-10-10-go.md)).
- Idle memory scatters by about 20 MiB between runs; quote medians of repetitions (`REPS=3`).

**Java backend (phase 5).** Same contract and conformance tests. Java 25 (virtual threads without pinning on `synchronized`), Netty and Armeria. Go vs. Java comparison.

- Armeria's WebSocket service over Netty; request handling and fan-out on virtual threads; HikariCP over Connector/J, the Kafka consumer, Lettuce for Valkey. Gradle with a JDK 25 toolchain, google-java-format through Spotless as the format check, `-Xlint:all -Werror`.
- Ids were computed by an own xxhash64 matching the Go backend bit for bit, so both backends ran on one database; since accounts, ids are random UUIDs and the shared vectors are the password and token formats.
- The JVM exporter reports no resident memory on macOS; the load generator reads it with `ps`.
- First comparison in [2026-10-10-go-vs-java.md](results/2026-10-10-go-vs-java.md): both sustain 500/s, Go's tail is tighter, baselines differ by 200 MiB, CPU per message within 10 %. The per-connection memory figures need repetitions and a fixed JVM heap before they mean anything.

**Idle memory per connection.** After tuning, Go 29 KiB and Java 12 KiB live ([2026-10-10-idle-memory.md](results/2026-10-10-idle-memory.md)). A Go gateway on gws measured a tenth less live memory; coder/websocket was kept as the mainstream, maintained choice. A poller-driven Go gateway stays an open experiment.

**iOS client (phase 6).** SwiftUI, `URLSessionWebSocketTask`, SwiftProtobuf; local storage, so a device syncs only what it is missing.

- The logic is a Swift package, `clients/ios/Packages/WrinkleCore`: `WrinkleCore` has the generated contract, the connection actor and the SwiftData store; `WrinkleKit` the session and the screens. `Sources/iOS` is the shell. The Xcode project is generated by xcodegen from `project.yml` and not committed, in the style of the sibling SwiftUI projects.
- The package compiles for the Mac too, so `swift test` runs headless: store tests, and a backend test over a real connection to `localhost:8080`. `make ios-test` runs them against the Go backend; `make build-ios` builds the app unsigned for the simulator; both are in `make ci`. `make ios-lint` is swiftlint and swift-format, strict.
- iOS 26 is the floor. The device keeps its device id and cursor per user in UserDefaults and conversations and messages in SwiftData; after a reconnect it syncs from the cursor and acks.
- `make ios-sim` builds, installs and launches the app in an iPhone simulator, which reaches the host's backend as `localhost`; `make ios-device` does the same on a paired iPhone, signed with the team in `.env`.

**License.** MIT.

## 2026-10-11

**Elixir and Rust (phase 7).** A gateway first in each, since connection handling is where they differ most; the full backend if worthwhile (still open). Four-way comparison: three gateways and two monoliths.

- An internal RPC between gateway and services (`proto/wrinkle/backend/v1`): the gateway forwards a device's request with its identity and gets back the response as it goes to the device; pushes keep going through Valkey. The Go and Java backends have a `services` role and a forwarding mode for their gateways (`split.sh` in each runs the pair for conformance and measurement). The Elixir and Rust implementations are gateways only, run beside Go or Java services (`run.sh` in each runs one beside a Go services process for conformance and measurement).
- Elixir: Bandit, a process per connection, requests in a task each, elixir-grpc over Mint. Rust: axum over tokio, a task per connection, tonic. Both pass the conformance tests.
- Comparison in [2026-10-11-gateways.md](results/2026-10-11-gateways.md): all three gateways within 15 ms at p50; Rust held 4000 idle connections in 78 MiB against the Go gateway's 167 (and 22 against 90 MiB under load at 500/s), with 60 % of its CPU per message and the tightest tail; Elixir's per-connection memory the highest, its baseline above 100 MiB.
- Two protocol flaws found: the stack degrades as it grows (1.4 s against 94 ms p50 at 500/s), so each backend now runs from a fresh stack; and grpc-go's default of 100 concurrent streams per connection broke the Elixir client under a connect storm.
- The Vitess container saturates between 500 and 1000 messages per second on this machine, so backends can only be compared below that.

**Web client rewritten.** In React and TypeScript with Vite, after the plain JavaScript version proved hard to use on a phone: one pane at a time on narrow screens, both side by side on wide ones. The contract is generated by buf with protoc-gen-es, so ids are `bigint`; Zustand holds the session; `server.mjs` serves the build with no dependencies. `make web-test` drives the built app in headless Chrome over the DevTools protocol: two users in separate browser contexts, one at phone width, loaded from a host name that is not a secure context, as on a LAN. Vitest covers the conversation model; ESLint and `tsc` the rest. All of it is in `make ci`.

**Database ceiling investigated.** Above about 500 messages per second the shared database limits every backend on a laptop.

- Not disk: the test server commits without fsync. The InnoDB buffer pool was the test server's 32 MB, which a day of runs outgrew; it is 512 MB now, and sends keep up at 1000 per second.
- Not Debezium or Kafka: the topic keeps pace with outbox inserts.
- It is the Vitess layer in one process: at 1000 messages per second, about 3400 transactions and 15000 statements per second, vtcombo uses about 2.7 cores and mysqld 1.7, and the fan-out consumes about 700 records per second whatever its pool size or concurrency. The options are in the roadmap.

**Push notifications.** `RegisterPushToken` in the contract, every backend storing the token; APNs from the Go fan-out to devices that are not connected; the iOS app registers, closes its connection in the background and opens a tapped conversation. Presence entries name the gateway process holding the device, and each process keeps a key with a 90 s expiry alive while it runs; the push notifier ignores entries of processes whose key has expired, so devices of a gateway that died get pushes again within 90 s. The Java fan-out sends no pushes (open).

**Scaling out.** Side by side, the shape the design scales in: services instances and gateways in any number and language behind two HAProxy balancers (`make lb-up`). Devices' connections go to the gateway with the fewest; each gateway keeps one HTTP/2 connection to the services balancer, which spreads requests, not connections, over the instances. Go and Java instances share one pool and one fan-out consumer group. The same in containers (`make cluster-up`): an image per backend, counts per language and role, replicas found by HAProxy through Docker's DNS, rescaled in place; for trying the scaled shape, measurements stay native.

- A plain connection balancer in front of the services would pin each gateway's one gRPC connection to one instance; HAProxy in HTTP/2 mode balances per request instead. Client-side balancing in each gateway would save the hop but needs doing in three gRPC clients of uneven support.
- The services balancer checks an instance by connecting to its RPC port: a stopped instance drops out, a hung one does not.
- In the cluster, HAProxy keeps a removed replica's address only a second, since Docker hands addresses to new containers: with the default holds, a slot of a kind scaled to zero kept passing checks against a container of another kind.
- Kubernetes was considered and left out: its Services balance connections too, so it would not replace HAProxy for the RPC, and it would add image builds and networking layers to a project whose point is comparing processes measured natively.

**Hardening.** Limits and liveness in every gateway, the same everywhere and documented in the contract: frame size, a deadline for Connect, a rate and an in-flight limit per connection (`ERROR_CODE_RESOURCE_EXHAUSTED`), one ack write at a time, pings and an idle timeout, close codes for every way a connection ends; the conformance tests cover the limits that show within seconds. Graceful drain: on SIGTERM a gateway fails its health check, refuses new connections, closes the open ones with 1001 so the devices reconnect elsewhere, and exits. The debug endpoints (profiles, forced garbage collection) answer only on the host; the device balancer passes only the WebSocket endpoint. Connect answers only once the user's push channel is subscribed, and fails `UNAVAILABLE` after 5 s so it may be retried.

**The outbox.** Outbox rows are deleted in the transaction that inserts them, as in Debezium's outbox pattern: the connector streams the insert and skips the delete, so the table stays empty. Debezium was kept over a relay in the services that produces to Kafka itself: a relay gives the same guarantee with fewer moving parts, but it loses the order of a conversation's events unless it polls each shard in order, has to be written in every services language, and must follow resharding by itself. Reading the change log keeps commit order for free, which at the scale the design aims for outweighs the extra process.

**Vitess restart.** The change-data path was found not to survive a restart of the Vitess container on its existing data: vtcombo comes back without a primary tablet for streaming, and Debezium retries forever; `make infra-reset` recovers. Rebuilding the image (a change under `infra/vitess/`) restarts it. Open in the roadmap.

**Accounts (#1).** Accounts with passwords: Register and SignIn return a device token, and Connect carries the token. Names are unique ignoring case and can change; ids are random UUIDs, for users and conversations both, which is what let groups fit later. Profiles have a name and one of a fixed set of icons, and changes reach the user's devices and conversation partners through a second outbox, in the user keyspace, and its own Debezium connector. TLS for devices on the network: an HAProxy edge with a certificate from mkcert, in front of whatever serves port 8080, which listens on loopback only; the web client is served over HTTPS likewise. Hashing a password takes about 19 MiB and tens of milliseconds, so the services run at most as many hashes at once as they have cores; a burst of sign-ins, as the load generator makes before a run, raises the services' resident memory, so measurements force a garbage collection before sampling.

## 2026-10-12

**Devices, deletion and blobs.** Devices listed, renamed and signed out from another device; a signed-out device's connections close at once (4001), through a sign-out on the user's push channel. Account deletion frees the name, signs every device out and removes the user's data; partners see a deleted account. Object storage for blobs: Garage behind a proxy on loopback 9200 and at `/blobs/` on the TLS edge (MinIO no longer publishes images). Uploads are signed by the services; avatars were the first use.

**Groups (#2).** A kind fixed at creation, a topic, admins, members added by anyone and removed by admins, leaving with promotion of the earliest member, at most 256 members. Changes are notices in the group's sequence; outbox rows carry their recipients. Read events go to the senders of the messages read, so clients show "read by n" without a fan-out to every member per read. Go and Java implement it; the web and iOS clients create groups, show members and sender names; the load generator can send a share of its messages to groups (`--group-share`, `--group-size`).

**Deleting messages (#4).** Senders delete their own messages for everyone: a tombstone that keeps the seq, the copies in members' logs and the list preview scrubbed by the fan-out, and a background push so iOS withdraws a notification already shown. Go and Java implement it; both clients delete from a message's menu and show "Message removed".

**End-to-end encryption (#6).** A key per conversation and epoch, wrapped for every member device's X25519 key; content sealed with AES-256-GCM; the services store and relay ciphertext and wrapped keys. Membership changes force a new epoch; device changes make senders rotate; a new device gets history from the user's other devices after the user approves. Safety numbers with a warning when a verified contact's devices change. Go and Java serve it, both clients and the load generator encrypt, the iOS notification service extension decrypts pushes. Test vectors shared by Go, TypeScript and Swift.

**Reactions (#3).** One sealed set per user and message, replaced as a whole: relayed as `ReactionEvent`s and returned with `GetMessages`, removed with the message. Go and Java implement it; both clients show chips with counts and react from a picker.

**Stickers (#5).** Users make stickers from their own images; stickers are public, permanent and read like any blob; anyone saves any sticker to a palette of up to 200, reordered and synced across devices. Sending one is sealed content naming it. Go and Java implement it; both clients send from the palette, save received stickers and manage the palette.

**Attachments (#7).** Images and files, encrypted on the device with a key per file, uploaded through the blob proxy, with keys, digests, names and thumbnails in the sealed content and the blob ids in clear for their lifecycle. Images are scaled and thumbnailed on the device. Go and Java check and keep the blobs; both clients send, show thumbnails, open a full-screen viewer and download files; the load generator can include attachments (`--attachment-share`, `--attachment-size`).

## 2026-10-13

**A review round over every implementation.** Go and Java release a message's uploads on any failure after marking them used (the remaining cases are in the roadmap); the Java gateway keeps its connection state as one enum under one lock and leaves WebSocket control frames outside the rate limit, as the other gateways do; the Elixir gateway keeps its connection state in a struct and counts queued pushes exactly, so its 1013 cut is the same 32 queued pushes as everywhere else; the Rust gateway bounds presence writes at 5 s and completes the closing handshake; the infrastructure got health checks for every service and hermetic lint of every HAProxy configuration.

**The Vitess restart, found the hard way.** A Docker Desktop restart restarted the Vitess container, which started an empty database; Debezium's stored offsets pointed into history that no longer existed, both connectors retried forever ("failed to find a PRIMARY tablet for VStream"), and every conformance run failed at the readiness probe with "no event delivered". `make infra-reset` fixed it; detecting the state in `make infra-up` is an idea in the roadmap.

## 2026-10-14

**Contacts and blocking (#10).** Mutual contacts, by a request and its acceptance; groups take only the adder's contacts. A block refuses the blocked user everything one-to-one with a plain permission error, and hides them in shared groups on the blocker's devices. Each user changes only their own side, through their own outbox; the fan-out copies it to the other user's shard, so every check is a primary-key read of one row. Go and Java implement it; both clients manage contacts and blocks, and the load generator befriends a group's members before creating it.

## 2026-10-15

**Notes.** Each user has a conversation of their own, shared among their devices: a third conversation kind with one member, found by the user's pair with themselves, so two devices opening it at once agree. Everything else is a direct conversation's machinery, the keys wrapped for the user's own devices. Go and Java implement it; both clients pin it at the top of the list.

**The open-file limit, found by a failed overnight measurement.** macOS starts shells with a soft limit of 256 open files. Go and the JVM raise their own; the BEAM and the Rust runtime do not, so run from a terminal, the Elixir gateway refused connections past about 200 and its listener restarted again and again, while every run from the agent's shell, with a higher limit, passed. The launcher the Elixir and Rust gateways share now raises the limit, and the protocol refuses to append to a results file from another commit.

**Every shape measured again** ([results](results/2026-10-15-shapes.md)), at bfc6a78, three repetitions each, the first round since accounts, groups and encryption. All six sustained 500 messages per second at 86 to 118 ms p50 end to end, and the database still set the limit above it. The Elixir gateway, whose median had drifted from 119 to 174 ms over three repetitions in the spoiled run, held at 117 to 122 ms on an idle machine; the drift was the machine, not the gateway. The single processes' resident memory now measures the sign-in phase's password hashing, so only their live memory is quoted.

## 2026-10-21

**The roadmap ordered.** The open work was put in the order it would be done, each item with its plan: coalesced acks first, as the cheapest relief for the database, then the database ceiling, then retention. History is to be kept for good, which the two weeks of the capacity estimates did not assume; replacing MySQL with HBase was weighed and rejected, since the send path, the claims and the change-data path rest on multi-row transactions and the binlog, and HBase was chosen instead as a cold tier for messages past a hot window, with the user logs kept short.

**The database ceiling moved.** Two changes from the ordered roadmap: every gateway writes a connection's acks at most once a minute, and on close, instead of each as it came; and the Go and Java fan-out store a message's copies in one transaction per user-keyspace shard, together with whatever else is queued for that shard, instead of one transaction per copy. A diagnostic count of Vitess's statements at 500 messages a second, taken from vtcombo's query counters, showed the fan-out's share falling from about 12 per message to 2 and the total from 24 to 13; Go's send path also stopped reading the conversation row for direct conversations, as Java already did. Measured again ([results](results/2026-10-21-shapes.md)), five of the six shapes sustained 1000 messages per second, twice the earlier limit, and every shape was faster below it. The Elixir gateway did not: at 1000/s its own queueing, not the database, timed out some of its sends, the first measurement where a gateway rather than the shared path set the limit.

## 2026-10-24

**A TypeScript backend.** A third full implementation, on Node 26 running TypeScript directly: gateway, services and fan-out, as a monolith or split, with the Go code as the reference for every feature, so the five implementations share data and the conformance tests hold it like the others. It is the one where a process has one thread for all its JavaScript, which the measurements will show the cost of. Two findings on the way: librdkafka's default partition assignors share none with franz-go's, so in a mixed group the TypeScript worker could not join until it offered the cooperative sticky assignor too; and the Kafka client's native build, with no prebuilt binary for Node 26 on Linux, has to run outside the package's scoped directory, since GNU `ar` cannot read the `@` in that path.

**Pushes from Java.** The Java fan-out sends APNs notifications, a port of Go's notifier on the JDK's HTTP/2 client, and reads `.env` as the others do. Every full backend now pushes, so a mixed pool no longer loses the notifications of the conversations a Java instance consumes.

**Eight shapes measured** ([results](results/2026-10-24-shapes.md)), at 7e6a395, the TypeScript backend for the first time. Five sustained 1000 messages per second: the Rust and Java gateways and the three monoliths, the TypeScript one with the lowest latency at that rate and about two thirds of the Go monolith's CPU per message. The Go, TypeScript and Elixir gateways delivered everything, or nearly, at 1000/s but with tails past a second, each queueing on the gateway's side of the RPC while the monolith of the same language did not; the TypeScript gateway kept the least live memory per idle connection, 5 KiB. With its schedulers no longer spinning, the Elixir gateway used 27 % less CPU per message and delivered 98 % at 1000/s, up from 95.
