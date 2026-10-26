// The conversation keyspace's services: direct conversations and notes,
// sending, read marks and history, with its cold tier. Every change writes
// an outbox row in the same transaction, which change data capture carries
// to the fan-out.
import { create } from "@bufbuild/protobuf";
import type { Pool } from "mysql2/promise";
import { ReadEventSchema } from "../gen/wrinkle/v1/event_pb.js";
import { MessageSchema, SealedSchema, type Message, type Sealed } from "../gen/wrinkle/v1/model_pb.js";
import { checkMessaging } from "../contacts/contacts.ts";
import { ApiError, failedPrecondition, invalidArgument } from "../internal/apierror.ts";
import {
  exec,
  isDuplicate,
  now,
  row,
  rows,
  timedRow,
  timedRows,
  timestampOf,
  tx,
  type DbTime,
  type Querier,
} from "../internal/db.ts";
import { newUUID, parseUUID, randomId, type UUID } from "../internal/ids.ts";
import type { Deps } from "../services/deps.ts";
import { releaseAttachments, useAttachments } from "../uploads/uploads.ts";
import { coldMessage, type Cold } from "./cold.ts";
import { DirectMembers } from "./direct.ts";
import { addReactions } from "./reaction.ts";
import {
  decodeBlobIds,
  decodeBody,
  encodeMembers,
  event,
  has,
  insertOutbox,
  kindDirect,
  kindGroup,
  kindNotes,
  load,
  member,
  notInConversation,
  selectState,
  storeMessage,
  type State,
} from "./state.ts";

const maxClientMessageId = 64;
export const nonceBytes = 12;
const maxCiphertext = 48 << 10;
export const maxBlobIds = 10;
const defaultPageSize = 50;
const maxPageSize = 500;
// Bounds the messages a group read event looks at for the senders to tell.
const readSendersWindow = 1000;
const maxUint64 = (1n << 64n) - 1n;

/** What the conversation services work with. */
export interface Conversations {
  deps: Deps;
  direct: DirectMembers;
}

export const newConversations = (deps: Deps): Conversations => ({ deps, direct: new DirectMembers() });

/**
 * Finds or creates the direct conversation between two users, userA <
 * userB. The pair row, on userA's shard, decides between two opening at
 * once; a pair whose conversation row is missing gets it here.
 */
export const open = (db: Querier, userA: UUID, userB: UUID): Promise<UUID> => openPair(db, kindDirect, userA, userB);

/** Finds or creates the user's notes: the pair of the user with themselves. */
export const openNotes = (db: Querier, userId: UUID): Promise<UUID> => openPair(db, kindNotes, userId, userId);

async function openPair(db: Querier, kind: string, userA: UUID, userB: UUID): Promise<UUID> {
  let conversationId = await pair(db, userA, userB);
  if (!conversationId) {
    const id = newUUID();
    try {
      await exec(
        db,
        `INSERT INTO conversation_pair (user_a, user_b, conversation_id, created_at) VALUES (?, ?, ?, ?)`,
        [userA, userB, id, now()],
      );
      conversationId = id;
    } catch (err) {
      if (!isDuplicate(err)) throw err;
      conversationId = await pair(db, userA, userB);
      if (!conversationId) throw new Error("conversation pair missing after a duplicate key", { cause: err });
    }
  }
  const members = userA.equals(userB) ? [userA] : [userA, userB];
  const at = now();
  await exec(
    db,
    `INSERT IGNORE INTO conversation (conversation_id, kind, topic, members, last_seq, created_at)
     VALUES (?, ?, '', ?, 0, ?)`,
    [conversationId, kind, encodeMembers(members), at],
  );
  for (const id of members) {
    await exec(
      db,
      `INSERT IGNORE INTO conversation_member (conversation_id, user_id, admin, joined_seq, left_seq, joined_at)
       VALUES (?, ?, FALSE, 1, NULL, ?)`,
      [conversationId, id, at],
    );
  }
  return conversationId;
}

async function pair(db: Querier, userA: UUID, userB: UUID): Promise<UUID | undefined> {
  const r = await row<{ conversation_id: Buffer }>(
    db,
    `SELECT conversation_id FROM conversation_pair WHERE user_a = ? AND user_b = ?`,
    [userA, userB],
  );
  return r?.conversation_id;
}

