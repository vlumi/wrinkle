// Appending events to users' logs, with the conversation list updates that
// go with them, and pushing what was stored.
import { clone, create, fromBinary, toBinary } from "@bufbuild/protobuf";
import type { Pool, PoolConnection } from "mysql2/promise";
import { PushSchema, type Push } from "../gen/wrinkle/backend/v1/services_pb.js";
import {
  EventSchema,
  MessageEventSchema,
  type Event,
  type MessageDeletedEvent,
  type ReadEvent,
} from "../gen/wrinkle/v1/event_pb.js";
import { MessageSchema, UserSchema, type Message, type User } from "../gen/wrinkle/v1/model_pb.js";
import { exec, isDuplicate, nullBytes, row, tx } from "../internal/db.ts";
import { parseUUID, sameId, type UUID } from "../internal/ids.ts";
import { log, type Logger } from "../internal/log.ts";
import * as metrics from "../internal/metrics.ts";
import { ListedCache } from "./listed.ts";
import { isDirect, peerOf, type OutboxRow } from "./outbox.ts";
import { secondsSince, sqlTime } from "./sqltime.ts";

export interface Publisher {
  publish(userId: UUID, payload: Uint8Array): Promise<void>;
}

/**
 * Told about messages and deletions appended to a user's log, to push them
 * to devices that are not connected. In a group, a user who blocked the
 * sender gets no push; the sender of a deletion is undefined when it is not
 * known or not needed.
 */
export interface Notifier {
  messageDelivered(userId: UUID, msg: Message, group: boolean): void;
  messageDeleted(userId: UUID, deleted: MessageDeletedEvent, senderId: UUID | undefined): void;
}

/** Changes the user's conversation list in the transaction that appends the event. */
export type ListUpdate = (c: PoolConnection, event: Event) => Promise<void>;

export interface Stored {
  event: Event;
  payload: Uint8Array;
}

export class Delivery {
  readonly db: Pool;
  /** The conversation keyspace, for the members of groups. */
  readonly conversations: Pool;
  readonly pub: Publisher;
  readonly notifier: Notifier | undefined;
  readonly log: Logger;
  readonly listed = new ListedCache();

  constructor(db: Pool, conversations: Pool, pub: Publisher, notifier: Notifier | undefined, log: Logger) {
    this.db = db;
    this.conversations = conversations;
    this.pub = pub;
    this.notifier = notifier;
    this.log = log;
  }

  /** Appends a conversation's event to one recipient's log, then pushes it. */
  async deliver(userId: UUID, o: OutboxRow): Promise<void> {
    const entry = new ListEntry(userId, o);
    if (!isDirect(o) && o.event.body.case === "messageDeleted") entry.deletedSender = { id: undefined };
    if (isDirect(o)) {
      entry.peerId = peerOf(o, userId);
      if (o.event.body.case === "message") {
        entry.peer = await this.peerForNewEntry(userId, o.conversationId, entry.peerId);
      }
    }
    const event = await this.append(userId, o.conversationId, o.outboxId, o.event, (c, e) => entry.update(c, e));
    if (!event) return;
    this.messageDelivered(userId, o, event);
    if (event.body.case === "messageDeleted" && this.notifier) {
      this.notifier.messageDeleted(userId, event.body.value, entry.deletedSender?.id);
    }
  }

  /** Notes a message's list entry as existing and tells the notifier. */
  messageDelivered(userId: UUID, o: OutboxRow, event: Event): void {
    if (event.body.case !== "message") return;
    const msg = event.body.value.message;
    if (!msg) return;
    this.listed.add(userId, o.conversationId);
    this.notifier?.messageDelivered(userId, msg, !isDirect(o));
  }

  /**
   * The peer's current profile when the user's list may not have the
   * conversation yet, for the entry a message creates. Once an entry is
   * known to exist, it is never looked up again.
   */
  async peerForNewEntry(userId: UUID, conversationId: UUID, peerId: UUID): Promise<User | undefined> {
    if (this.listed.has(userId, conversationId)) return undefined;
    const listed = await row(this.db, "SELECT 1 FROM user_conversation WHERE user_id = ? AND conversation_id = ?", [
      userId,
      conversationId,
    ]);
    if (listed) {
      this.listed.add(userId, conversationId);
      return undefined;
    }
    return this.profile(peerId);
  }

