# Architecture

How Wrinkle is put together, independent of the language a backend is written in. The [README](../README.md) gives the short version; each backend's README describes its own code: [Go](../backends/go/README.md), [Java](../backends/java/README.md), [Elixir](../backends/elixir/README.md), [Rust](../backends/rust/README.md). The design is sized for the figures in [capacity.md](capacity.md), and design choices are weighed at that scale, even where a small deployment could do with less.

## Principles

- **Per-conversation sequence.** A conversation numbers its messages 1, 2, 3, ... in the transaction that stores each one. Every device orders a conversation by that number and can tell when it misses one.
- **Per-user event log.** Everything a user's devices need to learn, messages and read marks from all of the user's conversations, is appended to one log per user. A device keeps one cursor into it.
- **Push is an optimization, sync is the guarantee.** Connected devices get events as they are appended. A device that missed some reads them from its log after its cursor.
- **The contract is the boundary.** The device protocol in [`proto/wrinkle/v1`](../proto/wrinkle/v1) is the same for every backend, and the conformance tests in [`conformance/`](../conformance) hold each backend to it. Clients and the load generator work against any backend unchanged.

## Processes

| Process | Holds | State | Scales by |
|---|---|---|---|
| Gateway | devices' WebSocket connections | connections in memory; presence in Valkey | more gateways behind the device balancer |
| Services | conversation and device-sync request handling | none; all in the databases | more instances behind the services balancer |
| Fan-out | the outbox stream from Kafka | consumer offsets in Kafka | partitions of the topic, shared by one consumer group |
| Conversation DB | conversations, messages, read marks, the outbox | sharded by conversation id | Vitess shards |
| User DB | users, devices, event logs, conversation lists | sharded by user id | Vitess shards |
| Kafka and Debezium | committed outbox rows of both keyspaces, in commit order per conversation or user | the topics | partitions |
| Valkey | presence, push channels, gateway instance keys | in memory, rebuildable | cluster or more instances |
| Object storage | blobs: avatars, later stickers and attachments | Garage locally behind a proxy; S3 or GCS in production | the storage service |

The fan-out runs inside the services processes. A gateway, the services and the fan-out can also run as one process, the monolith, in which the gateway calls the services directly.

### Shapes

| Shape | What runs | Used for |
|---|---|---|
| Monolith | one process with every role (Go, Java) | the simplest run; conformance and measurement |
| Split | a gateway and a services process beside it, no balancers | conformance and measurement of a gateway (all four languages) |
| Side by side | any number of gateways and services instances on the host, two HAProxy balancers in Docker | the scaled shape, natively ([local-network.md](local-network.md)) |
| Cluster | the same in containers, counts per language and role | trying the scaled shape without native toolchains |

## Data model

Two Vitess keyspaces, each sharded by the id that every query of it carries, so a request touches one shard of each keyspace it needs. Three claim tables are sharded by what they are looked up by instead: names, pairs of users, and groups by their creator's client id. The schemas are in [`schema/`](../schema).

```mermaid
flowchart TB
  subgraph conv ["Conversation keyspace, by conversation_id"]
    c["conversation: kind, topic, current members, last_seq"]
    cm["conversation_member: role, joined and left seq"]
    p["conversation_pair: claim of a pair, by user_a"]
    g["group_claim: a group by creator and client id"]
    m["message: seq, sender, client id, content"]
    r["read_mark: per participant"]
    o["outbox: change events, deleted on insert"]
  end
  subgraph user ["User keyspace, by user_id"]
    u["user: name, icon, avatar, password hash, log_head"]
    n["user_name: claim of a name, by name"]
    d["device: token hash, acked position, push token"]
    e["user_event: the log, by position"]
    l["user_conversation: the list, with peers' profiles"]
    uo["user_outbox: profile and device changes, deleted on insert"]
    up["upload: blobs by uploader, used or not"]
  end
```

Ids:

- **User and conversation ids**: random UUIDs, 16 bytes. Nothing about a user or a conversation is derived from its name or members, so names change freely and a conversation can later have more than two members.
- **Device and outbox ids**: random 63-bit numbers.
- **Valkey keys** name a user by the lowercase hex of the id.

Claims make the lookups that ids no longer answer, each sharded by its own key, without a transaction across shards:

- **Names.** `user_name` maps a name to its user, unique ignoring case: the column's collation compares case-insensitively and its vindex hashes case-insensitively, so "Bob" and "bob" meet on one shard and collide. A claim counts only while its user's row still has that name; one left behind by an interrupted registration or rename is stale, and the next claimant takes it over. Registering writes the user first, then the claim; renaming claims the new name, commits the change, then releases the old one. Every step can be interrupted without leaving a name owned twice or a user without a valid claim to their current name.
- **Pairs.** `conversation_pair` maps two users, smaller id first bytewise, to their conversation, sharded by the first user. The first opener's insert claims the pair; both opening at once end up with the same conversation. A claim whose conversation row is missing is completed by the next open.

Go and Java implement both, and the password and token formats, alike (unit tests in each hold the same vectors), so both work on the same data; the Elixir and Rust gateways only carry ids.

## Groups

A conversation's kind is fixed when it is created. A direct conversation has its two participants for good and is found by the pair claim; a group is created with a topic and optional invitees, its creator as admin, and found again on a retry by the creator's client id. Any member adds people and changes the topic; admins remove members and make others admins; anyone leaves, and when the last admin leaves, the member who joined first becomes one. A group has at most 256 members.

Every change to a group is a notice: a message in the group's sequence, with the acting user as its sender, so every member sees the changes and the messages in one order, and devices sync them like any message. The conversation row holds the current members' ids, so a send checks membership and learns its recipients from the row it locks anyway; `conversation_member` keeps each member's role and the part of the sequence they may read, from the notice that added them to the one that removed them. A removed member keeps the history they had and gets nothing new.

Outbox rows carry their recipients and the conversation's kind, topic and member count as of the change, so a fan-out worker still needs no lookup, and each recipient's list entry follows the group: its topic, its size, and whether the user is still a member or an admin. Profile changes still reach direct partners only; clients refresh a group's members when they open it. A group message costs one log append per member, which is what caps the size: rooms with thousands of readers would need a read path per room instead (issue #9).

## Accounts

- **Register** takes a name, a password and a device name; **SignIn** the same for an existing account, matching the name ignoring case. Both return the device's credentials: user id, device id and a device token. Only they and Connect are allowed before Connect.
- **Passwords** are stored as argon2id hashes (19 MiB, 2 passes) in the standard string form, which records its parameters, so they can be raised later. A wrong name and a wrong password get the same answer, in the same time.
- **Device tokens** are `v1.` and the base64url of the user id, the device id and a 32-byte secret; the device row keeps a SHA-256 of the secret. Connect with a token is one indexed read on the user's shard, so the cost of a password is paid at sign-in only, not on every reconnect.
- **Devices** are listed, renamed and signed out with ListDevices, RenameDevice and SignOutDevice; **SignOut** signs out the calling device and **ChangePassword** every other one, clearing their tokens and push registrations. Each change goes through the user's outbox as a `DeviceEvent` to the user's own log, so the other devices' lists follow. For a sign-out the fan-out also publishes a sign-out on the user's push channel, and the gateways holding that device close its connections with 4001; the client then forgets its credentials.
- **DeleteAccount** checks the password, clears the profile and every device's token, marks the user deleted and frees the name. Its `ProfileEvent` reaches the conversation partners, who see a deleted account; the fan-out then signs out every device and removes the user's log, list, devices and uploads. The user row stays as a tombstone so that nothing is delivered to it again, and messages already sent stay with the conversations.
- **Profiles** are the name, one of a fixed set of icons and an optional uploaded picture, changed with UpdateProfile. A change goes through the user keyspace's outbox like a message through the conversation keyspace's: the fan-out appends a `ProfileEvent` to the user's own log and to the log of everyone they have a conversation with, updating each partner's conversation list in the same transaction.

## End-to-end encryption

Message content is encrypted on the sending device and decrypted on the recipients' devices; the services store and relay ciphertext and keys, never plaintext. The scheme is a key per conversation and epoch, wrapped for every member device, which is simple enough to follow end to end and works the same for two users and for groups; an established protocol with forward secrecy (MLS) could replace it behind the same requests.