/** Checks a message's sealed content. */
export function checkSealed(sealed: Sealed | undefined, maxBytes: number): void {
  if (sealed?.nonce.length !== nonceBytes) throw invalidArgument(`sealed.nonce must be ${nonceBytes} bytes`);
  const n = sealed.ciphertext.length;
  if (n === 0 || n > maxBytes) throw invalidArgument(`sealed.ciphertext must be 1 to ${maxBytes} bytes`);
}

/** Only the contract's fields of sealed content. */
export const copySealed = (s: Sealed): Sealed =>
  create(SealedSchema, { epoch: s.epoch, nonce: s.nonce, ciphertext: s.ciphertext });

/** Fails unless the sealed content uses the current key epoch, and none is current while keys must rotate. */
export function checkEpoch(st: State, epoch: number): void {
  if (st.keyEpoch === 0 || epoch !== st.keyEpoch)
    throw failedPrecondition(`stale key epoch: the current one is ${st.keyEpoch}`);
  if (st.rotate) throw failedPrecondition(`keys must rotate: the membership changed since epoch ${st.keyEpoch}`);
}

interface MessageRow {
  seq: bigint;
  sender_user_id: Buffer;
  client_message_id: string;
  body: Buffer;
  blob_ids: Buffer | null;
  sent_at: DbTime;
}

/** The message of a stored row. */
export function toMessage(conversationId: UUID, r: MessageRow): Message {
  const msg = create(MessageSchema, {
    conversationId,
    seq: r.seq,
    senderUserId: r.sender_user_id,
    clientMessageId: r.client_message_id,
    sentAt: timestampOf(r.sent_at),
    blobIds: decodeBlobIds(r.blob_ids),
  });
  decodeBody(r.body, msg);
  return msg;
}

/**
 * Stores a sealed message, assigning the next sequence number. Resending
 * with the same client message id returns the message stored the first
 * time; otherwise the content must be sealed with the current key epoch,
 * and none is current while the conversation needs a new one. The
 * attachments' uploads are taken into use first, in the user keyspace, and
 * released again if the send then fails. An upload goes with one message
 * only.
 */
export async function send(
  cs: Conversations,
  userId: UUID,
  conversationId: UUID,
  clientMessageId: string,
  sealed: Sealed | undefined,
  blobIds: Uint8Array[],
): Promise<Message> {
  const bytes = Buffer.byteLength(clientMessageId, "utf8");
  if (bytes === 0 || bytes > maxClientMessageId)
    throw invalidArgument(`client_message_id must be 1 to ${maxClientMessageId} bytes`);
  checkSealed(sealed, maxCiphertext);
  const blobs = parseBlobIds(blobIds);
  await checkBlocks(cs, userId, conversationId);
  const db = cs.deps.conversations;
  let used = false;
  if (blobs.length > 0) used = await takeAttachments(cs.deps, userId, conversationId, clientMessageId, blobs);
  try {
    return await tx(db, async (c) => {
      const st = await member(c, conversationId, userId);
      const stored = await timedRow<MessageRow>(
        c,
        `SELECT seq, sender_user_id, client_message_id, body, blob_ids, sent_at FROM message
         WHERE conversation_id = ? AND sender_user_id = ? AND client_message_id = ?`,
        [conversationId, userId, clientMessageId],
      );
      if (stored) return toMessage(conversationId, stored);
      checkEpoch(st, sealed!.epoch);
      const msg = create(MessageSchema, {
        conversationId,
        senderUserId: userId,
        clientMessageId,
        content: { case: "sealed", value: copySealed(sealed!) },
        blobIds: blobs,
      });
      st.lastSeq++;
      await exec(c, `UPDATE conversation SET last_seq = ? WHERE conversation_id = ?`, [st.lastSeq, conversationId]);
      await storeMessage(c, conversationId, st, msg, st.members);
      return msg;
    });
  } catch (err) {
    // A release that fails leaves the uploads used; the send's own error is what the device hears.
    if (used) await releaseAttachments(cs.deps, userId, blobs).catch(() => undefined);
    throw err;
  }
}