  /** A user's current profile; one that does not exist has only its id. */
  async profile(userId: UUID): Promise<User> {
    const r = await row<{ name: string; icon: string; avatar: Buffer | null; deleted_at: Date | null }>(
      this.db,
      "SELECT name, icon, avatar, deleted_at FROM user WHERE user_id = ?",
      [userId],
    );
    return create(UserSchema, {
      userId,
      name: r?.name ?? "",
      icon: r?.icon ?? "",
      avatar: r?.avatar ?? new Uint8Array(),
      deleted: r ? r.deleted_at !== null : false,
    });
  }

  /**
   * Adds the event to the user's log, in one transaction with the list
   * update, and publishes it. It returns the event as stored, or undefined
   * when there is nothing to push: the user does not exist or is deleted, or
   * the log already has the event.
   */
  append(
    userId: UUID,
    sourceId: UUID,
    outboxId: bigint,
    source: Event,
    update?: ListUpdate,
  ): Promise<Event | undefined> {
    return this.appendBuilt(userId, sourceId, outboxId, source, undefined, update);
  }

  /**
   * append with the event's body completed by build, in the transaction and
   * under the lock on the user row, before it is stored.
   */
  async appendBuilt(
    userId: UUID,
    sourceId: UUID,
    outboxId: bigint,
    source: Event,
    build: ListUpdate | undefined,
    update: ListUpdate | undefined,
  ): Promise<Event | undefined> {
    const stored = await this.store(userId, sourceId, outboxId, source, build, update);
    if (!stored) return undefined;
    await this.delivered(userId, stored.event, stored.payload);
    return stored.event;
  }

  /** Counts an event stored in the user's log and publishes it. */
  async delivered(userId: UUID, event: Event, payload: Uint8Array): Promise<void> {
    metrics.fanoutDeliveries.inc();
    metrics.fanoutLag.observe(secondsSince(event.occurredAt));
    await this.publish(userId, create(PushSchema, { kind: { case: "event", value: payload } }));
  }

  /** Sends a push to the gateways holding the user's devices. A lost event is caught up on the next sync. */
  async publish(userId: UUID, push: Push): Promise<void> {
    try {
      await this.pub.publish(userId, toBinary(PushSchema, push));
    } catch (err) {
      this.log.warn("publish failed", { user: userId, err });
    }
  }

  private store(
    userId: UUID,
    sourceId: UUID,
    outboxId: bigint,
    source: Event,
    build: ListUpdate | undefined,
    update: ListUpdate | undefined,
  ): Promise<Stored | undefined> {
    return tx(this.db, async (c) => {
      const user = await row<{ log_head: bigint; deleted_at: Date | null }>(
        c,
        "SELECT log_head, deleted_at FROM user WHERE user_id = ? FOR UPDATE",
        [userId],
      );
      if (!user) {
        this.log.warn("no such user, dropping event", { user: userId });
        return undefined;
      }
      if (user.deleted_at !== null) return undefined;
      const event = clone(EventSchema, source);
      event.cursor = user.log_head + 1n;
      if (build) await build(c, event);
      const payload = toBinary(EventSchema, event);
      if (!(await appendEvent(c, userId, sourceId, outboxId, event, payload))) return undefined;
      if (update) await update(c, event);
      return { event, payload };
    });
  }
}

/**
 * Adds the event at its cursor and moves the log head. The unique key on the
 * source outbox row makes a redelivery fail the insert; it reports false then.
 */
async function appendEvent(
  c: PoolConnection,
  userId: UUID,
  sourceId: UUID,
  outboxId: bigint,
  event: Event,
  payload: Uint8Array,
): Promise<boolean> {
  try {
    await exec(
      c,
      `INSERT INTO user_event (user_id, position, source_id, outbox_id, payload, occurred_at)
       VALUES (?, ?, ?, ?, ?, ?)`,
      [userId, event.cursor, sourceId, outboxId, Buffer.from(payload), sqlTime(event.occurredAt)],
    );
  } catch (err) {
    if (isDuplicate(err)) return false;
    throw err;
  }
  await exec(c, "UPDATE user SET log_head = ? WHERE user_id = ?", [event.cursor, userId]);
  return true;
}

