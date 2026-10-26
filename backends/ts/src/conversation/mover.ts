// Moves messages older than the hot window to the cold tier, conversation
// by conversation, oldest first on each shard. Any number may run: each
// move happens under the conversation's row lock.
import { create, fromBinary } from "@bufbuild/protobuf";
import type { Pool } from "mysql2/promise";
import { ReactionSchema, SealedSchema, type Reaction, type Sealed } from "../gen/wrinkle/v1/model_pb.js";
import { exec, now, row, timedRows, timestampOf, tx, type DbTime, type Querier } from "../internal/db.ts";
import { hex, type UUID } from "../internal/ids.ts";
import type { Logger } from "../internal/log.ts";
import { startShardWork, type Leases } from "../internal/shardwork.ts";
import { encodeReactions, sameColdMessage, type Cold, type ColdMessage } from "./cold.ts";

const movePage = 1000;
// Bounds the messages one transaction moves, and so how long it holds the
// conversation's row.
const moveBatch = 200;

export interface MoveOptions {
  /** vtgate, for a connection to each shard alone. */
  mysql: string;
  hotMs: number;
  /** Time between two moves of each shard. */
  intervalMs: number;
}

/** Moves each shard under its lease, so instances share the shards. */
export const startMover = (
  db: Pool,
  cold: Cold,
  leases: Leases,
  log: Logger,
  opts: MoveOptions,
): (() => Promise<void>) =>
  startShardWork(db, opts.mysql, {
    name: "moving history to the cold tier",
    metric: "history_move",
    keyspace: "conversation",
    intervalMs: opts.intervalMs,
    unit: "messages",
    round: (shard, signal) => moveShard(db, shard.pool, cold, new Date(now().getTime() - opts.hotMs), signal),
    leases,
    log,
  });

// Takes the shard's conversations in the order of their oldest messages, a
// page at a time, each once. A conversation stops at its first message
// within the window, so one sent a little out of time order waits for the
// next round; the pages go on after the last one's time.
async function moveShard(db: Pool, shard: Pool, cold: Cold, cutoff: Date, signal: AbortSignal): Promise<number> {
  let total = 0;
  const done = new Set<string>();
  let after = new Date(0);
  for (;;) {
    signal.throwIfAborted();
    const page = await agedPage(shard, cutoff, after);
    let fresh = false;
    for (const { conversation_id: id } of page) {
      if (done.has(hex(id))) continue;
      done.add(hex(id));
      fresh = true;
      signal.throwIfAborted();
      total += await moveConversation(db, cold, id, cutoff);
    }
    if (page.length < movePage || !fresh) return total;
    after = page[page.length - 1]!.sent_at;
  }
}

const agedPage = (shard: Pool, cutoff: Date, after: Date) =>
  timedRows<{ conversation_id: UUID; sent_at: DbTime }>(
    shard,
    `SELECT conversation_id, sent_at FROM message WHERE sent_at < ? AND sent_at >= ? ORDER BY sent_at LIMIT ?`,
    [cutoff, after, movePage],
  );

// Moves the conversation's messages older than the cutoff, in seq order, a
// batch per transaction.
async function moveConversation(db: Pool, cold: Cold, conversationId: UUID, cutoff: Date): Promise<number> {
  let total = 0;
  for (;;) {
    const { n, more } = await moveBatchOf(db, cold, conversationId, cutoff);
    total += n;
    if (!more) return total;
  }
}

interface MessageRow {
  seq: bigint;
  sender_user_id: Buffer;
  client_message_id: string;
  body: Buffer;
  sent_at: DbTime;
  outbox_id: bigint;
  blob_ids: Buffer | null;
}

/**
 * Writes the next batch to the cold tier, then, with the conversation's
 * row locked, raises the watermark over it and deletes it from MySQL in one
 * transaction. The puts come before the lock, so sends to the conversation
 * never wait on the cold tier; under the lock the batch is read again, and
 * a message deleted or reacted to meanwhile is put again. A failure after
 * the puts leaves copies of rows still in MySQL, which the next move puts
 * again.
 */
