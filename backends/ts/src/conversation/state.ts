// A conversation's row and what every change writes with it: the stored
// message, the sender's read mark and the outbox row.
import { randomBytes } from "node:crypto";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { EventSchema, MessageEventSchema, type Event } from "../gen/wrinkle/v1/event_pb.js";
import { MessageSchema, type Message, type Notice } from "../gen/wrinkle/v1/model_pb.js";
import { ErrorReason } from "../gen/wrinkle/v1/frame_pb.js";
import { notFound, permissionDenied, failedPrecondition } from "../internal/apierror.ts";
import { exec, now, row, timestampOf, type Querier } from "../internal/db.ts";
import { hex, randomId, type UUID } from "../internal/ids.ts";

// The kinds of conversation, as stored.
export const kindDirect = "direct";
export const kindGroup = "group";
export const kindNotes = "notes";

/** A conversation's row: what a change checks and fans out to. */
export interface State {
  kind: string;
  topic: string;
  /** Sorted bytewise. */
  members: UUID[];
  lastSeq: bigint;
  /** The current key epoch, 0 before the first. */
  keyEpoch: number;
  /** Whether the membership changed since the current epoch. */
  rotate: boolean;
  /** The history up to this seq is in the cold tier. */
  coldSeq: bigint;
}

export const compareIds = (a: UUID, b: UUID): number => Buffer.compare(a, b);

/** Whether the user is among the sorted members. */
export function has(members: UUID[], userId: UUID): boolean {
  let lo = 0;
  let hi = members.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    const c = compareIds(members[mid]!, userId);
    if (c === 0) return true;
    if (c < 0) lo = mid + 1;
    else hi = mid;
  }
  return false;
}

export const sorted = (members: UUID[]): UUID[] => [...members].sort(compareIds);

/** The ids concatenated, sorted bytewise. */
export const encodeMembers = (members: UUID[]): Buffer => Buffer.concat(sorted(members));

export function decodeMembers(b: Buffer): UUID[] {
  if (b.length % 16 !== 0) throw new Error(`members of ${b.length} bytes`);
  const members: UUID[] = [];
  for (let i = 0; i < b.length; i += 16) members.push(Buffer.from(b.subarray(i, i + 16)));
  return members;
}

export const without = (members: UUID[], userId: UUID): UUID[] => members.filter((id) => !id.equals(userId));

/** Marks that the next send needs a new key epoch, unless there is none yet: the first publish starts one anyway. */
export function membershipChanged(st: State): void {
  if (st.keyEpoch > 0) st.rotate = true;
}

export const selectState = `SELECT kind, topic, members, last_seq, key_epoch, rotate, cold_seq FROM conversation WHERE conversation_id = ?`;
export const selectStateForUpdate = selectState + ` FOR UPDATE`;

interface StateRow {
  kind: string;
  topic: string;
  members: Buffer;
  last_seq: bigint;
  key_epoch: number;
  rotate: number;
  cold_seq: bigint;
}

/** Reads the conversation's row with the given query. */
export async function load(q: Querier, query: string, conversationId: UUID): Promise<State> {
  const r = await row<StateRow>(q, query, [conversationId]);
  if (!r) throw notFound(`conversation ${hex(conversationId)} does not exist`);
  return {
    kind: r.kind,
    topic: r.topic,
    members: decodeMembers(r.members),
    lastSeq: r.last_seq,
    keyEpoch: r.key_epoch,
    rotate: Boolean(r.rotate),
    coldSeq: r.cold_seq,
  };
}

export const notInConversation = (userId: UUID, conversationId: UUID) =>
  permissionDenied(`user ${hex(userId)} is not in conversation ${hex(conversationId)}`);

/** Locks the conversation's row and checks that the user is a current member. */
export async function member(q: Querier, conversationId: UUID, userId: UUID): Promise<State> {
  const st = await load(q, selectStateForUpdate, conversationId);
  if (!has(st.members, userId)) throw notInConversation(userId, conversationId);
  return st;
}

/** Loads a group with the given query and checks that the user is a current member. */
export async function group(q: Querier, query: string, conversationId: UUID, userId: UUID): Promise<State> {
  const st = await load(q, query, conversationId);
  if (st.kind !== kindGroup)
    throw failedPrecondition(ErrorReason.NOT_A_GROUP, `conversation ${hex(conversationId)} is not a group`);
  if (!has(st.members, userId)) throw permissionDenied(`user ${hex(userId)} is not in group ${hex(conversationId)}`);
  return st;
}

/** Keeps only the message's content, or its deletion time, as message.body stores it. */
export const encodeBody = (msg: Pick<Message, "content" | "deletedAt">): Uint8Array =>
  toBinary(MessageSchema, create(MessageSchema, { content: msg.content, deletedAt: msg.deletedAt }));

