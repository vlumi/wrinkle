# Wrinkle

*A new wrinkle on messaging.*

Wrinkle is a prototype backend for one-to-one text messaging across multiple devices per user: real-time delivery to connected devices, incremental sync for the rest, and read receipts. The same architecture is implemented in several languages, with shared clients and a load generator, to see how each implementation behaves as load grows.

It is an experiment, not a product. Nothing here is production-hardened.

**Status:** the Go backend passes the conformance tests; no clients or measurements yet. See [ROADMAP.md](ROADMAP.md).

## How it works

Three ideas carry the design:

- **Per-conversation sequence.** Every message gets a sequence number from a counter owned by its conversation, assigned in the transaction that stores it. Every device orders a conversation by it, so all participants converge on the same order, and a device can tell when it is missing a message.
- **Per-user event log.** Everything a user's devices need to know (messages in any of their conversations, read receipts, delivery failures) is appended to that user's log. A device keeps one cursor into it, and sync returns what came after.
- **Push is an optimization, sync is the guarantee.** Connected devices get events over WebSocket as they happen. A device that misses a push gets the same events from its log on the next sync.

```text
 clients ── WebSocket ──► gateway ──► conversation service ──► conversation DB
    ▲                        ▲                                  (sharded by conversation)
    │ push                   │ push                                   │ CDC
    │                        │                                        ▼
    │                   fan-out workers ◄────────────────────────── Kafka
    │                        │
    │ sync                   ▼
    └──────── sync service ─► user DB (sharded by user):
                              event logs, conversation lists, devices
```

Components:

- **Gateway:** holds the WebSocket connections, tracks which device is connected where, forwards requests, pushes events.
- **Conversation service:** sending and read marks; stores each change with an outbox row in one transaction.
- **Fan-out workers:** read committed changes from Kafka (captured from the outbox by Debezium), append them to each participant's event log, and push them to connected devices.
- **Sync service:** incremental sync, conversation lists, device registration.
- **Presence:** which device is connected to which gateway node.

Infrastructure: MySQL sharded with Vitess (one keyspace by conversation, one by user), Apache Kafka, Debezium, Valkey.

## Implementations

All backends implement the same Protobuf contract and pass the same conformance tests, so clients and the load generator work against any of them.

| Backend | Status |
|---|---|
| Go | passes conformance |
| Java | planned |
| Elixir | later |
| Rust | later |

Clients: a web client (plain JavaScript) and an iOS client (SwiftUI), both planned.

## Measuring

The load generator simulates devices and reports, per backend on the same machine and infrastructure:

- memory per idle connection, from the slope across several connection counts;
- end-to-end latency, send to receive, p50 and p99, at fixed message rates;
- the highest sustained message rate before p99 latency breaks;
- CPU time per thousand messages.

Absolute numbers depend on the machine and say little about production. Comparisons between backends under identical conditions are the point.

## Running

Needs Docker Desktop with at least 8 GB of memory, Go, and `make tools` once for `buf`, `protoc-gen-go` and `golangci-lint`.

```sh
make infra-up          # Vitess, Kafka, Debezium and Valkey, with the outbox connector registered
make ci                # everything below, plus lint and unit tests
make smoke             # the change-data path: an outbox insert reaches Kafka
make conformance-go    # the conformance tests against the Go backend
make infra-down        # stop and discard all data

go run ./backends/go/cmd/wrinkle   # the Go backend on ws://localhost:8080/v1/ws
```

From the host: MySQL protocol through vtgate at `127.0.0.1:15003` (keyspaces `conversation` and `user`, no password), Kafka at `localhost:19092`, Kafka Connect at `localhost:8083`, Valkey at `localhost:6379`.

## Layout

```text
proto/        the contract: frames, requests, events
schema/       SQL schemas and Vitess keyspace definitions
infra/        Docker Compose for Vitess, Kafka, Debezium, Valkey, metrics
backends/     go/, java/, elixir/, rust/
clients/      web/, ios/
loadgen/      load generator (Go)
conformance/  backend-agnostic tests against the contract
docs/         design notes and measurement results
```