async function moveBatchOf(
  db: Pool,
  cold: Cold,
  conversationId: UUID,
  cutoff: Date,
): Promise<{ n: number; more: boolean }> {
  const conv = await row<{ cold_seq: bigint }>(db, `SELECT cold_seq FROM conversation WHERE conversation_id = ?`, [
    conversationId,
  ]);
  if (!conv) throw new Error(`conversation ${hex(conversationId)} missing`);
  const coldSeq = conv.cold_seq;
  const staged = await readBatch(db, conversationId, coldSeq, cutoff, moveBatch);
  if (staged.length === 0) return { n: 0, more: false };
  await cold.put(conversationId, staged);

  return tx(db, async (c) => {
    const locked = await row<{ cold_seq: bigint }>(
      c,
      `SELECT cold_seq FROM conversation WHERE conversation_id = ? FOR UPDATE`,
      [conversationId],
    );
    if (!locked) throw new Error(`conversation ${hex(conversationId)} missing`);
    // Another mover went first; read from its watermark.
    if (locked.cold_seq !== coldSeq) return { n: 0, more: true };
    const n = staged.length;
    const upTo = staged[n - 1]!.seq;
    const current = await readBatch(c, conversationId, coldSeq, undefined, n);
    if (current.length !== n || current[n - 1]!.seq !== upTo) {
      throw new Error(`batch of conversation ${hex(conversationId)} changed from ${n} to ${current.length} messages`);
    }
    const changed = current.filter((m, i) => !sameColdMessage(m, staged[i]!));
    if (changed.length > 0) await cold.put(conversationId, changed);
    await exec(c, `UPDATE conversation SET cold_seq = ? WHERE conversation_id = ?`, [upTo, conversationId]);
    await exec(c, `DELETE FROM reaction WHERE conversation_id = ? AND seq > ? AND seq <= ?`, [
      conversationId,
      coldSeq,
      upTo,
    ]);
    await exec(c, `DELETE FROM message WHERE conversation_id = ? AND seq > ? AND seq <= ?`, [
      conversationId,
      coldSeq,
      upTo,
    ]);
    return { n, more: n === moveBatch };
  });
}

// Reads up to limit messages after the watermark, in seq order, with their
// reactions, stopping at the first sent at or after the cutoff; without a
// cutoff it reads them all.
async function readBatch(
  q: Querier,
  conversationId: UUID,
  coldSeq: bigint,
  cutoff: Date | undefined,
  limit: number,
): Promise<ColdMessage[]> {
  const batch = await timedRows<MessageRow>(
    q,
    `SELECT seq, sender_user_id, client_message_id, body, sent_at, outbox_id, blob_ids FROM message
     WHERE conversation_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
    [conversationId, coldSeq, limit],
  );
  const read: ColdMessage[] = [];
  for (const r of batch) {
    if (cutoff && r.sent_at.getTime() >= cutoff.getTime()) break;
    read.push({
      seq: r.seq,
      sender: r.sender_user_id,
      clientId: r.client_message_id,
      body: r.body,
      sentAt: r.sent_at,
      outboxId: r.outbox_id,
      blobIds: r.blob_ids ?? Buffer.alloc(0),
      reactions: Buffer.alloc(0),
    });
  }
  if (read.length === 0) return read;
  await addColdReactions(q, conversationId, coldSeq, read[read.length - 1]!.seq, read);
  return read;
}

// Reads the reactions to the messages, which span (from, to], into their
// cold rows.
async function addColdReactions(
  q: Querier,
  conversationId: UUID,
  from: bigint,
  to: bigint,
  messages: ColdMessage[],
): Promise<void> {
  const list = await timedRows<{ seq: bigint; user_id: Buffer; body: Buffer; updated_at: DbTime }>(
    q,
    `SELECT seq, user_id, body, updated_at FROM reaction
     WHERE conversation_id = ? AND seq > ? AND seq <= ? ORDER BY seq, updated_at, user_id`,
    [conversationId, from, to],
  );
  const bySeq = new Map<bigint, Reaction[]>();
  for (const r of list) {
    let sealed: Sealed;
    try {
      sealed = fromBinary(SealedSchema, r.body);
    } catch (err) {
      throw new Error(`unmarshal reaction: ${(err as Error).message}`, { cause: err });
    }
    const reactions = bySeq.get(r.seq) ?? [];
    reactions.push(create(ReactionSchema, { userId: r.user_id, sealed, updatedAt: timestampOf(r.updated_at) }));
    bySeq.set(r.seq, reactions);
  }
  for (const m of messages) m.reactions = encodeReactions(bySeq.get(m.seq) ?? []);
}