- **Device keys.** Each device makes an X25519 key pair when it first connects and publishes the public key (`SetDeviceKey`, once). `GetDeviceKeys` is the directory: a user's signed-in devices and their keys. A change to a device with a key reaches the user's own devices, direct partners and group co-members as a `DeviceEvent`.
- **Epochs.** A member device makes an epoch key, wraps it for every member device (a key derived with HKDF from the X25519 secret of the two devices, so the wrap also proves which device made it), and publishes the wraps (`PublishKeys`) as the conversation's next epoch. Messages are sealed with AES-256-GCM under the current epoch's key, with the conversation, epoch, sender and client message id as associated data, so the services cannot move content between messages. The exact formats are in [`content.proto`](../proto/wrinkle/v1/content.proto), with test vectors that Go, the web client and iOS all check.
- **Rotation.** A membership change marks the conversation, and the services refuse sends until a member publishes the next epoch, so a removed member cannot read what follows. A device signing in or out changes the members' devices; a sending device notices that the current epoch's recipients differ and publishes a new epoch first. Sends with an older epoch are refused, so every device converges on the current one. Of two devices publishing the same epoch, the second is refused and uses the first's.
- **History on a new device.** The services cannot give a new device the old keys. When one of the user's devices sees the new device appear, it asks whether to share history and, if the user agrees, wraps every epoch key it holds for the new device. A server that added a device of its own to an account would therefore get nothing old without the user's consent.
- **Verification.** Each user's fingerprint covers all their device keys; two users compare a safety number derived from both. Clients remember a contact marked verified and warn when that contact's devices change.
- **What the services still see**: who talks to whom and when, sizes, group topics and membership notices (the services run groups with them), profiles, and avatars. Push notifications carry the sealed content when it fits; the iOS notification service extension decrypts it with the keys the app shares with it through an app group, and otherwise shows "New message".
- **Load.** The load generator encrypts and decrypts like a client, so its measurements include the cost.

## Blobs

Pictures, and later stickers and attachments, are blobs in object storage, never in the databases or through the WebSocket. A device asks the services for an upload slot with CreateUpload, giving the purpose, media type and size; the services record the upload in the user keyspace and answer with a path signed for an HTTP PUT of exactly that type and size (AWS signature version 4, by hand in Go and Java, with shared test vectors). The device uploads, then refers to the blob, as UpdateProfile does for an avatar; the services check that the blob is the user's and is there. Blobs are read with a plain GET by their random id and never change, so clients cache them for good.

Stickers are blobs too: a user uploads a small image and makes it a sticker, whose id is the blob's. Stickers are public and permanent, so anyone who receives one can show it from the blob address and save it to their palette, a private ordered list in the user keyspace synced to the user's devices with `PaletteEvent`s through the user outbox. Sending a sticker is a sealed message naming it, so the services do not know a sticker was sent; they do know who made which sticker and who saved it.

Locally the store is Garage, behind an HAProxy on loopback port 9200 that also takes `/blobs/` from the TLS edge, so devices use one address for the WebSocket and the blobs. Uploads are signed for a fixed host name that the proxy sets, whatever address a device used. Uploads nothing refers to are deleted by a sweeper in the fan-out a day after their slot was made; a replaced avatar goes back to unused and is deleted the same way.

## Flows

### Connect and sync

```mermaid
sequenceDiagram
  participant D as Device
  participant G as Gateway
  participant S as Services
  participant U as User DB
  opt first time on this device
    D->>G: SignIn(name, password) or Register
    G->>S: Call
    S->>U: name claim, password hash, new device
    S-->>D: user id, device id, device token
  end
  D->>G: Connect(device token)
  G->>S: Call
  S->>U: device by token
  S-->>G: user, device, acked cursor, log head
  G->>G: attach: subscribe, presence
  G-->>D: ConnectResponse
  D->>G: Sync(cursor)
  G->>S: Call
  S->>U: events after cursor
  S-->>D: events, next cursor
  D->>G: Ack(cursor)
```

Until Connect completes, the gateway answers anything but Register, SignIn and Connect with `UNAUTHENTICATED`, and closes a connection that has not connected within 5 seconds of opening or of its last Register or SignIn. The gateway answers Connect only once the user's push channel is subscribed, so no push that follows is missed; if that does not happen within 5 seconds (Valkey unreachable), Connect fails with `UNAVAILABLE` and may be retried. The response carries the cursor the device last acked and the head of the user's log, so a device knows at once how far behind it is. Sync pages through the log, up to 1000 events at a time.

### Sending a message

```mermaid
sequenceDiagram
  participant A as Sender
  participant G as Gateway
  participant S as Services
  participant C as Conversation DB
  participant K as Kafka
  participant F as Fan-out
  participant U as User DB
  participant V as Valkey
  participant B as Recipient
  A->>G: SendMessage
  G->>S: Call
  S->>C: lock conversation, seq = last_seq + 1, message, outbox
  S-->>A: Message with seq
  C-->>K: Debezium streams the outbox insert
  K->>F: record, keyed by conversation
  F->>U: append to each participant's log, update their lists
  F->>V: publish to each participant's channel
  V-->>G: event
  G-->>B: Event
```

