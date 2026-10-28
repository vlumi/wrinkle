# Architecture

How Wrinkle is put together, independent of the language a backend is written in. The [README](../README.md) gives the short version; each backend's README describes its own code: [Go](../backends/go/README.md), [Java](../backends/java/README.md), [TypeScript](../backends/ts/README.md), [Elixir](../backends/elixir/README.md), [Rust](../backends/rust/README.md). The design is sized for the figures in [capacity.md](capacity.md), and design choices are weighed at that scale, even where a small deployment could do with less.

In brief: devices hold a WebSocket to a gateway, which forwards their requests to stateless services. A change, such as a message, is stored in MySQL with an outbox row in one transaction; Debezium carries the committed outbox rows through Kafka to the fan-out, which appends events to each recipient's log and pushes them to the gateways holding their devices. A device reads what it missed from its log by cursor. The logs keep 30 days of events; message history is kept for good, the last 30 days in MySQL and older messages in HBase. Background work (the fan-out, the log trimmer, the history mover, the upload sweeper) runs in the services processes, the per-shard jobs each shard leased to one instance at a time.

## Principles

- **Per-conversation sequence.** A conversation numbers its messages 1, 2, 3, ... in the transaction that stores each one. Every device orders a conversation by that number and can tell when it misses one.
- **Per-user event log.** Everything a user's devices need to learn is appended to one log per user: messages, read marks, deletions and reactions from all of the user's conversations, and changes to the user's profile, devices and sticker palette (the event kinds in [`event.proto`](../proto/wrinkle/v1/event.proto)). A device keeps one cursor into it.
- **Push is an optimization, sync is the guarantee.** Connected devices get events as they are appended. A device that missed some reads them from its log after its cursor.
- **The contract is the boundary.** The device protocol in [`proto/wrinkle/v1`](../proto/wrinkle/v1) is the same for every backend, and the conformance tests in [`conformance/`](../conformance) hold each backend to it. Clients and the load generator work against any backend unchanged.

## Choices

