# Go backend

The Go backend is one binary, `bin/wrinkle`, that runs any of the three roles: the WebSocket gateway, the conversation and sync services, and the fan-out worker, with APNs push from the fan-out. It runs as a monolith (all roles in one process, the default) or split into gateways and services instances over the internal gRPC contract, and its services instances also serve the Elixir and Rust gateways. Go 1.27 with [coder/websocket](https://github.com/coder/websocket) for the connections, go-sql-driver/mysql against vtgate, [franz-go](https://github.com/twmb/franz-go) for Kafka, go-redis for Valkey, grpc-go for the internal RPC, and client_golang for metrics. coder/websocket was kept over gws and gobwas/ws as the mainstream, maintained choice ([idle memory](../../docs/results/2026-10-10-idle-memory.md)).

The design common to every backend (data model, flows, guarantees, failure handling) is in [docs/architecture.md](../../docs/architecture.md); this page covers how the Go code realizes it.

## Layout

```text
cmd/wrinkle/      main: flags, roles, startup and shutdown; HTTP mux; .env loading; APNs wiring
gateway/          WebSocket connections, limits, keepalive, per-user registry, push delivery, drain
services/         the Backend interface: Local (in-process) and the gRPC server and client
accounts/         account service: Register, SignIn, device tokens and Connect, passwords, devices and their keys, profiles, name claims, deletion
conversation/     conversation service: direct conversations by pair claim, groups and notices, key epochs, send, read marks, history, outbox rows
devicesync/       sync service: devices and acks, the event log, conversation lists, group names, push tokens
uploads/          upload slots for blobs, and the sweeper of unused ones
fanout/           Kafka consumer: both outboxes to the recipients' logs and conversation lists, then publish
push/             APNs client (token auth, HTTP/2) and the notifier for devices not connected
presence/         Valkey: device-to-instance hash, instance liveness keys, per-user push channels
internal/ids      UUIDs for users and conversations (16 bytes, BINARY(16), hex in Valkey); random 63-bit ids
internal/vtdb     a database/sql pool for one Vitess keyspace
internal/blobs    the blob storage: SigV4 presigned requests, HEAD and DELETE through the blob proxy
internal/apierror contract error codes carried on Go errors
internal/metrics  the shared Prometheus metrics
split.sh          a Go gateway beside a Go services process, for conformance and measurement
Dockerfile        the image for the cluster, built from the repository root
```

## Roles and startup

[main.go](cmd/wrinkle/main.go) parses `--roles`, a comma-separated list of `gateway`, `services` and `fanout`:

- `gateway,fanout` (the default) is the monolith: the gateway calls the services in-process through `services.Local`.
- `gateway` with `--services <addr>` forwards every request over gRPC instead and opens no database pools. The channel connects lazily and reconnects with a backoff capped at 5 s, so the gateway starts and serves while the services are not reachable yet; until they are, requests fail fast with `UNAVAILABLE`.
- `services,fanout` is a services instance, as [services.sh](../services.sh), [beside-services.sh](../beside-services.sh) and the cluster run it.

Startup opens a pool per keyspace (`conversation`, `user`) when any role needs the database, connects to Valkey when a role needs it (gateway, fan-out; not a services-only process), then starts each role in its own goroutine; the HTTP server runs for every role. Before parsing flags, [env.go](cmd/wrinkle/env.go) reads `.env` from the working directory or beside `bin/`; variables already set win.

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--roles` | `WRINKLE_ROLES` | `gateway,fanout` | roles to run |
| `--listen` | `WRINKLE_LISTEN` | `:8080` | HTTP: `/v1/ws` (gateway), `/metrics`, `/healthz`, debug |
| `--mysql` | `WRINKLE_MYSQL` | `127.0.0.1:15003` | vtgate |
| `--kafka` | `WRINKLE_KAFKA` | `localhost:19092` | brokers, comma-separated |
| `--valkey` | `WRINKLE_VALKEY` | `localhost:6379` | Valkey |
| `--services` | `WRINKLE_SERVICES` | empty | gateway: forward to this services address |
| `--services-listen` | `WRINKLE_SERVICES_LISTEN` | `:9000` | services: gRPC address |
| `--node` | `WRINKLE_NODE` | host name | prefix of the instance name in presence |
| `--db-conns` | `WRINKLE_DB_CONNS` | 64 | connections per keyspace pool |
| `--fanout-concurrency` | `WRINKLE_FANOUT_CONCURRENCY` | 64 | conversations delivered at a time |
| `--apns-key` | `WRINKLE_APNS_KEY` | empty | `.p8` key; push is off without it |
| `--apns-key-id` | `WRINKLE_APNS_KEY_ID` | from `AuthKey_<id>.p8` | key id |
| `--apns-team-id` | `WRINKLE_TEAM_ID` | empty | developer team |
| `--apns-topic` | `WRINKLE_APNS_TOPIC` | the app's bundle id | APNs topic |
| `--blob-url` | `WRINKLE_BLOB_URL` | `http://127.0.0.1:9200` | the blob proxy, for HEAD and DELETE |
| `--blob-key-id` | `WRINKLE_BLOB_KEY_ID` | the local key | blob storage access key |
| `--blob-secret` | `WRINKLE_BLOB_SECRET` | the local secret | blob storage secret |
| `--blob-region` | `WRINKLE_BLOB_REGION` | `garage` | blob storage region |
| `--debug` | | false | debug logging |

On SIGINT or SIGTERM, or when a role fails, `/healthz` turns 503, the gateway drains (below), then the HTTP server and the other roles stop, all within 9.5 s. The gRPC server stops gracefully; the Kafka client leaves the group.

## Connections

Each connection is a `conn` in [conn.go](gateway/conn.go). The HTTP server's goroutine for the upgrade becomes the read loop; there is no writer goroutine while nothing is queued.

```mermaid
flowchart TB
  ws(["WebSocket"])
  read["read loop"]
  req["request goroutine"]
  ack["ack writer"]
  q[["out queue, 32"]]
  write["writer, while queued"]
  push["push loop, one per gateway"]
  ws --> read
  read -- "request" --> req
  read -- "ack" --> ack
  read -- "pong" --> q
  req -- "response" --> q
  push -- "event" --> q
  q --> write --> ws
```

- **Reading.** The read loop reads one frame at a time (read limit 64 KiB, so the library closes oversize frames with 1009), records the time for the idle check, and decodes a `Frame`. A text frame closes with 1003, a malformed or unexpected one with 1008.
- **Requests.** Each request runs in its own goroutine under a 10 s timeout, from the server's request context rather than the connection's, so a drain can let it finish. `dispatch` lets only Register, SignIn and Connect through before Connect (anything else gets `UNAUTHENTICATED`) and refuses those three after it (`FAILED_PRECONDITION`), then calls `services.Backend` with an empty user id until connected. A successful Connect records the user and device and attaches the connection to presence; the response goes out once the user's push channel is confirmed subscribed, or `UNAVAILABLE` if it is not within 5 s, after which the connection is unidentified again and Connect may be retried within a fresh 5 s deadline. A transport failure to the services becomes `UNAVAILABLE`.
- **Writing.** `send` puts serialized frames on a 32-slot channel and starts a writer goroutine if none runs; the writer exits when the queue is empty. Pushed events never wait: a full queue closes the connection with 1013 and the device syncs on reconnect. `wrinkle_events_pushed_total` counts only events accepted into the queue. Replies to the device's own frames wait up to 10 s for room, which holds back its read loop. A failed write closes with 1011.
- **Events** arrive from Valkey as a serialized `Push` ([push.go](gateway/push.go)), read with `protowire` so the event inside is never decoded; `pushEvent` wraps its bytes in a `Frame` by writing the field tag. A `Push` with `signed_out` closes the user's connections here whose device it lists, or all of them, with 4001.

Limits from the contract ([limits.go](gateway/limits.go)):

| Limit | How |
|---|---|
| 50 frames/s, bursts of 100 | a token bucket per connection, used only by the read loop, so unlocked; every frame takes a token |
| 32 requests in flight | an atomic counter; requests over either limit get `RESOURCE_EXHAUSTED`, other frames are dropped |
| one ack write at a time | `ackCursor` keeps the highest pending cursor; one goroutine writes until none is pending |
| Connect within 5 s | a timer per connection, stopped by Connect (not by Register or SignIn); on expiry, close with 1008 |
| pings every 30 s, idle after 90 s | one ticker per gateway ([keepalive.go](gateway/keepalive.go)) sweeps all connections: idle ones close with 1001, the rest get a ping; pongs update the last-seen time |

**Push delivery and presence.** [presence.go](presence/presence.go) names each process `<node>/<random hex>` and keeps the key `gateway:<instance>` alive with a 90 s expiry, refreshed by the same 30 s ticker. Users are named in Valkey by the lowercase hex of their 16-byte id. A connected device is a field in the hash `presence:<user>` naming that instance; disconnecting removes the field only if it still names this instance (a Lua script), so a device that reconnected elsewhere keeps its entry. The gateway has one Valkey pub/sub connection, and the [registry](gateway/registry.go) keeps `push:<user>` subscribed on it while the user has a connection on this gateway. Attaching and detaching only change the user's connection set under a short lock and queue the user; one worker compares each queued user's desired state with what it last requested and sends SUBSCRIBE or UNSUBSCRIBE, up to 1000 channels per command, so no lock is held across a Valkey round trip and a detach racing an attach settles on the right state. Valkey confirms every channel of each command in order; an attach waits until its user's requests are all confirmed and the last was a subscribe. One receiving loop hands each push to every local connection of the user and counts the confirmations. If the pub/sub connection fails (an error, or no answer to a ping after 15 s of silence), the gateway replaces it rather than relying on go-redis's own reconnect, after a pause doubling from 100 ms to 5 s (reset once a connection has received anything), and subscribes every user with a connection again; pushes published in between reach those devices on their next sync.

**Drain** ([server.go](gateway/server.go)): new upgrades get 503 and the health check fails; every open connection is closed with 1001; requests in flight get 5 s, then their context is canceled; closing handshakes get 3 s more, after which remaining sockets are dropped; then the instance key is deleted, so pushes to its devices go to APNs at once rather than after 90 s.

## Services

`services.Backend` ([backend.go](services/backend.go)) has two methods, `Call` for a device's request with its identity and `Ack`. `Local` dispatches to the three services, checking every conversation id in a request is 16 bytes (`INVALID_ARGUMENT`), and applies the same before-and-after-Connect rules as the gateway, for gateways in other languages; `Client` forwards both over [`proto/wrinkle/backend/v1`](../../proto/wrinkle/backend/v1) ([rpc.go](services/rpc.go)). The client is one `grpc.ClientConn`, so in the side-by-side setup it is one HTTP/2 connection to the services balancer. The server allows 2^20 concurrent streams per connection, since grpc-go's default of 100 failed the Elixir gateway under a connect storm. A request's own failure travels as the response's error body; a gRPC error means the request was not handled.

**Account service** ([accounts](accounts/)), against the `user` keyspace. Users and conversations get random version 4 UUIDs ([ids.go](internal/ids/ids.go)), stored as `BINARY(16)` and ordered bytewise.

- *Passwords* ([password.go](accounts/password.go)): argon2id from `golang.org/x/crypto/argon2`, m=19456 KiB, t=2, p=1, 16-byte salt, 32-byte key, stored as a PHC string with unpadded standard base64; verification reads the parameters from the string. At most `GOMAXPROCS` hashes run at once, since each takes about 19 MiB. A unit test checks a fixed PHC string that the Java backend checks too.
- *Device tokens* ([token.go](accounts/token.go)): `v1.` and the unpadded base64url of user id, device id (8 bytes big-endian) and a 32-byte random secret; the device row keeps the SHA-256 of the secret in `token_hash`, compared in constant time. `NULL` means signed out.
- *Name claims* ([names.go](accounts/names.go)): `user_name` is sharded by `unicode_loose_xxhash` with an accent-sensitive, case-insensitive collation, so names collide ignoring case. A claim counts only while its owner's row still has the name, checked by MySQL (`name = ? COLLATE utf8mb4_0900_as_ci`), never by Go. Claiming inserts; on a duplicate, a claim of our own is fine, a valid one of another user is `ALREADY_EXISTS`, and a stale one is deleted and the insert retried, up to three times.
- `Register` validates (names 1 to 64 bytes of UTF-8 without control characters or white space at either end; passwords 8 to 256 bytes), hashes, inserts the user row, then claims the name, deleting the row again if the name is taken, so an interruption leaves at worst a user nobody can reach. Then a new device with its token. `SignIn` resolves the name through a valid claim and verifies the password; an unknown name or an out-of-range password verifies against a dummy hash, so it fails as slowly and with the same message as a wrong password. Each sign-in is a new device.
- `Connect` parses and checks the token, reads the user (a deleted one is an invalid token), the device's acked cursor and the log head, and touches `last_seen_at`.
- *Devices* ([devices.go](accounts/devices.go)): every change to a device writes a `user_outbox` row of kind `device` with a `DeviceEvent`, in the transaction that makes it and under the lock on the user row, which holds `last_outbox_id` ([outbox.go](accounts/outbox.go)): a sign-in or registration, `RenameDevice`, `SignOut`, `SignOutDevice` (another device of the user's that is signed in, else `NOT_FOUND`), and `ChangePassword`, which checks the current password before the transaction and again that the hash is unchanged inside it, then stores the new hash and signs out every other device, one event each. Signing out clears the token and the push registration; fan-out then closes the device's connections. `ListDevices` lists the devices with a token, oldest first. Every `DeviceEvent` carries the user id.
- *Device keys* ([keys.go](accounts/keys.go)): `SetDeviceKey` takes a 32-byte X25519 public key (`INVALID_ARGUMENT` otherwise) and stores it in `device.public_key` under the same locks, with its `device` outbox row; the same key again commits nothing, a different one is `FAILED_PRECONDITION`. `GetDeviceKeys` takes 1 to 256 ids of 16 bytes and returns, in the order asked, each user that exists and is not deleted with their signed-in devices that have a key, only id and key, by device id: two queries over the asked users' shards, the users, then their devices.
- `DeleteAccount` checks the password like `ChangePassword`, then in one transaction empties the user row (name, icon, avatar, password hash) and sets `deleted_at`, keeping it as a tombstone, signs out every device and writes a `ProfileEvent` of a user with `deleted` set; then it deletes the name claim, which is stale anyway once the row no longer has the name.
- `UpdateProfile` claims a new name first (a case-only change finds its own claim), checks a new avatar (the user's `upload` row with purpose `avatar`, then a HEAD of the blob), then in one transaction locks the user row, updates name, icon and avatar, marks the new avatar's upload used and the old one's unused, takes `last_outbox_id + 1` and inserts and deletes the `user_outbox` row with a `ProfileEvent`. Then it releases the old claim, unless the change is in case only, which keeps the one claim; a release that fails leaves a stale claim, which the next claimant takes over.

**Conversation service** ([service.go](conversation/service.go)), against the `conversation` keyspace. `Send` validates, then in one transaction locks the conversation row (`SELECT ... FOR UPDATE`), returns the stored message if the client message id was seen before, otherwise takes `last_seq + 1`, inserts the message, and writes the outbox row. `MarkRead` does the same for read marks, moving only forward. The outbox row is inserted and deleted in the same transaction: the binlog keeps the insert for Debezium and the table stays empty. Opening a conversation looks up `conversation_pair` (sharded by `user_a`); if there is none it inserts one with a new id, and on a duplicate key reads the winner's, so both users opening at once get one conversation. The conversation row (kind `direct`, both ids as its members) and both `conversation_member` rows follow with `INSERT IGNORE`, which also completes a pair whose opener stopped in between. A conversation row keeps its current members as concatenated 16-byte ids, sorted, so a send checks membership and names its recipients from the row it locks anyway. Bodies are serialized `Message`s with only the content (sealed or notice); the servers never read sealed content. `Send` checks the nonce (12 bytes) and ciphertext (1 to 48 KiB) first, and after the resend lookup that the sealed epoch is the row's `key_epoch` and none is 0 (`FAILED_PRECONDITION`, stale key epoch), then that `rotate` is not set (`FAILED_PRECONDITION`, keys must rotate). Sending also moves the sender's `read_mark` to the message, without a read event. In a group, a read event goes to the reader and to the current members who sent any of the newest 1000 messages it covers. `GetMessages` returns what the user's `conversation_member` row allows: from `joined_seq`, and up to `left_seq` for a former member. `DeleteMessage` ([delete.go](conversation/delete.go)) locks the conversation row, checks the requester's `conversation_member` row (a former member may delete their messages up to `left_seq`; outside what they may read is `NOT_FOUND`), the message's kind (notices `INVALID_ARGUMENT`) and sender (`PERMISSION_DENIED`), replaces the body with a `Message` holding only `deleted_at`, and writes a `message_deleted` outbox row to the current members, and the requester if no longer one, with `target_outbox_id` naming the message's own outbox row. Deleting a deleted message commits nothing.

**Key epochs** ([keys.go](conversation/keys.go)). The conversation row holds `key_epoch` (0 until the first publish) and `rotate`; adding, removing or leaving sets `rotate` when `key_epoch` is above 0, in the same update as the members. `PublishKeys` validates (1 to 500 keys, 16-byte recipient user ids, non-zero device ids, 60-byte wraps), reads the requesting device's key from the user keyspace, then locks the conversation row: a non-member is `PERMISSION_DENIED`, a device without a key `FAILED_PRECONDITION`. Epoch `key_epoch + 1` needs every recipient to be a current member (`INVALID_ARGUMENT`), inserts the wraps and moves `key_epoch` to it, clearing `rotate`. The current epoch takes current members as recipients; an earlier one only the requester's own devices (`PERMISSION_DENIED`). A request for an existing epoch that wraps for the requesting device itself is an attempt to make that epoch, so unless this device already holds a wrap of the epoch it made (a retry), it fails with `FAILED_PRECONDITION`, as does any other epoch: of two devices publishing the same next epoch, the later fails. Wraps go in with `INSERT IGNORE`, so a recipient keeps the one it has. `GetConversationKeys` needs a `conversation_member` row, current or former, and returns the row's epoch and `rotate`, the recipients of the current epoch's wraps, and the requesting device's wraps after `after_epoch` by epoch; a former member's stop at what they had, since recipients must be current members. Wraps stay when devices sign out and accounts are deleted.

**Groups** ([group.go](conversation/group.go)). `CreateGroup` claims the creator's client group id in `group_claim` (sharded by creator; a duplicate key reads the winner's), then in one transaction on the conversation's shard creates the row, the members (the creator admin, everyone from seq 1) and the `GroupCreated` notice, or finds the group a first attempt created. Every change (`AddMembers`, `RemoveMember`, `LeaveGroup`, `SetTopic`, `SetAdmin`) locks the conversation row, checks kind (`FAILED_PRECONDITION`), membership and, for removing and admin changes, the admin flag (`PERMISSION_DENIED`), and stores a notice as the next message with an outbox row to the members (before a removal or leave, so the one leaving hears of it). Profiles in notices and `GetMembers` are read from the user keyspace in one `IN` query. The last admin leaving promotes the member who joined first (`joined_seq`, `joined_at`, user id). A deleted account first leaves every group its list shows it in.

**Sync service** ([devicesync](devicesync/)), against the `user` keyspace: `Ack` moves the device's cursor forward with `GREATEST`; `Sync` reads the user's log after a cursor, up to 1000 events, with `has_more`; `Open` resolves the peer by name through a valid claim (`NOT_FOUND` otherwise, `INVALID_ARGUMENT` for oneself), opens the conversation and adds it to the user's list with the peer's current name and icon; `CreateGroup` and `AddMembers` resolve the member names (at most 255; unknown names `NOT_FOUND`; the requester and repeats dropped), and a new group goes into the creator's list at once; `ListConversations` reads that list, most recent first; `RegisterPushToken` stores or clears an APNs token.

**Uploads** ([uploads](uploads/)): `CreateUpload` checks the purpose's media types and size, inserts an `upload` row with a new blob id and returns a PUT presigned with SigV4 ([blobs.go](internal/blobs/blobs.go)) for exactly that `Content-Type` and `Content-Length`, valid 15 minutes. Requests are signed for the host `blobs`, which the blob proxy sets on every request; a unit test checks the same vectors as the Java backend's. The sweeper runs with the fan-out role, a minute after start and then every 10 minutes: it reads up to 500 uploads unused for a day, deletes each row while still unused, and only then the blob, so of several sweepers one deletes it.

Pools: [vtdb.go](internal/vtdb/vtdb.go) interpolates parameters on the client, so vtgate sees no prepared statements, with 5 s connect and 10 s read and write timeouts.

**Fan-out worker** ([worker.go](fanout/worker.go)). One franz-go consumer in the group `wrinkle-fanout` on both outbox topics, `wrinkle.conversation.outbox` (keyed by conversation id) and `wrinkle.user.user_outbox` (keyed by user id), shared with any Java services instances. Debezium writes binary columns as base64 and unsigned bigints as decimal strings. Auto-commit is off and rebalances are blocked while a poll's records are handled. Per poll:

1. Records of each partition are grouped by key; each conversation's or user's records run in offset order in one goroutine, up to `--fanout-concurrency` keys at a time.
2. A record is parsed from Debezium's JSON ([outbox.go](fanout/outbox.go)) and delivered to the recipients the row names, 16 at a time ([deliver.go](fanout/deliver.go)): in one transaction, lock the user row, append the event at `log_head + 1`, move the head, and update the conversation list (last message, a group's topic and size and the recipient's member and admin flags from the notice, all only from a message at or after the entry's last one, or read positions, the peer's in direct conversations only, or for a deletion the copy of the message in the log, found by the target outbox row and rewritten without its content, and the entry's last message when it is the one deleted, leaving `updated_at`; for the sender, a message also moves its own read position to it, `GREATEST(read_seq, seq)` in the same statement). The unique key on the source outbox row (`source_id`, the conversation's or user's id, and `outbox_id`) turns a redelivery into a no-op. A deleted user's log gets nothing. A message that creates a direct list entry gives it the peer's current profile, read before the transaction; entries known to exist are cached, since they are never removed. A profile change ([profile.go](fanout/profile.go)) goes to the user's own log, then to each direct conversation partner found in the user's list, 500 at a time in id order and up to 16 partners at once, each append in one transaction with the partner's `peer_name`, `peer_icon`, `peer_avatar` and `peer_deleted` update. A deleted user's goes to the partners in the same way, then a `Push` with `signed_out` for all devices closes the user's connections, and the user's log, list and devices are deleted, 1000 rows a statement, and the uploads marked unused. A device change goes to the user's own log; a sign-out then publishes a `Push` with `signed_out` for that device, even when the event was a duplicate. A change to a device that has a key (setting it, signing out, renaming) also goes, as a `DeviceEvent` with only the user id, device id and key and `signed_out`, to everyone who encrypts for it ([device.go](fanout/device.go)): the direct partners and the groups the user's list shows them a member of, 500 entries at a time, the groups' current members read from the `conversation` keyspace, each user once, under the same source key. Each event is published to `push:<user>` as a `Push`; a failed publish is only logged, since the log has it.
3. A failing record is retried five times with growing pauses. If it still fails, the worker rewinds every partition to the start of the poll, waits a second and fetches again, so nothing is skipped; malformed rows are logged and skipped.
4. When everything succeeded, the offsets are committed.

**Push notifications** ([push/](push/)). After a message is appended to a recipient's log, the worker calls `Notifier.MessageDelivered`, which returns at once: the send runs in a goroutine, at most 64 at a time, and beyond that it is dropped (`dropped` in the metrics). Profile changes and notices notify nobody; a deletion (`Notifier.MessageDeleted`) sends a background notification (`content-available`, priority 5) with `conversation_id`, `seq` and `deleted` to every registered device of the recipient that is not connected, so the app can withdraw an alert it showed. It reads the user's registered tokens and, from the conversation list, the peer's current name as the title, or in a group the topic; the server cannot read the message, so the body is `New message`, with `mutable-content` set and the sealed content as data (`epoch`, base64 `nonce` and `ciphertext`, hex `sender_user_id`, `client_message_id`) for the app's notification service extension to decrypt and show instead, left out when the payload would exceed 4 KB. It asks presence which devices are connected to a live instance (removing entries of instances whose key expired), computes the badge as unread messages across conversations, and sends an alert to each registered device that is not connected, never to the sender's own. The conversation id travels as lowercase hex in the thread id and the `conversation_id` data field, the message's seq as the number `seq`. [apns.go](push/apns.go) signs an ES256 provider token with the `.p8` key, reuses it for 45 minutes, and posts to the production or sandbox endpoint as the token was registered. A token APNs rejects (410, `BadDeviceToken`, `DeviceTokenNotForTopic`) is cleared, unless the device registered a new one meanwhile.

## Metrics and debug endpoints

Every role serves on `--listen` ([http.go](cmd/wrinkle/http.go)):

- `GET /metrics`: the shared metrics ([metrics.go](internal/metrics/metrics.go)) and the Go runtime's, uncompressed; names in [docs/metrics.md](../../docs/metrics.md).
- `GET /healthz`: 200, or 503 while shutting down.
- From loopback addresses only: `GET /debug/pprof/` (index, `profile`, `heap`, `goroutine`, `allocs`) and `POST /debug/gc`, which collects and returns memory to the OS before a measurement samples it.

The gateway adds `GET /v1/ws`. A services instance in the side-by-side setup listens on `127.0.0.1:8190+n`.

## Building, running, testing

| Target | What |
|---|---|
| `make build-go` | `bin/wrinkle` |
| `make run-go-mono` | all roles on `:8080`; `ARGS=...` passes flags |
| `make run-go-services` | a services and fan-out instance, `INSTANCE=n`, RPC on `:9100+n` |
| `make run-go-gw` | a gateway on `:8180` behind the balancers (`make lb-up`) |
| `make conformance-go-mono`, `conformance-go-gw` | the conformance tests against one shape |
| `make measure-go-mono`, `measure-go-gw` | the measurement protocol, from a fresh stack |
| `make cluster-up GO_SERVICES=2 GO_GW=1` | the image from the [Dockerfile](Dockerfile) in the cluster |

[split.sh](split.sh) runs a Go gateway beside a Go services process through [beside-services.sh](../beside-services.sh) (services on `127.0.0.1:9000`, metrics on `:8090`; the gateway is stopped first so it drains while the services answer). The same services process runs beside the Elixir and Rust gateways in their conformance and measurement, and the web and iOS client tests run against the Go monolith.

`make ci` runs golangci-lint on the module and the unit tests: the shared argon2id vector, password hashing and malformed hashes, the device token layout, name and icon validation, the token bucket and ack coalescing, the push registry's reconciliation, request type labels, Debezium records of both outboxes, list membership flags from notices, hex Valkey keys, APNs signing, addressing, unregistered tokens and token reuse against a local server, the sealed content in message alerts and its omission when too large, key ids from file names, UUIDs and 63-bit ids, `.env` parsing and numeric variables, and the loopback check. Everything that needs the infrastructure is covered by the conformance tests instead.

## Measured

On 2026-10-11, from a fresh stack ([gateways](../../docs/results/2026-10-11-gateways.md)): the Go monolith held 4000 idle connections in 146 MiB and delivered 500 messages/s at 87 ms p50 and 277 ms p99 end to end; the split Go gateway held them in 167 MiB, at 94 / 574 ms and 454 ms of gateway CPU per thousand messages. Earlier runs: [Go](../../docs/results/2026-10-10-go.md), [Go against Java](../../docs/results/2026-10-10-go-vs-java.md), [idle memory per connection](../../docs/results/2026-10-10-idle-memory.md).

## Gaps

- A goroutine per connection, plus the one coder/websocket keeps for timeouts, sets the idle memory; the roadmap's second Go gateway on a poller-driven library would measure the alternative.
- `MessageFailedEvent` is never produced; the synchronous path has nothing to report with it.
- Acks are forwarded as they come, one write at a time per connection, not coalesced to once a minute as the design allows.
- Limits are per connection only; none per user or per address, so nothing slows down password guessing beyond the cost of the hash.
- Changing the password signs the other devices out, but their open connections keep working until they reconnect.
