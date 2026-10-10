# Roadmap

Each phase leaves something that runs on its own.

## 1. Contract and infrastructure

- [ ] Protobuf contract: WebSocket frames (request, response, event, ack, ping), requests and responses, the event types.
- [ ] SQL schemas and Vitess keyspaces: conversations by conversation id, users by user id, with vschemas.
- [ ] Docker Compose: Vitess, Kafka (KRaft, single broker), Debezium on Kafka Connect, Valkey. Heaps capped so the whole stack fits in ~4 GB.
- [ ] Smoke test of the change-data path: an outbox insert appears on the Kafka topic.

Open questions:

- Vitess publishes x86 images only (`vitess/vttestserver`, `vitess/lite`). It builds and runs on arm64 Linux and macOS, and its maintainers develop on arm64 Macs, but there are no official arm64 builds or images ([vitessio/vitess#17225](https://github.com/vitessio/vitess/issues/17225); the image request is [#15034](https://github.com/vitessio/vitess/issues/15034)). Plan: build an arm64 `vttestserver` image locally from the Dockerfiles in the Vitess repository, pinned to a release tag, so the stack runs natively. Fallback: the x86 image under emulation, acceptable since the database layer is the same for every backend.
- Debezium's Vitess connector reads through vtgate's change stream; whether `vttestserver` supports it needs checking. Fallback: Debezium's MySQL connector on the single `mysqld` behind `vttestserver`, which captures all shard databases from one binlog.

## 2. Go backend

- [ ] One binary, roles chosen by configuration: gateway, conversation service, sync service, fan-out worker.
- [ ] Conformance tests in `conformance/`, run against any backend through the contract.

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