- **Debezium over a relay.** The services could produce to Kafka themselves, from an outbox they poll. Producing straight from the request would be a dual write: a crash between the commit and the send loses the event. A relay that polls the outbox keeps the guarantee with fewer moving parts, and the order too: a conversation's rows commit in sequence order, since each send holds the conversation's row lock until it commits. Its costs are elsewhere. It must not keep a cursor by row id, or it skips a row with a lower id that commits after it read a higher one; so it publishes what remains and deletes it afterward, a second write per event where the change log needs none (the row is inserted and deleted in one transaction). Several relays on a shard must split the rows so that each conversation stays with one, delivery waits for the next poll, and it has to be written in every services language and follow resharding by itself. Reading the change log avoids all of that, which at the scale the design aims for outweighs the extra process; the roadmap has an experiment to measure the difference.
- **Requests balanced one at a time.** Each gateway keeps one HTTP/2 connection to the services. A balancer that spread connections would pin each gateway to one services instance, so the services balancer is HAProxy in HTTP/2 mode, which spreads requests. Client-side balancing in each gateway would save the hop but needs doing in five gRPC clients of uneven support.
- **Kubernetes for the backends only, and optional.** The stateless tier also runs on Docker Desktop's Kubernetes (`make k8s-up`), beside the Compose cluster and with the same images; the stateful infrastructure stays in Compose, where its data and settings are simplest locally. Kubernetes' Services balance connections, so HAProxy still spreads the gateways' requests over the services pods, finding them through headless Services. Measurements stay native: both clusters run in Docker Desktop's virtual machine.
- **A key per conversation and epoch** for end-to-end encryption, rather than an established protocol: simple enough to follow end to end and the same for two users and for groups. MLS could replace it behind the same requests ([below](#end-to-end-encryption)).

## Processes

| Process | Holds | State | Scales by |
|---|---|---|---|
| Gateway | devices' WebSocket connections | connections in memory; presence in Valkey | more gateways behind the device balancer |
| Services | the conversation service and the sync service | none; all in the databases | more instances behind the services balancer |
| Fan-out | the outbox stream from Kafka | consumer offsets in Kafka | partitions of the topic, shared by one consumer group |
| Per-shard jobs | the log trimmer and the history mover, a loop per shard | a lease per shard in Valkey | shards, shared among the services instances |
| Conversation DB | conversations, messages, read marks, reactions, wrapped keys, the outbox | sharded by conversation id | Vitess shards |
| User DB | users, devices, event logs, conversation lists, uploads, stickers and palettes | sharded by user id | Vitess shards |
| Kafka and Debezium | committed outbox rows of both keyspaces, in commit order per conversation or user | the topics | partitions |
| Valkey | presence, push channels, gateway instance keys, leases of the per-shard work | in memory, rebuildable | cluster or more instances |
| Object storage | blobs: avatars, stickers, encrypted attachments | Garage locally behind a proxy; S3 or GCS in production | the storage service |
| History cold tier | messages older than the hot window | HBase, standalone locally; an HBase cluster or Bigtable in production | regions, split by conversation id |

The services are two, split by keyspace. The conversation service owns the conversation keyspace: sends, read marks, groups, deletions, reactions and key epochs. The sync service owns the user keyspace: accounts, devices and profiles, sync and conversation lists, uploads, stickers and push tokens; the code splits it further (accounts, uploads and stickers are packages of their own in Go, Java and TypeScript). The fan-out runs inside the services processes, and so do the background jobs: the upload sweeper, and per shard the log trimmer and the history mover (with the fan-out role in Go and Java, wherever the services run in TypeScript). Each shard has its own loop, by default every minute (`WRINKLE_LOG_TRIM_INTERVAL_S`, `WRINKLE_COLD_MOVE_INTERVAL_S`), and a round needs the shard's lease in Valkey (`lease:<job>:<keyspace>/<shard>`, 30 s, renewed every 10 s, dropped after the round), so several instances share the shards instead of repeating each other's work, and the shards of one that stops are taken over within 30 s. A round that loses its lease stops; repeating one is harmless anyway. The jobs find the shards through vtgate and reach each over a connection to that shard alone; on the `mysql` profile a keyspace is one shard. A gateway, the services and the fan-out can also run as one process, the monolith, in which the gateway calls the services directly.

### Shapes

| Shape | What runs | Used for |
|---|---|---|
| Monolith | one process with every role (Go, Java, TypeScript) | the simplest run; conformance and measurement |
| Split | a gateway and a services process beside it, no balancers | conformance and measurement of a gateway (all five languages; Elixir and Rust beside Go services) |
| Side by side | any number of gateways and services instances on the host, two HAProxy balancers in Docker | the scaled shape, natively ([local-network.md](local-network.md)) |
| Cluster | the same in containers, counts per language and role | trying the scaled shape without native toolchains |

## Data model

Two Vitess keyspaces, each sharded by the id that every query of it carries, so a request touches one shard of each keyspace it needs. Four tables are sharded by something else: the three claim tables by what they are looked up by (names; pairs of users by the first user; groups by their creator), and `sticker` by the sticker's id, since stickers are public and outlive their creator. The schemas are in [`schema/`](../schema). On the infrastructure's `mysql` profile (`make infra-up PROFILE=mysql`) the same schemas run unsharded on one MySQL, a database per keyspace, with Debezium's MySQL connector reading the binlog; a backend that finds no vtgate treats each keyspace as one shard, and nothing else changes.

```mermaid
flowchart TB
  subgraph conv ["Conversation keyspace, by conversation_id"]
    c["conversation: kind, topic, current members, last_seq, key_epoch, rotate, cold_seq"]
    cm["conversation_member: role, joined and left seq"]
    ck["conversation_key: an epoch key wrapped for one recipient device"]
    p["conversation_pair: claim of a pair, by user_a"]
    g["group_claim: a group by creator and client id"]
    m["message: seq, sender, client id, sealed content, blob ids"]
    rx["reaction: one sealed set per message and user"]
    r["read_mark: per participant"]
    o["outbox: change events with their recipients, deleted on insert"]
  end
  subgraph user ["User keyspace, by user_id"]
    u["user: name, icon, avatar, password hash, log_head, log_tail"]
    n["user_name: claim of a name, by name"]
    d["device: token hash, public key, acked position, push token"]
    e["user_event: the log, by position"]
    l["user_conversation: the list, with peers' profiles"]
    uo["user_outbox: profile, device, palette and contact changes, deleted on insert"]
    up["upload: blobs by uploader, used or not"]
    s["sticker: creator and media type, by sticker_id"]
    ps["palette_sticker: the user's palette, in order"]
    ct["contact: the user's own side toward another, a request or a block"]
    cp["contact_peer: the other's side, copied by the fan-out"]
  end
  subgraph cold ["HBase, by conversation_id and seq"]
    h["message: the history up to cold_seq, with its reactions"]
  end
  m -- "moved past the hot window" --> h
```

Ids:

- **User and conversation ids**: random UUIDs, 16 bytes. Nothing about a user or a conversation is derived from its name or members, so names change freely and a conversation can have any members.
- **Blob and sticker ids**: random UUIDs too; a sticker's id is its blob's.
- **Device and outbox ids**: random 63-bit numbers.
- **Valkey keys** name a user by the lowercase hex of the id.

Claims make the lookups that ids no longer answer, each sharded by its own key, without a transaction across shards:

- **Names.** `user_name` maps a name to its user, unique ignoring case: the column's collation compares case-insensitively and its vindex hashes case-insensitively, so "Bob" and "bob" meet on one shard and collide. A claim counts only while its user's row still has that name; one left behind by an interrupted registration or rename is stale, and the next claimant takes it over. Registering writes the user first, then the claim; renaming claims the new name, commits the change, then releases the old one. Every step can be interrupted without leaving a name owned twice or a user without a valid claim to their current name.
- **Pairs.** `conversation_pair` maps two users, smaller id first bytewise, to their conversation, sharded by the first user. The first opener's insert claims the pair; both opening at once end up with the same conversation. A claim whose conversation row is missing is completed by the next open. A user's notes are the pair of the user with themselves, so two devices opening them at once end up with the same notes too.
- **Groups.** `group_claim` maps a creator and the client's group id to the group, so a retried CreateGroup finds the group the first attempt made.

Go, Java and TypeScript implement all of this, and the password and token formats, alike (unit tests in each hold the same vectors), so all three work on the same data; the Elixir and Rust gateways only carry ids.

## Accounts

- **Register** takes a name, a password and a device name; **SignIn** the same for an existing account, matching the name ignoring case. Both return the device's credentials: user id, device id and a device token. Only they and Connect are allowed before Connect.
- **Passwords** are stored as argon2id hashes (19 MiB, 2 passes) in the standard string form, which records its parameters, so they can be raised later. A wrong name and a wrong password get the same answer, in the same time.
- **Device tokens** are `v1.` and the base64url of the user id, the device id and a 32-byte secret; the device row keeps a SHA-256 of the secret. Connect with a token is one indexed read on the user's shard, so the cost of a password is paid at sign-in only, not on every reconnect.
- **Devices** are listed, renamed and signed out with ListDevices, RenameDevice and SignOutDevice; **SignOut** signs out the calling device and **ChangePassword** every other one, clearing their tokens and push registrations. Each change goes through the user's outbox as a `DeviceEvent` to the user's own log, so the other devices' lists follow. For a sign-out the fan-out also publishes a sign-out on the user's push channel, and the gateways holding that device close its connections with 4001; the client then forgets its credentials.
- **DeleteAccount** checks the password, clears the profile and every device's token, marks the user deleted and frees the name. Its `ProfileEvent` reaches the conversation partners, who see a deleted account; the fan-out then signs out every device and removes the user's log, list, devices and palette, and releases the avatar for the sweeper. The user row stays as a tombstone so that nothing is delivered to it again, and messages already sent stay with the conversations, with their attachments; stickers stay public.
- **Profiles** are the name, one of a fixed set of icons and an optional uploaded picture, changed with UpdateProfile. A change goes through the user keyspace's outbox like a message through the conversation keyspace's: the fan-out appends a `ProfileEvent` to the user's own log and to the log of everyone they have a direct conversation with, updating each partner's conversation list in the same transaction ([flow](#profile-changes)).

## Groups

A conversation's kind is fixed when it is created. A direct conversation has its two participants for good and is found by the pair claim; a user's notes have the user alone, for good, are found by the user's pair with themselves, and are created by the first `OpenNotes`; the clients show them pinned at the top of the list before they exist. Everything else a direct conversation does works in the notes, with the keys wrapped for the user's own devices, and since nobody is notified of their own messages, no push is sent; a group is created with a topic and optional invitees, its creator as admin, and found again on a retry by the creator's client id. Any member adds people and changes the topic; admins remove members and make others admins; anyone leaves, and when the last admin leaves, the member who joined first becomes one. A group has at most 256 members.

Every change to a group is a notice: a message in the group's sequence, with the acting user as its sender, so every member sees the changes and the messages in one order, and devices sync them like any message. The conversation row holds the current members' ids, so a send checks membership and learns its recipients from the row it locks anyway; `conversation_member` keeps each member's role and the part of the sequence they may read, from the notice that added them to the one that removed them. A removed member keeps the history they had and gets nothing new.

Outbox rows carry their recipients and the conversation's kind, topic and member count as of the change, so a fan-out worker still needs no lookup, and each recipient's list entry follows the group: its topic, its size, and whether the user is still a member or an admin. Profile changes reach direct partners only; clients refresh a group's members when they open it. A group message costs one log append per member, which is what caps the size: rooms with thousands of readers would need a read path per room instead (issue #9).

## Contacts and blocking

Contacts are mutual: two users are contacts when each asked the other, or one asked and the other accepted. A group takes only its adder's contacts, so nobody lands in a group of strangers; anyone may still message anyone directly. A block refuses the blocked user in everything one-to-one: opening or writing to the direct conversation, reacting there, asking to be a contact, adding the blocker to a group. The refusal is a plain permission error, and the blocked user's view of the blocker is the same as of anyone else. Neither side's read receipts reach the other while the block lasts, and neither may publish keys for the other: a read tells only the reader's own devices, and a user's own devices still share the conversation's history among themselves. Shared groups stay as they are for everyone else; the blocker's clients hide the blocked user's messages and reactions, and the fan-out sends the blocker no push for them. Blocking ends any contact or request between the two, and unblocking does not bring a contact back.

Each user owns their side toward another (a request or a block) in `contact` on their own shard, and only their own requests change it, through their outbox, as profile changes go. The fan-out copies each side to the other user's `contact_peer`, so a user's contacts and requests both ways are one local read, and when a side ends or turns into a block, it ends the other user's request through the other user's outbox. No request writes another user's shard, and every change to a side is ordered by its owner's outbox, so redelivered or reordered copies are recognized by their outbox id. Each change reaches both users as a `ContactEvent` with their own view. The checks that refuse never use the copies: they read the other user's own row by its primary key, so a block holds from the moment it commits.

## End-to-end encryption

Message content is encrypted on the sending device and decrypted on the recipients' devices; the services store and relay ciphertext and wrapped keys, never plaintext. Each conversation has key epochs: an epoch key made by a member device and wrapped for every member device, and a new epoch whenever the set of recipients changes.

```mermaid
sequenceDiagram
  participant A as Alice's phone
  participant S as Services
  participant B as Bob's device
  participant L as Alice's new laptop
  A->>S: SetDeviceKey(X25519 public key), once per device
  B->>S: SetDeviceKey
  A->>S: GetConversationKeys
  S-->>A: no epoch yet
  A->>S: GetDeviceKeys(members)
  A->>S: PublishKeys(epoch 1, wrapped for every member device)
  A->>S: SendMessage(sealed with epoch 1)
  S->>S: epoch is current, no rotation pending
  S-->>B: MessageEvent, through the log and a push
  B->>S: GetConversationKeys(after epoch 0)
  S-->>B: epoch 1 wrapped for this device, by Alice's phone
  Note over A,B: A member added or removed: the services mark the conversation, and sends fail until a member publishes epoch 2. A device signed in or out: the next sender sees that the recipients differ and publishes epoch 2 first.
  L->>S: SignIn, SetDeviceKey
  S-->>A: DeviceEvent (a new device with a key)
  A->>A: asks Alice whether to share history
  A->>S: PublishKeys(epochs 1 to n, wrapped for the laptop only)
  L->>S: GetConversationKeys(after epoch 0)
  S-->>L: every epoch wrapped for the laptop
```

Each device makes an X25519 key pair when it first connects and publishes the public key with `SetDeviceKey`, once; a device with a new key is a new device. `GetDeviceKeys` is the directory: a user's signed-in devices and their keys. A change to a device with a key reaches the user's own devices, direct partners and group co-members as a `DeviceEvent`.

A member device starts an epoch by making a random key, wrapping it for every member device, and publishing the wraps with `PublishKeys` as the conversation's next epoch. The wrapping key is derived with HKDF from the X25519 secret shared by the two devices, so a wrap also proves which device made it. Messages are sealed with AES-256-GCM under the current epoch's key, with the conversation, epoch, sender and client message id as associated data, so the services cannot move content between messages. Reactions and attachments are sealed the same way, with their own associated data. The exact formats are in [`content.proto`](../proto/wrinkle/v1/content.proto), with test vectors that Go, the web client and iOS all check.

Every send is checked against the epoch. The conversation row holds the current epoch and a rotation flag: a membership change sets the flag, and the services refuse sends until a member publishes the next epoch, so a removed member cannot read what follows. Sends sealed with an older epoch are refused too, so every device converges on the current one. The services do not know which devices an epoch was wrapped for beyond the recipients they stored, so a device signing in or out is left to the senders: before each send a device compares the current epoch's recipients with the members' devices and, when they differ, publishes a new epoch first. Of two devices publishing the same epoch, the second is refused and uses the first's.

The services cannot give a new device the old keys. When one of the user's devices sees the new device appear, it asks whether to share history and, if the user agrees, wraps every epoch key it holds for the new device; the new device learns of it when it next opens the conversation or reconnects. A server that added a device of its own to an account would therefore get nothing old without the user's consent. Each user's fingerprint covers all their device keys; two users compare a safety number derived from both, and clients remember a contact marked verified and warn when that contact's devices change.

What the services still see: who talks to whom and when, sizes, group topics and membership notices (the services run groups with them), profiles and avatars, and who made and saved which sticker. Push notifications carry the sealed content when it fits; the iOS notification service extension decrypts it with the keys the app shares with it through an app group, and otherwise shows "New message". There is no forward secrecy between rotations: a device key that leaks reads every epoch wrapped for it. MLS (RFC 9420) would add that and could replace the scheme behind the same requests. The load generator encrypts and decrypts like a client, so its measurements include the cost.

## Blobs

Avatars, stickers and attachments are blobs in object storage, never in the databases or through the WebSocket. A device asks the services for an upload slot with CreateUpload, giving the purpose, media type and size; the services record the upload in the user keyspace and answer with a path signed for an HTTP PUT of exactly that type and size (AWS signature version 4, by hand in Go, Java and TypeScript, with shared test vectors). The device uploads, then refers to the blob, as UpdateProfile does for an avatar; the services check that the blob is the user's and is there. Blobs are read with a plain GET by their random id and never change, so clients cache them for good.

Locally the store is Garage, behind an HAProxy on loopback port 9200 that also takes `/blobs/` from the TLS edge, so devices use one address for the WebSocket and the blobs. Uploads are signed for a fixed host name that the proxy sets, whatever address a device used. Uploads nothing refers to are deleted by a sweeper in the services processes a day after their slot was made; a replaced avatar, or an attachment of a deleted message, goes back to unused and is deleted the same way, and so is an attachment a send took whose message was never delivered.

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

Until Connect completes, the gateway answers anything but Register, SignIn and Connect with `UNAUTHENTICATED`, and closes a connection that has not connected within 5 seconds of opening or of its last successful Register or SignIn; a connection may make five attempts that reach a password hash. An account has at most 20 signed-in devices. The gateway answers Connect only once the user's push channel is subscribed, so no push that follows is missed; if that does not happen within 5 seconds (Valkey unreachable), Connect fails with `UNAVAILABLE` and may be retried. The response carries the cursor the device last acked and the head of the user's log, so a device knows at once how far behind it is. Sync pages through the log, up to 1000 events at a time. Every list a device reads is paged, and a page also ends before 512 KiB, so no frame from the server passes 1 MiB, which every client accepts: Sync and GetMessages say `has_more`, ListConversations returns a token for the next page, GetConversationKeys at most 100 keys. A refusal of `FAILED_PRECONDITION` or `RESOURCE_EXHAUSTED` carries a reason, such as a stale key epoch or a full group, which clients act on rather than on the message.

The log is for delivery, not the archive: it keeps events for 30 days (`WRINKLE_LOG_RETENTION_S`), and the conversations keep the history. A trimmer, one of the per-shard jobs ([Processes](#processes)), drops older events a page at a time on each shard of the user keyspace, oldest first through an index on the events' time. For each user in a page, one transaction raises `user.log_tail` to the newest position dropped and deletes the events up to it, so a log stays contiguous. Sync reads the tail after the events, and a cursor before it fails with `CURSOR_EXPIRED`: either the events read were all there, or the drop came first and the tail shows it, so a device never gets a log with a hole. A device whose cursor expired starts again as one without history: from the head given by Connect, then the conversation list, contacts and history fetched again, which leaves nothing in between unseen.

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
  A->>G: SendMessage(sealed with epoch e)
  G->>S: Call
  S->>C: lock conversation, check e is current and no rotation pending, seq = last_seq + 1, message, outbox
  S-->>A: Message with seq
  C-->>K: Debezium streams the outbox insert
  K->>F: record, keyed by conversation
  F->>U: append to each participant's log, update their lists
  F->>V: publish to each participant's channel
  V-->>G: event
  G-->>B: Event
```

1. **Store.** The services lock the conversation's row and check the sender is a member and the content is sealed with the row's current key epoch while no rotation is pending (`FAILED_PRECONDITION` otherwise, and the device publishes a new epoch and retries). Then they take the next sequence number, insert the message and an outbox row naming the recipients, delete the outbox row again and commit: one local transaction on the conversation's shard. A resend with the same `client_message_id` finds the stored message and returns it instead of storing a second one.
2. **Capture.** Debezium reads the shard's change stream and publishes the outbox insert to Kafka, keyed by conversation id, and skips the delete. The row lives only within the transaction; the change stream is what carries it, so the event is published exactly when the transaction commits.
3. **Fan out.** A fan-out worker takes the record and, for each recipient (the sender too, for their other devices), appends the event to the recipient's log at the next position and updates their conversation list, in one transaction on the recipient's shard. Copies of messages bound for one shard share that transaction: while one runs, the next gathers the copies of every record being delivered meanwhile, so under load a few statements carry many messages, and a batch that fails is appended copy by copy.
4. **Push.** After the append, the worker publishes the event, now carrying its log position, to the recipient's Valkey channel. Each gateway holding one of that user's devices is subscribed to the channel and writes the event to those connections.
5. **Notify.** For a recipient's devices that are not connected and have registered for push, the fan-out sends an APNs notification.

The sender's response returns after step 1; everything after it is asynchronous. End to end, send to receipt on a connected device, took 61 to 68 ms at the median at 500 messages a second, and 103 to 181 ms at 1000, in the last results ([2026-10-24](results/2026-10-24-shapes.md)).

### Read marks

`MarkRead` moves the user's read position in the conversation forward, never back, and writes an outbox row like a message; sending a message moves the sender's own position with it. In a direct conversation the fan-out appends the read event to both participants' logs: the peer learns the message was read, and the user's other devices learn what was read elsewhere. In a group it goes to the reader and to the senders of the messages the read covers (among the newest 1000 of them), not to every member: each sender learns who has read their messages, which is what the clients show ("read by 2"), and a read costs about one append per sender instead of one per member. Clients get everyone's positions from `GetMembers` when they open a group.

### Deleting a message

A sender deletes their own message for everyone with `DeleteMessage`. The message keeps its seq, since sequences are dense and devices find gaps by them; its body is replaced by one holding only `deleted_at` and its blob ids are cleared, in the conversation keyspace, with an outbox row that names the outbox row which carried the message, and the message's attachments. The fan-out appends a `MessageDeletedEvent` to each member's log and, in the same transaction, rewrites the member's earlier copy of the message (found by that outbox row, through the log's source key) and the conversation list's preview if it was the last message; the attachments named in the outbox row go back to unused for the sweeper.

What is guaranteed: once the deletion is in a member's log, no Sync and no GetMessages returns the content again, from any backend. What is not: a device that already holds the content keeps it until it processes the deletion event, which is the client's job, and a notification already shown stays shown. For the latter, devices that are not connected get a background push so the iOS app can withdraw the alert, but iOS delivers background pushes at its discretion and the backend does not retry them.

### Reactions

A user's reactions to a message are one sealed set of up to three emoji, under the conversation's current key epoch, which `SetReaction` replaces as a whole or removes. The services keep one row per message and user in the conversation keyspace, with an outbox row like any change, so the fan-out appends a `ReactionEvent` to every current member's log in the conversation's order. Reactions take no seq and are never unread; `GetMessages` returns them with each message, and deleting a message deletes them. The services cannot see or count the emoji: clients decrypt and count, and nobody is notified of a reaction, since the services cannot say what it was.

### History and the cold tier

Message history is kept for good, but only the last 30 days of it (`WRINKLE_HISTORY_HOT_S`) in MySQL; older messages move to HBase, in a table keyed by conversation id and seq, so a conversation's history is one range ([schema](../schema/hbase/schema.rb)). A row holds the message's columns and its reactions as one cell, and every write puts a whole row, so any write can be repeated. `conversation.cold_seq` is the watermark: the history up to it is in HBase, the rest in MySQL.

A mover, one of the per-shard jobs ([Processes](#processes)), pages, on each shard of the conversation keyspace, through the messages by age, through an index on their time, and takes each conversation once: up to 200 messages at a time in seq order, it puts the messages to HBase, then locks the conversation's row, reads them again, puts again any deleted or reacted to meanwhile, raises `cold_seq` and deletes them from MySQL in one transaction, stopping at the first message within the window. Sends to the conversation never wait on HBase, only on that recheck. A failure after the puts leaves copies of rows still in MySQL, which the next move puts again.

GetMessages reads MySQL first, then the watermark, then HBase below the lowest seq MySQL gave. The watermark only rises, and what it covers is in HBase before it does, so nothing falls between the two reads; a page whose messages the watermark has passed meanwhile is read again, for their reactions. A point read that misses in MySQL, the conversation list's last message, tries HBase. Deleting and reacting take the conversation's row lock as before and, for a message at or below `cold_seq`, rewrite its HBase row before the transaction commits, putting the old row back if it does not; the outbox row is written in MySQL as before, so the events and the fan-out do not change. What stays in MySQL: a resend is recognized while its original is there, and a group read event tells the senders found there. Go and Java use HBase's native clients; Node has none maintained, so TypeScript goes through HBase's REST server, whose scanners read forward only, which works because a conversation's seqs in HBase have no gaps.

### Attachments

Attachments are blobs the sending device encrypts first: each file gets its own key, and the device uploads only the ciphertext, through the blob proxy, before sending. The message's sealed content carries each file's key, the ciphertext's digest, its name, type and size, and for images a small thumbnail, all made on the device, since the services cannot read the file to resize it. The message also lists the blob ids in clear: the services check that each is the sender's own, unused attachment upload, mark them used before storing the message and release them again if the send then fails, keep them while the message exists, and release them when it is deleted, for the sweeper. Uploads span keyspaces and requests, so the fan-out, which handles every committed change at least once, settles them: a send's uploads stay pending until the fan-out delivers the message, so those of a send that failed without its release are swept a day later: an upload records the message it was taken for, and the sweeper deletes a pending one only when that message is not in MySQL while it would still be there. A deletion's outbox row names the uploads the fan-out releases. Recipients download a blob by id, check the digest, decrypt and cache it.

### Stickers

A user uploads a small image and makes it a sticker with `CreateSticker`; the sticker's id is the blob's. Stickers are public and permanent, so anyone who receives one can show it from the blob address and save it to their palette, a private ordered list of up to 200 in the user keyspace, synced to the user's devices with `PaletteEvent`s through the user outbox. Sending a sticker is a sealed message naming it, so the services do not know a sticker was sent; they do know who made which sticker and who saved it.

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
  A->>S: UpdateProfile(name, icon, avatar)
  S->>N: claim the new name
  S->>U: user row, outbox row
  S->>N: release the old name
  S-->>A: User
  U-->>K: Debezium streams the outbox insert
  K->>F: record, keyed by user
  F->>U: append to the user's own log
  F->>P: per partner: append, update their list's entry
```

A user with many conversations makes one change fan out to many logs; the fan-out pages through the user's list and appends to several partners at once. Device changes take the same path, to the user's own log and, for a device with a key, to the partners and group co-members who encrypt for it.

### Acks

A device acks the cursor it has processed; the gateway writes it to the device's row, and the next Connect returns it, so a device that lost its local state can resume. Every gateway forwards a connection's acks at most once a minute (`WRINKLE_ACK_INTERVAL_MS`), and at once when the connection ends, so acks cost about one write per connected device per minute however often the device acks. The first ack after a quiet minute is written at once; later ones within the minute only raise the cursor held, which is written when the minute since the last write is up, or when the connection ends, after any write in flight, and a drain waits for those writes as for requests. A gateway that crashes loses at most a minute of acks; that matters only to a device that lost its local state, which then syncs those events again.

## Guarantees

- **Order within a conversation.** Sequence numbers are dense and assigned under the conversation's row lock. Debezium publishes a shard's changes in commit order, all of a conversation's records go to one Kafka partition, and a fan-out worker handles one conversation's records in order, so each participant's log has them in sequence order.
- **At least once, without duplicates.** A fan-out worker commits its Kafka offsets only after every record of a batch is in every log; a failed batch is fetched again. A redelivered record finds its source key (conversation id and outbox id) already in the log and is skipped.
- **Idempotent sends.** The client's message id makes a retried send return the original message, while the original is within the hot window.
- **Nothing lost to a missed push.** Pushes go only to devices connected at the moment; anything a device did not receive is in its log, and Sync from its cursor returns it, or, after the log's retention, fails with `CURSOR_EXPIRED` and the device rebuilds from the conversations.
- **Monotonic positions.** Read positions and acked cursors only move forward.
- **A removed member reads nothing new.** Sends are refused until the next epoch, which is wrapped for current members only, and the member's log gets no further events of the group.

## Connections

Devices connect over TLS, terminated at the edge, since they send passwords; plain WebSocket is only for loopback. The rules every gateway enforces are in the contract, in [`frame.proto`](../proto/wrinkle/v1/frame.proto):

- Frames up to 64 KiB; Connect within 5 seconds of opening, or of the last Register or SignIn.
- Per connection, 50 protobuf frames a second on average in bursts of 100, and 32 requests in flight. Requests beyond the limits get `RESOURCE_EXHAUSTED`; other frames beyond them are dropped. WebSocket control frames are not counted.
- WebSocket pings every 30 seconds; a connection silent for 90 seconds is closed.
- Each way a connection ends has a close code, and clients reconnect with backoff after any of them but 4001 (signed out), then sync.
- A device that does not keep up with its pushed events is closed (1013, with 32 events queued) rather than buffered for; it syncs on reconnect.

### Presence

Each gateway process has an instance id, its node name and a random suffix, and keeps a Valkey key for it alive with a 90-second expiry, refreshed every 30 seconds. A user's presence hash maps each connected device to the instance holding it. A reader, the push notifier, counts a device as connected only while its instance key exists, so the devices of a gateway that died count as disconnected within 90 seconds and get notifications again. A disconnect removes the device's entry only if it still names this instance, so a device that has already reconnected elsewhere keeps its new entry.

Pushes use one Valkey channel per user; a gateway subscribes to a user's channel while it holds one of the user's devices.

### Drain

On SIGTERM a gateway fails its health check, so the balancer stops sending it connections, and refuses new ones itself. It closes every connection with 1001, which the clients take as a cue to reconnect, through the balancer to another gateway. It gives requests in flight up to 5 seconds and exits within 10.

## The internal RPC

A gateway that does not run the services in-process forwards each device request over gRPC ([`proto/wrinkle/backend/v1`](../proto/wrinkle/backend/v1)): the request as the device sent it, with the user and device the connection belongs to, and gets back the response as it goes to the device. The gateway owns the connection state: it enforces Connect first and remembers the identity Connect established. Acks have their own call. Pushes do not use the RPC; they go through Valkey.

Each gateway keeps one HTTP/2 connection to the services balancer, which spreads requests, not connections, over the services instances ([Choices](#choices)).

## Failures

| What fails | Effect | Recovery |
|---|---|---|
| A gateway | its devices disconnect | they reconnect through the balancer to another gateway and sync; presence entries expire within 90 s |
| A services instance | requests in flight on it fail with `UNAVAILABLE` | the balancer drops it within 2 s; Kafka moves its fan-out partitions to the other instances, and its shard leases expire within 30 s for others to take |
| Kafka or Debezium | sends still succeed; nothing is fanned out | the stream resumes from where it was; the logs catch up and devices get the events then |
| Valkey | no pushes; presence unknown; log trimming and history moves pause; Connect fails with `UNAVAILABLE`, since a device's push channel cannot be set up | connected devices still sync, and connecting ones retry; when it is back, gateways resubscribe and announce themselves again, but devices that stayed connected have no presence entry until they reconnect, so they may get notifications meanwhile |
| APNs | no notifications | none needed; devices sync when opened |
| Object storage | uploads and downloads fail; sends with attachments fail | messages without attachments are unaffected; clients retry |
| HBase | a page of history reaching below the watermark, or a change to a cold message, fails with `UNAVAILABLE`; the mover stops | sends and the conversation lists are unaffected; devices retry; the mover catches up |
| A conversation shard | sends and reads in its conversations fail | the shard's replica takes over (a real Vitess topology; the prototype runs one process) |
| A user shard | Connect, Sync and fan-out to its users fail; and, since a fan-out worker retries a whole fetch, it delivers to no one while its fetches hold records for that shard, which with many shards is almost every fetch | the worker retries until the shard is back; isolating a shard is open ([ROADMAP.md](../ROADMAP.md), "The fan-out's blast radius") |

The prototype's single Vitess process has one known weakness: it starts empty when its container restarts, losing the data; `make infra-up` then resets the connectors' positions so the change stream resumes on the new database ([local-network.md](local-network.md#troubleshooting)).

## Scaling

- **Connections** scale with gateways: each holds its devices in tens of KiB per connection ([capacity.md](capacity.md)), and the device balancer sends each new connection to the gateway with the fewest.
- **Requests** scale with services instances, which hold no state.
- **Writes** scale with shards. A send is one transaction on one conversation shard; its fan-out is one transaction per participant on their user shards.
- **Fan-out** scales with Kafka partitions, keyed by conversation, so different conversations proceed in parallel and each stays in order.
- **Background work** scales with shards: each shard's trim and move runs on its own, and the services instances share the shards through the leases.
- **The database** is where a laptop runs out first: at about 1000 messages a second the single Vitess process limits every backend; plain MySQL in its place halves the latency below the limit and takes some runs to 2000 ([results](results/2026-10-28-mysql.md); [ROADMAP.md](../ROADMAP.md), "Database ceiling").

Where the prototype departs from the design it was sized on, push routing through a channel per user and frequent acks, is listed in [capacity.md](capacity.md).

## Implementations

| | Gateway | Services and fan-out | Push |
|---|---|---|---|
| [Go](../backends/go/README.md) | goroutines, coder/websocket | database/sql, franz-go, gohbase | APNs |
| [Java](../backends/java/README.md) | Armeria on Netty, virtual threads | HikariCP, the Kafka client, the HBase client | APNs |
| [TypeScript](../backends/ts/README.md) | ws on Node, one event loop | mysql2, librdkafka, HBase's REST server | APNs |
| [Elixir](../backends/elixir/README.md) | Bandit, a process per connection | beside Go, Java or TypeScript services | through the services |
| [Rust](../backends/rust/README.md) | axum on tokio, a task per connection | beside Go, Java or TypeScript services | through the services |

Clients: the [web client](../clients/web) in React and TypeScript, the [iOS client](../clients/ios) in SwiftUI with local storage, and the [Go client library](../clients/go) the conformance tests and the load generator use.
