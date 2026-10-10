# Roadmap

Each phase leaves something that runs on its own.

## 1. Contract and infrastructure

- [x] Protobuf contract: WebSocket frames (request, response, event, ack, ping), requests and responses, the event types.
- [x] SQL schemas and Vitess keyspaces: conversations by conversation id, users by user id, with vschemas.
- [x] Docker Compose: Vitess, Kafka (KRaft, single broker), Debezium on Kafka Connect, Valkey. Heaps capped so the whole stack fits in ~4 GB.
- [x] Smoke test of the change-data path: an outbox insert appears on the Kafka topic.
- [x] `make ci`: the local CI entry point. Each later phase adds its checks to it (format, lint, unit tests, conformance against every backend).

Resolved:

- Vitess publishes x86 images only ([vitessio/vitess#17225](https://github.com/vitessio/vitess/issues/17225), [#15034](https://github.com/vitessio/vitess/issues/15034)). `infra/vitess/` builds `vttestserver` natively from the Vitess sources at a release tag, with MySQL 8.4 LTS from the multi-arch `mysql` image since there are no arm64 MySQL packages for Debian. The first build takes a few minutes; Compose caches it.
- Debezium's Vitess connector streams from `vttestserver` (vtcombo) over VStream. Its initial snapshot, a VStream copy, fails against vtcombo with duplicate BEGIN events, so the connector runs with `snapshot.mode=never`; the outbox only needs streaming. `BIGINT UNSIGNED` columns arrive as decimal strings in the JSON records.

Left for later:

- Outbox rows are never deleted. Prune them once the fan-out workers exist, or delete in the same transaction as Debezium's outbox pattern suggests.

## 2. Go backend

- [x] One binary, roles chosen by configuration: gateway, conversation service, sync service, fan-out worker.
- [x] Conformance tests in `conformance/`, run against any backend through the contract.

Notes:

- The roles are `gateway` and `fanout`. The gateway runs the conversation and sync services in-process; running them as separate processes, with the gateway calling them remotely, is left until a measurement needs it.
- Push goes from fan-out workers to gateways through Valkey pub/sub, one channel per user; a gateway subscribes to a user's channel while it holds one of that user's connections. The presence hash (device to node) is maintained but nothing reads it yet.
- Ids are derived, not allocated: user id from the name, conversation id from the user pair, both within 63 bits. Sign-in and opening a conversation need no lookup table.
- `MessageFailedEvent` is in the contract but nothing produces it yet; the synchronous path has no asynchronous failure to report.

## 3. Web client

- [x] One page of plain JavaScript: sign in by name, conversation list, conversation view, read marks, connection state.

Notes:

- No build step: protobuf.js loads the `.proto` files at runtime from `/proto/`, so `make web` serves the repository root. 64-bit ids travel as strings.
- `make web-test` drives the page in headless Chrome over the DevTools protocol, two users in two pages; it is part of `make ci`. The only JavaScript check beyond that is `node --check`.

## 4. Load generator and metrics

- [x] Load generator in Go: simulated devices, configurable connection count, message rate, devices per user.
- [x] Prometheus and Grafana in Compose; the same metric names in every backend.
- [x] Measurement protocol in `docs/`: warm-up, run length, repetitions, how results are recorded (date, commit, machine, configuration).
- [x] First results for the Go backend.

Notes:

- `docs/measuring.md` is the protocol, `loadgen/protocol.sh` runs it, `docs/results/` holds the write-ups with the raw JSON lines. Metric names are in `docs/metrics.md`.
- The first run found the fan-out worker serial and the protocol leaking Kafka backlog between runs; both fixed the same day. The second run: 500/s sustained with 66 ms p50 end to end, about 46 KiB per idle connection.
- The Vitess container saturates between 500 and 1000 messages per second on this machine, so backends can only be compared below that. Giving the stack more CPU, or splitting vtcombo into real vtgate and vttablet processes, would raise the ceiling; so would batching in the fan-out worker.
- Idle memory scatters by about 20 MiB between runs; quote medians of repetitions (`REPS=3`).

## 5. Java backend

- [x] Same contract and conformance tests. Java 25 (virtual threads without pinning on `synchronized`), Netty and Armeria.
- [x] Go vs. Java comparison.

Notes:

- Armeria's WebSocket service over Netty; request handling and fan-out on virtual threads; HikariCP over Connector/J, the Kafka consumer, Lettuce for Valkey. Gradle with a JDK 25 toolchain, google-java-format through Spotless as the format check, `-Xlint:all -Werror`.
- Ids are computed by an own xxhash64 that matches the Go backend bit for bit (a unit test holds Go's values), so both backends run on one database.
- The JVM exporter reports no resident memory on macOS; the load generator reads it with `ps` instead.
- First comparison in `docs/results/2026-10-10-go-vs-java.md`: both sustain 500/s, Go's tail is tighter, baselines differ by 200 MiB, CPU per message within 10 %. The per-connection memory figures need repetitions and a fixed JVM heap before they mean anything.

## 6. iOS client

- [x] SwiftUI, `URLSessionWebSocketTask`, SwiftProtobuf; local storage, so a device syncs only what it is missing.

Notes:

- The logic is a Swift package, `clients/ios/Packages/WrinkleCore`: `WrinkleCore` has the generated contract, the connection actor and the SwiftData store; `WrinkleKit` the session and the screens. `Sources/iOS` is the shell. The Xcode project is generated by xcodegen from `project.yml` and not committed, in the style of the sibling SwiftUI projects.
- The package compiles for the Mac too, so `swift test` runs headless: store tests, and a backend test over a real connection to `localhost:8080`. `make ios-test` runs them against the Go backend; `make ios-build` builds the app unsigned for the simulator; both are in `make ci`. `make ios-lint` is swiftlint and swift-format, strict.
- iOS 26 is the floor. The device keeps its device id and cursor per user in UserDefaults and conversations and messages in SwiftData; after a reconnect it syncs from the cursor and acks.
- `Scripts/run-ios.sh` builds, installs and launches the app in an iPhone simulator; the simulator reaches the host's backend as `localhost`.

## 7. Elixir and Rust

- [ ] Gateway first in each, since connection handling is where they differ most; then the full backend if worthwhile.
- [ ] Four-way comparison.

## Later

- [ ] A second Go gateway beside the current one, without a goroutine per connection (a poller-driven WebSocket library), to see how far the idle memory per connection falls when the model changes; see `docs/results/2026-10-10-idle-memory.md`.

## Before making the repository public

- [x] Choose a license: MIT.
- [ ] Results write-up in `docs/`.