1. **Store.** The services lock the conversation's row, take the next sequence number, insert the message and an outbox row, delete the outbox row again and commit: one local transaction on the conversation's shard. A resend with the same `client_message_id` finds the stored message and returns it instead of storing a second one.
2. **Capture.** Debezium reads the shard's change stream and publishes the outbox insert to Kafka, keyed by conversation id, and skips the delete. The row lives only within the transaction; the change stream is what carries it, so the event is published exactly when the transaction commits.
3. **Fan out.** A fan-out worker takes the record and, for each participant (the sender too, for their other devices), appends the event to the participant's log at the next position and updates their conversation list, in one transaction on the participant's shard.
4. **Push.** After the append, the worker publishes the event, now carrying its log position, to the participant's Valkey channel. Each gateway holding one of that user's devices is subscribed to the channel and writes the event to those connections.
5. **Notify.** For a recipient's devices that are not connected and have registered for push, the Go fan-out sends an APNs notification.

The sender's response returns after step 1; everything after it is asynchronous. End to end, send to receipt on a connected device, takes about 100 ms at the median in the measurements ([results](results/)).

### Read marks

`MarkRead` moves the user's read position in the conversation forward, never back, and writes an outbox row like a message; sending a message moves the sender's own position with it. In a direct conversation fan-out appends the read event to both participants' logs: the peer learns the message was read, and the user's other devices learn what was read elsewhere. In a group it goes to the reader and to the senders of the messages the read covers (among the newest 1000 of them), not to every member: each sender learns who has read their messages, which is what the clients show ("read by 2"), and a read costs about one append per sender instead of one per member. Clients get everyone's positions from `GetMembers` when they open a group.

### Deleting a message