/**
 * Refuses a message or reaction in a direct conversation when either user
 * blocked the other. The row is read without a lock, before anything is
 * taken into use; the change's transaction checks membership again.
 */
export async function checkBlocks(cs: Conversations, userId: UUID, conversationId: UUID): Promise<void> {
  let entry = cs.direct.get(conversationId);
  if (!entry) {
    const st = await load(cs.deps.conversations, selectState, conversationId);
    if (st.kind === kindDirect || st.kind === kindNotes) cs.direct.add(conversationId, st.kind, st.members);
    entry = { members: st.members, direct: st.kind === kindDirect };
  }
  if (!entry.members.some((id) => id.equals(userId))) throw notInConversation(userId, conversationId);
  if (!entry.direct) return;
  const peer = entry.members.find((id) => !id.equals(userId));
  if (peer) await checkMessaging(cs.deps.users, userId, peer);
}

/** Checks a message's attachment ids: at most ten distinct ones. */
export function parseBlobIds(blobIds: Uint8Array[]): UUID[] {
  if (blobIds.length > maxBlobIds) throw invalidArgument(`blob_ids must have at most ${maxBlobIds} ids`);
  const list: UUID[] = [];
  for (const b of blobIds) {
    const id = parseUUID(b);
    if (!id) throw invalidArgument("blob_ids must be 16 bytes each");
    if (list.some((x) => x.equals(id))) throw invalidArgument("blob_ids must be distinct");
    list.push(id);
  }
  return list;
}

/** Takes the attachments' uploads into use, unless the message is a resend of one already stored; whether it did. */
async function takeAttachments(
  deps: Deps,
  userId: UUID,
  conversationId: UUID,
  clientMessageId: string,
  blobIds: UUID[],
): Promise<boolean> {
  const r = await row(
    deps.conversations,
    `SELECT 1 FROM message WHERE conversation_id = ? AND sender_user_id = ? AND client_message_id = ?`,
    [conversationId, userId, clientMessageId],
  );
  if (r) return false;
  await useAttachments(deps, userId, conversationId, clientMessageId, blobIds);
  return true;
}

/**
 * Moves the user's read position forward. Moving it backwards or to where
 * it is does nothing. In a group, the read event goes to the reader and to
 * the senders of the messages it covers; across a block, either way, only
 * to the reader.
 */
export async function markRead(cs: Conversations, userId: UUID, conversationId: UUID, seq: bigint): Promise<void> {
  if (seq === 0n) throw invalidArgument("seq must be positive");
  let blocked = false;
  try {
    await checkBlocks(cs, userId, conversationId);
  } catch (err) {
    if (!(err instanceof ApiError)) throw err;
    blocked = true;
  }
  await tx(cs.deps.conversations, async (c) => {
    const st = await member(c, conversationId, userId);
    if (seq > st.lastSeq) throw invalidArgument(`seq ${seq} is beyond the last message ${st.lastSeq}`);
    const mark = await row<{ seq: bigint }>(
      c,
      `SELECT seq FROM read_mark WHERE conversation_id = ? AND user_id = ? FOR UPDATE`,
      [conversationId, userId],
    );
    const current = mark?.seq ?? 0n;
    if (current >= seq) return;
    const at = now();
    await exec(
      c,
      `INSERT INTO read_mark (conversation_id, user_id, seq, updated_at) VALUES (?, ?, ?, ?)
       ON DUPLICATE KEY UPDATE seq = ?, updated_at = ?`,
      [conversationId, userId, seq, at, seq, at],
    );
    const recipients =
      st.kind === kindGroup
        ? await readRecipients(c, st, conversationId, userId, current, seq)
        : blocked
          ? [userId]
          : st.members;
    await insertOutbox(c, {
      conversationId,
      outboxId: randomId(),
      recipients,
      st,
      kind: "read",
      event: event(timestampOf(at), {
        case: "read",
        value: create(ReadEventSchema, { conversationId, userId, seq }),
      }),
      at,
    });
  });
}