/** Sets the message's content, or its deletion time, from a stored body. */
export function decodeBody(body: Uint8Array, msg: Message): void {
  let content: Message;
  try {
    content = fromBinary(MessageSchema, body);
  } catch (err) {
    throw new Error(`unmarshal message body: ${(err as Error).message}`, { cause: err });
  }
  msg.content = content.content;
  msg.deletedAt = content.deletedAt;
}

/** A message's attachment ids concatenated, as message.blob_ids stores them. */
export const encodeBlobIds = (blobIds: Uint8Array[]): Buffer => Buffer.concat(blobIds);

export function decodeBlobIds(b: Uint8Array | null): Uint8Array[] {
  const list: Uint8Array[] = [];
  if (!b) return list;
  for (let i = 0; i + 16 <= b.length; i += 16) list.push(Buffer.from(b.subarray(i, i + 16)));
  return list;
}

/** The client message id of a notice: random, so no device's id can collide with it. */
export const noticeId = (): string => "notice-" + randomBytes(8).toString("hex");

/**
 * Saves the message at st.lastSeq, moves the sender's read mark to it and
 * writes the outbox row for the recipients; the caller has moved the
 * conversation's counter.
 */
export async function storeMessage(
  q: Querier,
  conversationId: UUID,
  st: State,
  msg: Message,
  recipients: UUID[],
): Promise<void> {
  const sentAt = now();
  msg.seq = st.lastSeq;
  msg.sentAt = timestampOf(sentAt);
  const outboxId = randomId();
  await exec(
    q,
    `INSERT INTO message (conversation_id, seq, sender_user_id, client_message_id, body, blob_ids, sent_at, outbox_id)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
    [
      conversationId,
      msg.seq,
      Buffer.from(msg.senderUserId),
      msg.clientMessageId,
      Buffer.from(encodeBody(msg)),
      encodeBlobIds(msg.blobIds),
      sentAt,
      outboxId,
    ],
  );
  // Sending moves the sender's own read position, without a read event:
  // the lists move it with the message.
  await exec(
    q,
    `INSERT INTO read_mark (conversation_id, user_id, seq, updated_at) VALUES (?, ?, ?, ?)
     ON DUPLICATE KEY UPDATE seq = GREATEST(seq, ?), updated_at = ?`,
    [conversationId, Buffer.from(msg.senderUserId), msg.seq, sentAt, msg.seq, sentAt],
  );
  await insertOutbox(q, {
    conversationId,
    outboxId,
    recipients,
    st,
    kind: "message",
    event: event(msg.sentAt, { case: "message", value: create(MessageEventSchema, { message: msg }) }),
    at: sentAt,
  });
}

/**
 * Stores a change to a group, made by the sender, as the message at
 * st.lastSeq, with st as the group after the change, which the
 * conversation row takes on.
 */
export async function storeNotice(
  q: Querier,
  conversationId: UUID,
  st: State,
  senderId: UUID,
  notice: Notice,
  recipients: UUID[],
): Promise<void> {
  await exec(q, `UPDATE conversation SET topic = ?, members = ?, last_seq = ?, rotate = ? WHERE conversation_id = ?`, [
    st.topic,
    encodeMembers(st.members),
    st.lastSeq,
    st.rotate,
    conversationId,
  ]);
  const msg = create(MessageSchema, {
    conversationId,
    senderUserId: senderId,
    clientMessageId: noticeId(),
    content: { case: "notice", value: notice },
  });
  await storeMessage(q, conversationId, st, msg, recipients);
}

/** An event without a cursor, as an outbox row carries it. */
export const event = (occurredAt: Timestamp | undefined, body: Event["body"]): Event =>
  create(EventSchema, { occurredAt, body });

/** A change on its way to the fan-out: the event, who gets it, and the conversation as it is after the change. */
export interface OutboxRow {
  conversationId: UUID;
  outboxId: bigint;
  recipients: UUID[];
  st: State;
  kind: string;
  /** The outbox row of the message a deletion is about. */
  target?: bigint;
  /** A deleted message's sender and blob ids. */
  releasedUploads?: Buffer;
  event: Event;
  at: Date;
}

/**
 * Writes the row. The insert reaches the binlog, which is all the fan-out
 * reads; deleting the row in the same transaction keeps the table empty.
 */
export async function insertOutbox(q: Querier, r: OutboxRow): Promise<void> {
  await exec(
    q,
    `INSERT INTO outbox (conversation_id, outbox_id, recipients, conversation_kind, topic, member_count, kind, target_outbox_id, released_uploads, payload, created_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [
      r.conversationId,
      r.outboxId,
      Buffer.concat(r.recipients),
      r.st.kind,
      r.st.topic,
      r.st.members.length,
      r.kind,
      r.target ?? null,
      r.releasedUploads ?? null,
      Buffer.from(toBinary(EventSchema, r.event)),
      r.at,
    ],
  );
  await exec(q, `DELETE FROM outbox WHERE conversation_id = ? AND outbox_id = ?`, [r.conversationId, r.outboxId]);
}