A sender deletes their own message for everyone with `DeleteMessage`. The message keeps its seq, since sequences are dense and devices find gaps by them; its content is cleared and `deleted_at` set, in the conversation keyspace, with an outbox row that names the outbox row which carried the message. The fan-out appends a `MessageDeletedEvent` to each member's log and, in the same transaction, rewrites the member's earlier copy of the message (found by that outbox row, through the log's source key) and the conversation list's preview if it was the last message. A device syncing from an old cursor therefore never receives the text. Devices that are not connected get a background push, so the iOS app can withdraw a notification it already showed; nothing guarantees that.

### Reactions

A user's reactions to a message are one sealed set of up to three emoji, under the conversation's current key epoch, which `SetReaction` replaces as a whole or removes. The services keep one row per message and user in the conversation keyspace, with an outbox row like any change, so the fan-out appends a `ReactionEvent` to every current member's log in the conversation's order. Reactions take no seq and are never unread; `GetMessages` returns them with each message, and deleting a message deletes them. The services cannot see or count the emoji: clients decrypt and count.

### Acks

A device acks the cursor it has processed. The gateway writes it to the device's row, at most one write at a time per connection, keeping only the highest cursor that arrived meanwhile. The next Connect returns it, so a device that lost its local state can resume.

## Guarantees

- **Order within a conversation.** Sequence numbers are dense and assigned under the conversation's row lock. Debezium publishes a shard's changes in commit order, all of a conversation's records go to one Kafka partition, and a fan-out worker handles one conversation's records in order, so each participant's log has them in sequence order.
- **At least once, without duplicates.** A fan-out worker commits its Kafka offsets only after every record of a batch is in every log; a failed batch is fetched again. A redelivered record finds its source key (conversation id and outbox id) already in the log and is skipped.
- **Idempotent sends.** The client's message id makes a retried send return the original message.
- **Nothing lost to a missed push.** Pushes go only to devices connected at the moment; anything a device did not receive is in its log, and Sync from its cursor returns it.
- **Monotonic positions.** Read positions and acked cursors only move forward.

### Profile changes

```mermaid
sequenceDiagram
  participant A as User
  participant S as Services
  participant N as Name claims
  participant U as User DB
  participant K as Kafka
  participant F as Fan-out
  participant P as Partners' shards
  A->>S: UpdateProfile(name, icon)
  S->>N: claim the new name
  S->>U: user row, outbox row
  S->>N: release the old name
  S-->>A: User
  U-->>K: Debezium streams the outbox insert
  K->>F: record, keyed by user
  F->>U: append to the user's own log
  F->>P: per partner: append, update their list's entry
```

A user with many conversations makes one change fan out to many logs; the fan-out pages through the user's list and appends to several partners at once.

## Connections

Devices connect over TLS, terminated at the device balancer, since they send passwords; plain WebSocket is only for loopback. The rules every gateway enforces are in the contract, in [`frame.proto`](../proto/wrinkle/v1/frame.proto):

- Frames up to 64 KiB; Connect within 5 seconds of opening, or of the last Register or SignIn.
- Per connection, 50 frames a second on average in bursts of 100, and 32 requests in flight. Requests beyond the limits get `RESOURCE_EXHAUSTED`; other frames beyond them are dropped.
- WebSocket pings every 30 seconds; a connection silent for 90 seconds is closed.
- Each way a connection ends has a close code, and clients reconnect with backoff after any of them, then sync.
- A device that does not keep up with its pushed events is closed rather than buffered for; it syncs on reconnect.

### Presence

Each gateway process has an instance id, its node name and a random suffix, and keeps a Valkey key for it alive with a 90-second expiry, refreshed every 30 seconds. A user's presence hash maps each connected device to the instance holding it. A reader, the push notifier, counts a device as connected only while its instance key exists, so the devices of a gateway that died count as disconnected within 90 seconds and get notifications again. A disconnect removes the device's entry only if it still names this instance, so a device that has already reconnected elsewhere keeps its new entry.

Pushes use one Valkey channel per user; a gateway subscribes to a user's channel while it holds one of the user's devices.

### Drain

On SIGTERM a gateway fails its health check, so the balancer stops sending it connections, and refuses new ones itself. It closes every connection with 1001, which the clients take as a cue to reconnect, through the balancer to another gateway. It gives requests in flight up to 5 seconds and exits within 10.

## The internal RPC

A gateway that does not run the services in-process forwards each device request over gRPC ([`proto/wrinkle/backend/v1`](../proto/wrinkle/backend/v1)): the request as the device sent it, with the user and device the connection belongs to, and gets back the response as it goes to the device. The gateway owns the connection state: it enforces Connect first and remembers the identity Connect established. Acks have their own call. Pushes do not use the RPC; they go through Valkey.

Each gateway keeps one HTTP/2 connection to the services balancer, which spreads requests, not connections, over the services instances. A balancer that spread connections would pin each gateway to one instance.

## Failures

| What fails | Effect | Recovery |
|---|---|---|
| A gateway | its devices disconnect | they reconnect through the balancer to another gateway and sync; presence entries expire within 90 s |
| A services instance | requests in flight on it fail with `UNAVAILABLE` | the balancer drops it within 2 s; Kafka moves its fan-out partitions to the other instances |
| Kafka or Debezium | sends still succeed; nothing is fanned out | the stream resumes from where it was; the logs catch up and devices get the events then |
| Valkey | no pushes; presence unknown | devices still sync; when it is back, gateways resubscribe and announce themselves again, but devices that stayed connected have no presence entry until they reconnect, so they may get notifications meanwhile |
| APNs | no notifications | none needed; devices sync when opened |
| A conversation shard | sends and reads in its conversations fail | the shard's replica takes over (a real Vitess topology; the prototype runs one process) |
| A user shard | Connect, Sync and fan-out to its users fail | fan-out retries the batch until the shard is back |

The prototype's single Vitess process has one known weakness: after a restart on existing data the change stream does not resume until the stack is reset ([ROADMAP.md](../ROADMAP.md), "Later").

## Scaling

- **Connections** scale with gateways: each holds its devices in tens of KiB per connection ([capacity.md](capacity.md)), and the device balancer sends each new connection to the gateway with the fewest.
- **Requests** scale with services instances, which hold no state.
- **Writes** scale with shards. A send is one transaction on one conversation shard; its fan-out is one transaction per participant on their user shards.
- **Fan-out** scales with Kafka partitions, keyed by conversation, so different conversations proceed in parallel and each stays in order.
- **The database** is where a laptop runs out first: above about 500 messages a second the single Vitess process limits every backend ([ROADMAP.md](../ROADMAP.md), "Database ceiling").

Where the prototype departs from the design it was sized on, push routing through a channel per user and frequent acks, is listed in [capacity.md](capacity.md).

## Implementations

| | Gateway | Services and fan-out | Push |
|---|---|---|---|
| [Go](../backends/go/README.md) | goroutines, coder/websocket | database/sql, franz-go | APNs |
| [Java](../backends/java/README.md) | Armeria on Netty, virtual threads | HikariCP, the Kafka client | not yet |
| [Elixir](../backends/elixir/README.md) | Bandit, a process per connection | beside Go or Java services | through the services |
| [Rust](../backends/rust/README.md) | axum on tokio, a task per connection | beside Go or Java services | through the services |

Clients: the [web client](../clients/web) in React and TypeScript, the [iOS client](../clients/ios) in SwiftUI with local storage, and the [Go client library](../clients/go) the conformance tests and the load generator use.