/** The reader and the current members among the senders of the newest messages in (from, to]. */
async function readRecipients(
  q: Querier,
  st: State,
  conversationId: UUID,
  readerId: UUID,
  from: bigint,
  to: bigint,
): Promise<UUID[]> {
  const senders = await rows<{ sender_user_id: Buffer }>(
    q,
    `SELECT DISTINCT sender_user_id FROM (
       SELECT sender_user_id FROM message
       WHERE conversation_id = ? AND seq > ? AND seq <= ? ORDER BY seq DESC LIMIT ?) AS m`,
    [conversationId, from, to, readSendersWindow],
  );
  const recipients = [readerId];
  for (const { sender_user_id: sender } of senders)
    if (!sender.equals(readerId) && has(st.members, sender)) recipients.push(sender);
  return recipients;
}

/**
 * Messages with seq below beforeSeq, newest first: in a group, those since
 * the user joined and, for a former member, until they left. The newer ones
 * are in MySQL; the page goes on in the cold tier below the watermark.
 */
export async function messages(
  db: Pool,
  cold: Cold,
  userId: UUID,
  conversationId: UUID,
  beforeSeq: bigint,
  limit: number,
): Promise<Message[]> {
  const m = await row<{ joined_seq: bigint; left_seq: bigint | null }>(
    db,
    `SELECT joined_seq, left_seq FROM conversation_member WHERE conversation_id = ? AND user_id = ?`,
    [conversationId, userId],
  );
  if (!m) {
    await load(db, selectState, conversationId);
    throw notInConversation(userId, conversationId);
  }
  const upTo = m.left_seq ?? maxUint64;
  const before = beforeSeq === 0n ? maxUint64 : beforeSeq;
  const n = Math.min(limit === 0 ? defaultPageSize : limit, maxPageSize);
  for (;;) {
    const page = await messagePage(db, cold, conversationId, m.joined_seq, upTo, before, n);
    if (page) return page;
  }
}

const min = (...xs: bigint[]): bigint => xs.reduce((a, b) => (b < a ? b : a));

/**
 * Where a page that MySQL gave goes on in the cold tier: from joinedSeq up
 * to hi, when there is room and anything there. A page with a message the
 * watermark has since passed is read again.
 */
export function coldRange(
  page: Message[],
  joinedSeq: bigint,
  upTo: bigint,
  beforeSeq: bigint,
  coldSeq: bigint,
  limit: number,
): { hi: bigint; more: boolean; again: boolean } {
  let hi = min(upTo, beforeSeq - 1n, coldSeq);
  const lowest = page[page.length - 1]?.seq;
  if (lowest !== undefined) {
    if (lowest <= coldSeq) return { hi: 0n, more: false, again: true };
    hi = min(hi, lowest - 1n);
  }
  return { hi, more: page.length < limit && hi >= joinedSeq, again: false };
}

/**
 * A page of history: MySQL first, then the watermark, then the cold tier
 * below the lowest seq MySQL gave; undefined when it must be read again.
 * The watermark only rises, and what it covers is in the cold tier before
 * it does, so nothing falls between the two. A move that overlapped the
 * read may have taken the reactions of what MySQL gave, so the read is
 * made again then.
 */
async function messagePage(
  db: Pool,
  cold: Cold,
  conversationId: UUID,
  joinedSeq: bigint,
  upTo: bigint,
  beforeSeq: bigint,
  limit: number,
): Promise<Message[] | undefined> {
  const list = await timedRows<MessageRow>(
    db,
    `SELECT seq, sender_user_id, client_message_id, body, blob_ids, sent_at FROM message
     WHERE conversation_id = ? AND seq >= ? AND seq <= ? AND seq < ? ORDER BY seq DESC LIMIT ?`,
    [conversationId, joinedSeq, upTo, beforeSeq, limit],
  );
  const result = list.map((r) => toMessage(conversationId, r));
  await addReactions(db, conversationId, result);
  const c = await row<{ cold_seq: bigint }>(db, `SELECT cold_seq FROM conversation WHERE conversation_id = ?`, [
    conversationId,
  ]);
  const { hi, more, again } = coldRange(result, joinedSeq, upTo, beforeSeq, c?.cold_seq ?? 0n, limit);
  if (again) return undefined;
  if (!more) return result;
  // Failing as unavailable, retryable: the hot part alone would read as the
  // end of history.
  for (const m of await cold.scan(conversationId, joinedSeq, hi, limit - result.length))
    result.push(coldMessage(conversationId, m));
  return result;
}
