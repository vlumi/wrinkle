# Roadmap

Each phase leaves something that runs on its own.

## 1. Contract and infrastructure

- [x] Protobuf contract: WebSocket frames (request, response, event, ack, ping), requests and responses, the event types.
- [x] SQL schemas and Vitess keyspaces: conversations by conversation id, users by user id, with vschemas.
- [x] Docker Compose: Vitess, Kafka (KRaft, single broker), Debezium on Kafka Connect, Valkey. Heaps capped so the whole stack fits in ~4 GB.
- [x] Smoke test of the change-data path: an outbox insert appears on the Kafka topic.
- [x] `make ci`: the local CI entry point. Each later phase adds its checks to it (format, lint, unit tests, conformance against every backend).

Resolved:

- Vitess publishes x86 images only ([vitessio/vitess#17225](https://github.com/vitessio/vitess/issues/17225), [#15034](https://github.com/vitessio/vitess/issues/15034)). `infra/vitess/` builds `vttestserver` natively from the v23.0.0 sources, with MySQL from the multi-arch `mysql` image since there are no arm64 MySQL packages for Debian. The first build takes a few minutes; Compose caches it.
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

- [ ] One page of plain JavaScript: sign in by name, conversation list, conversation view, read marks, connection state.

## 4. Load generator and metrics

- [ ] Load generator in Go: simulated devices, configurable connection count, message rate, devices per user.
- [ ] Prometheus and Grafana in Compose; the same metric names in every backend.
- [ ] Measurement protocol in `docs/`: warm-up, run length, repetitions, how results are recorded (date, commit, machine, configuration).
- [ ] First results for the Go backend.

## 5. Java backend

- [ ] Same contract and conformance tests. Java 25 (virtual threads without pinning on `synchronized`), Netty and Armeria.
- [ ] Go vs. Java comparison.

## 6. iOS client

- [ ] SwiftUI, `URLSessionWebSocketTask`, SwiftProtobuf; local storage, so a device syncs only what it is missing.

## 7. Elixir and Rust

- [ ] Gateway first in each, since connection handling is where they differ most; then the full backend if worthwhile.
- [ ] Four-way comparison.

## Before making the repository public

- [ ] Choose a license.
- [ ] Results write-up in `docs/`.