/** A change to a membership flag of a list entry, when set. */
export interface Flag {
  set: boolean;
  value: boolean;
}

const unset: Flag = { set: false, value: false };
const flag = (value: boolean): Flag => ({ set: true, value });

/** What a group's notice changes about the recipient's place in it. */
export function membership(userId: UUID, msg: Message): { member: Flag; admin: Flag } {
  const is = (u: User | undefined) => u !== undefined && sameId(Buffer.from(u.userId), userId);
  const sender = sameId(Buffer.from(msg.senderUserId), userId);
  if (msg.content.case !== "notice") return { member: unset, admin: unset };
  const n = msg.content.value.kind;
  switch (n.case) {
    case "groupCreated":
      return { member: flag(true), admin: flag(sender) };
    case "membersAdded":
      if (n.value.users.some(is)) return { member: flag(true), admin: flag(false) };
      break;
    case "memberRemoved":
      if (is(n.value.user)) return { member: flag(false), admin: flag(false) };
      break;
    case "memberLeft":
      if (sender) return { member: flag(false), admin: flag(false) };
      if (is(n.value.promoted)) return { member: unset, admin: flag(true) };
      break;
    case "adminChanged":
      if (is(n.value.user)) return { member: unset, admin: flag(n.value.admin) };
      break;
    default:
      break;
  }
  return { member: unset, admin: unset };
}

/** The body a list entry keeps of a message: its content and attachments. */
export const listBody = (msg: Message): Buffer =>
  Buffer.from(toBinary(MessageSchema, create(MessageSchema, { content: msg.content, blobIds: msg.blobIds })));

/** A recipient's place in the conversation list for the row being delivered. */
export class ListEntry {
  readonly userId: UUID;
  readonly o: OutboxRow;
  /** The other user of a direct conversation. */
  peerId: UUID | undefined;
  /** The peer's profile when the entry may be new, undefined otherwise. */
  peer: User | undefined;
  /** Receives the sender of a group message being deleted, from the user's copy of it, for the notifier. */
  deletedSender: { id: UUID | undefined } | undefined;

  constructor(userId: UUID, o: OutboxRow) {
    this.userId = userId;
    this.o = o;
  }

  async update(c: PoolConnection, event: Event): Promise<void> {
    switch (event.body.case) {
      case "message":
        if (event.body.value.message) await this.recordMessage(c, event.body.value.message, event);
        return;
      case "read":
        return this.recordRead(c, event.body.value);
      case "messageDeleted":
        return this.recordDeletion(c, event.body.value);
      default:
        return;
    }
  }

