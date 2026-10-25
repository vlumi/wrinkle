// Moves messages older than the hot window to the cold tier, conversation
// by conversation, oldest first on each shard. Any number may run: each
// move happens under the conversation's row lock.
import { create, fromBinary } from "@bufbuild/protobuf";
import type { Pool, PoolConnection } from "mysql2/promise";
import { ReactionSchema, SealedSchema, type Reaction, type Sealed } from "../gen/wrinkle/v1/model_pb.js";
import { exec, now, row, timedRows, timestampOf, tx, type DbTime } from "../internal/db.ts";
import { hex, type UUID } from "../internal/ids.ts";
import type { Logger } from "../internal/log.ts";
import { closeShardPools, openShardPools } from "../internal/shards.ts";
import { encodeReactions, type Cold, type ColdMessage } from "./cold.ts";

const moveFirst = 60_000;
const movePage = 1000;
// Bounds the messages one transaction moves, and so how long it holds the
// conversation's row.
const moveBatch = 200;

export interface MoveOptions {
  /** vtgate, for a connection to each shard alone. */
  mysql: string;
  hotMs: number;
  intervalMs: number;
}

/**
 * Moves after a minute, or the interval when shorter, then at every
 * interval. The shards are found on the first round, and again after that
 * failed.
 */
export function startMover(db: Pool, cold: Cold, log: Logger, opts: MoveOptions): () => void {
  let shards: Promise<Pool[]> | undefined;
  let stopped = false;
  let timer = setTimeout(
    function run() {
      shards ??= openShardPools(db, opts.mysql, "conversation").catch((err: unknown) => {
        shards = undefined;
        throw err;
      });
      shards
        .then((s) => move(db, s, cold, opts.hotMs))
        .then((n) => {
          if (n > 0) log.info("moved history to the cold tier", { messages: n });
        })
        .catch((err: unknown) => {
          if (!stopped) log.warn("moving history to the cold tier failed", { err });
        })
        .finally(() => {
          if (!stopped) timer = setTimeout(run, opts.intervalMs);
        });
    },
    Math.min(moveFirst, opts.intervalMs),
  );
  return () => {
    stopped = true;
    clearTimeout(timer);
    void shards?.then((s) => closeShardPools(db, s)).catch(() => undefined);
  };
}

/** Moves every message older than the hot window and returns how many. */
export async function move(db: Pool, shards: Pool[], cold: Cold, hotMs: number): Promise<number> {
  const cutoff = new Date(now().getTime() - hotMs);
  let total = 0;
  for (const shard of shards) total += await moveShard(db, shard, cold, cutoff);
  return total;
}

// Takes the shard's conversations in the order of their oldest messages, a
// page at a time, each once. A conversation stops at its first message
// within the window, so one sent a little out of time order waits for the
// next round; the pages go on after the last one's time.
async function moveShard(db: Pool, shard: Pool, cold: Cold, cutoff: Date): Promise<number> {
  let total = 0;
  const done = new Set<string>();
  let after = new Date(0);
  for (;;) {
    const page = await agedPage(shard, cutoff, after);
    let fresh = false;
    for (const { conversation_id: id } of page) {
      if (done.has(hex(id))) continue;
      done.add(hex(id));
      fresh = true;
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
    const n = await tx(db, (c) => moveBatchOf(c, cold, conversationId, cutoff));
    total += n;
    if (n < moveBatch) return total;
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
 * Writes the next batch to the cold tier with the conversation's row
 * locked, then raises the watermark over it and deletes it from MySQL in
 * the same transaction. A failure after the puts leaves copies of rows
 * still in MySQL, which the next move puts again.
 */
async function moveBatchOf(c: PoolConnection, cold: Cold, conversationId: UUID, cutoff: Date): Promise<number> {
  const conv = await row<{ cold_seq: bigint }>(
    c,
    `SELECT cold_seq FROM conversation WHERE conversation_id = ? FOR UPDATE`,
    [conversationId],
  );
  if (!conv) throw new Error(`conversation ${hex(conversationId)} missing`);
  const batch = await timedRows<MessageRow>(
    c,
    `SELECT seq, sender_user_id, client_message_id, body, sent_at, outbox_id, blob_ids FROM message
     WHERE conversation_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
    [conversationId, conv.cold_seq, moveBatch],
  );
  const moved: ColdMessage[] = [];
  for (const r of batch) {
    if (r.sent_at.getTime() >= cutoff.getTime()) break;
    moved.push({
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
  if (moved.length === 0) return 0;
  const upTo = moved[moved.length - 1]!.seq;
  await addColdReactions(c, conversationId, conv.cold_seq, upTo, moved);
  await cold.put(conversationId, moved);
  await exec(c, `UPDATE conversation SET cold_seq = ? WHERE conversation_id = ?`, [upTo, conversationId]);
  await exec(c, `DELETE FROM reaction WHERE conversation_id = ? AND seq > ? AND seq <= ?`, [
    conversationId,
    conv.cold_seq,
    upTo,
  ]);
  await exec(c, `DELETE FROM message WHERE conversation_id = ? AND seq > ? AND seq <= ?`, [
    conversationId,
    conv.cold_seq,
    upTo,
  ]);
  return moved.length;
}

// Reads the reactions to the messages, which span (from, to], into their
// cold rows.
async function addColdReactions(
  c: PoolConnection,
  conversationId: UUID,
  from: bigint,
  to: bigint,
  messages: ColdMessage[],
): Promise<void> {
  const list = await timedRows<{ seq: bigint; user_id: Buffer; body: Buffer; updated_at: DbTime }>(
    c,
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