  /**
   * Adds the conversation to the user's list, or makes the message its last
   * unless a later one is already there; the group's topic, size and the
   * user's membership follow the latest message the same way. The sender's
   * own read position moves to the message. A new direct entry takes the
   * peer's profile; an existing one keeps what profile changes put there.
   */
  private async recordMessage(c: PoolConnection, msg: Message, event: Event): Promise<void> {
    const { userId, peer, o } = this;
    const ownSeq = sameId(Buffer.from(msg.senderUserId), userId) ? msg.seq : 0n;
    const peerColumn = isDirect(o) ? this.peerId : null;
    const body = listBody(msg);
    const { member, admin } = membership(userId, msg);
    const seq = msg.seq;
    const sender = Buffer.from(msg.senderUserId);
    const sentAt = sqlTime(msg.sentAt);
    const occurredAt = sqlTime(event.occurredAt);
    await exec(
      c,
      `INSERT INTO user_conversation
         (user_id, conversation_id, kind, peer_user_id, peer_name, peer_icon, peer_avatar, peer_deleted,
          topic, member_count, member, admin, last_seq,
          last_sender_user_id, last_client_message_id, last_body, last_sent_at,
          read_seq, peer_read_seq, updated_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
       ON DUPLICATE KEY UPDATE
         last_seq = GREATEST(last_seq, ?),
         topic = IF(? >= last_seq, ?, topic),
         member_count = IF(? >= last_seq, ?, member_count),
         member = IF(? AND ? >= last_seq, ?, member),
         admin = IF(? AND ? >= last_seq, ?, admin),
         last_sender_user_id = IF(? >= last_seq, ?, last_sender_user_id),
         last_client_message_id = IF(? >= last_seq, ?, last_client_message_id),
         last_body = IF(? >= last_seq, ?, last_body),
         last_sent_at = IF(? >= last_seq, ?, last_sent_at),
         read_seq = GREATEST(read_seq, ?),
         updated_at = GREATEST(updated_at, ?)`,
      [
        userId,
        o.conversationId,
        o.conversationKind,
        peerColumn ?? null,
        peer?.name ?? "",
        peer?.icon ?? "",
        nullBytes(peer?.avatar),
        peer?.deleted ?? false,
        o.topic,
        o.memberCount,
        !member.set || member.value,
        admin.value,
        seq,
        sender,
        msg.clientMessageId,
        body,
        sentAt,
        ownSeq,
        occurredAt,
        seq,
        seq,
        o.topic,
        seq,
        o.memberCount,
        member.set,
        seq,
        member.value,
        admin.set,
        seq,
        admin.value,
        seq,
        sender,
        seq,
        msg.clientMessageId,
        seq,
        body,
        seq,
        sentAt,
        ownSeq,
        occurredAt,
      ],
    );
  }

  /** Moves the user's own read position or, in a direct conversation, the peer's, whichever the event is about. */
  private async recordRead(c: PoolConnection, read: ReadEvent): Promise<void> {
    let column = "peer_read_seq";
    if (sameId(Buffer.from(read.userId), this.userId)) column = "read_seq";
    else if (!isDirect(this.o)) return;
    await exec(
      c,
      `UPDATE user_conversation SET ${column} = GREATEST(${column}, ?)
       WHERE user_id = ? AND conversation_id = ?`,
      [read.seq, this.userId, this.o.conversationId],
    );
  }

  /**
   * Drops the deleted message's content from the copy in the user's log, so
   * that a sync from before it never returns the content, and from the list
   * entry when it is the last message, keeping the entry's place in the list.
   */
  private async recordDeletion(c: PoolConnection, deleted: MessageDeletedEvent): Promise<void> {
    const { userId, o } = this;
    const copy = await row<{ position: bigint; payload: Buffer }>(
      c,
      "SELECT position, payload FROM user_event WHERE user_id = ? AND source_id = ? AND outbox_id = ? FOR UPDATE",
      [userId, o.conversationId, o.targetOutboxId],
    );
    if (copy) {
      let event: Event;
      try {
        event = fromBinary(EventSchema, copy.payload);
      } catch (err) {
        // Retrying would not mend it; the copy becomes a bare tombstone.
        log.warn("unreadable copy of a deleted message", { user: userId, position: copy.position, err });
        event = create(EventSchema, {
          body: {
            case: "message",
            value: create(MessageEventSchema, {
              message: create(MessageSchema, { conversationId: o.conversationId, seq: deleted.seq }),
            }),
          },
        });
      }
      const msg = event.body.case === "message" ? event.body.value.message : undefined;
      if (msg) {
        if (this.deletedSender) this.deletedSender.id = parseUUID(msg.senderUserId);
        msg.content = { case: undefined };
        msg.blobIds = [];
        msg.deletedAt = deleted.deletedAt;
        await exec(c, "UPDATE user_event SET payload = ? WHERE user_id = ? AND position = ?", [
          Buffer.from(toBinary(EventSchema, event)),
          userId,
          copy.position,
        ]);
      }
    }
    const body = Buffer.from(toBinary(MessageSchema, create(MessageSchema, { deletedAt: deleted.deletedAt })));
    await exec(
      c,
      `UPDATE user_conversation SET last_body = ?
       WHERE user_id = ? AND conversation_id = ? AND last_seq = ?`,
      [body, userId, o.conversationId, deleted.seq],
    );
  }
}
