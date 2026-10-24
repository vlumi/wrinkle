// Messages that are not notices, the common case, appended in batches: one
// transaction per shard of the user keyspace for the recipients of rows
// being delivered at the same time.
import { clone, toBinary } from "@bufbuild/protobuf";
import type { Pool } from "mysql2/promise";
import { EventSchema, type Event } from "../gen/wrinkle/v1/event_pb.js";
import { exec, nullBytes, placeholders, rows, rowsOf, tx } from "../internal/db.ts";
import { sameId, type UUID } from "../internal/ids.ts";
import type { Logger } from "../internal/log.ts";
import type { Shards } from "../internal/shards.ts";
import { listBody, ListEntry, type Delivery } from "./deliver.ts";
import { isDirect, peerOf, type OutboxRow } from "./outbox.ts";
import { sqlTime } from "./sqltime.ts";

/** Bounds the appends in one batch transaction. */
const maxBatch = 256;

/**
 * Whether a row's appends can share transactions with other rows': a
 * message that is not a notice, whose list update needs no flags.
 */
export function batchable(o: OutboxRow): boolean {
  if (o.event.body.case !== "message") return false;
  const msg = o.event.body.value.message;
  return msg !== undefined && msg.content.case !== "notice";
}

/** One recipient's copy of a message waiting for its batch. */
interface PendingAppend {
  entry: ListEntry;
  /** What was stored, undefined when nothing was (a deleted or unknown user). */
  stored: { event: Event; payload: Uint8Array } | undefined;
  /** The batch failed; append on its own. */
  failed: boolean;
  done: Promise<void>;
  finish: () => void;
}

interface ShardQueue {
  pending: PendingAppend[];
  running: boolean;
}

/**
 * Collects appends from rows being delivered at the same time and stores
 * them in one transaction per shard of the user keyspace: while one shard's
 * transaction runs, the next batch for it gathers, so batches grow with the
 * load.
 */
export class Batcher {
  private readonly db: Pool;
  private readonly shards: Shards;
  private readonly log: Logger;
  private readonly queues = new Map<string, ShardQueue>();

  constructor(db: Pool, shards: Shards, log: Logger) {
    this.db = db;
    this.shards = shards;
    this.log = log;
  }

  /** Queues an append on its user's shard. */
  add(entry: ListEntry): PendingAppend {
    let finish!: () => void;
    const done = new Promise<void>((resolve) => (finish = resolve));
    const p: PendingAppend = { entry, stored: undefined, failed: false, done, finish };
    const shard = this.shards.of(entry.userId);
    let q = this.queues.get(shard);
    if (!q) {
      q = { pending: [], running: false };
      this.queues.set(shard, q);
    }
    q.pending.push(p);
    if (!q.running) {
      q.running = true;
      // The first batch takes what the rows being handled now queue.
      const queue = q;
      setImmediate(() => void this.run(queue));
    }
    return p;
  }

  /** Stores the shard's queued appends, a batch at a time, until none is left. */
  private async run(q: ShardQueue): Promise<void> {
    for (;;) {
      const batch = q.pending.splice(0, maxBatch);
      if (batch.length === 0) {
        q.running = false;
        return;
      }
      try {
        await this.store(batch);
      } catch (err) {
        for (const p of batch) {
          p.stored = undefined;
          p.failed = true;
        }
        this.log.debug("batch failed, appending one at a time", { appends: batch.length, err });
      }
      for (const p of batch) p.finish();
    }
  }

  /**
   * Appends each pending message to its recipient's log and list, in one
   * transaction: it locks the users' rows in id order, so batches never wait
   * on each other in a cycle, numbers the events after each log's head, and
   * writes the events, the heads and the list entries a statement each.
   */
  private store(batch: PendingAppend[]): Promise<void> {
    return tx(this.db, async (c) => {
      const users: UUID[] = [];
      const seen = new Set<string>();
      for (const p of batch) {
        const key = p.entry.userId.toString("hex");
        if (!seen.has(key)) {
          seen.add(key);
          users.push(p.entry.userId);
        }
      }
      const locked = await rows<{ user_id: Buffer; log_head: bigint; deleted_at: Date | null }>(
        c,
        `SELECT user_id, log_head, deleted_at FROM user WHERE user_id IN (${placeholders(users.length)})
         ORDER BY user_id FOR UPDATE`,
        users,
      );
      const heads = new Map<string, { id: UUID; head: bigint }>();
      for (const r of locked) {
        if (r.deleted_at === null) heads.set(r.user_id.toString("hex"), { id: r.user_id, head: r.log_head });
      }

      const events: unknown[] = [];
      const lists: unknown[] = [];
      const stored: [PendingAppend, Event, Uint8Array][] = [];
      for (const p of batch) {
        const h = heads.get(p.entry.userId.toString("hex"));
        if (!h) continue;
        const event = clone(EventSchema, p.entry.o.event);
        event.cursor = h.head + 1n;
        h.head = event.cursor;
        const payload = toBinary(EventSchema, event);
        stored.push([p, event, payload]);
        const occurredAt = sqlTime(event.occurredAt);
        events.push(
          p.entry.userId,
          event.cursor,
          p.entry.o.conversationId,
          p.entry.o.outboxId,
          Buffer.from(payload),
          occurredAt,
        );
        lists.push(...listRow(p.entry, event, occurredAt));
      }
      if (stored.length > 0) {
        await exec(
          c,
          `INSERT INTO user_event (user_id, position, source_id, outbox_id, payload, occurred_at)
           VALUES ${rowsOf(stored.length, 6)}`,
          events,
        );
        const cases: string[] = [];
        const args: unknown[] = [];
        const ids: UUID[] = [];
        for (const { id, head } of heads.values()) {
          cases.push(" WHEN ? THEN ?");
          args.push(id, head);
          ids.push(id);
        }
        await exec(
          c,
          `UPDATE user SET log_head = CASE user_id${cases.join("")} END
           WHERE user_id IN (${placeholders(ids.length)})`,
          [...args, ...ids],
        );
        await exec(
          c,
          `INSERT INTO user_conversation
             (user_id, conversation_id, kind, peer_user_id, peer_name, peer_icon, peer_avatar, peer_deleted,
              topic, member_count, member, admin, last_seq,
              last_sender_user_id, last_client_message_id, last_body, last_sent_at,
              read_seq, peer_read_seq, updated_at)
           VALUES ${rowsOf(stored.length, 20)}
           ON DUPLICATE KEY UPDATE
             last_seq = GREATEST(last_seq, VALUES(last_seq)),
             topic = IF(VALUES(last_seq) >= last_seq, VALUES(topic), topic),
             member_count = IF(VALUES(last_seq) >= last_seq, VALUES(member_count), member_count),
             last_sender_user_id = IF(VALUES(last_seq) >= last_seq, VALUES(last_sender_user_id), last_sender_user_id),
             last_client_message_id = IF(VALUES(last_seq) >= last_seq, VALUES(last_client_message_id), last_client_message_id),
             last_body = IF(VALUES(last_seq) >= last_seq, VALUES(last_body), last_body),
             last_sent_at = IF(VALUES(last_seq) >= last_seq, VALUES(last_sent_at), last_sent_at),
             read_seq = GREATEST(read_seq, VALUES(read_seq)),
             updated_at = GREATEST(updated_at, VALUES(updated_at))`,
          lists,
        );
      }
      // Only a committed batch has stored anything.
      return () => {
        for (const [p, event, payload] of stored) p.stored = { event, payload };
      };
    }).then((commit) => commit());
  }
}

/** The values of the user's list entry for a message that is not a notice, as recordMessage inserts them. */
function listRow(e: ListEntry, event: Event, occurredAt: string): unknown[] {
  if (event.body.case !== "message" || !event.body.value.message) throw new Error("batched event is not a message");
  const msg = event.body.value.message;
  const ownSeq = sameId(Buffer.from(msg.senderUserId), e.userId) ? msg.seq : 0n;
  const peer = e.peer;
  return [
    e.userId,
    e.o.conversationId,
    e.o.conversationKind,
    isDirect(e.o) ? (e.peerId ?? null) : null,
    peer?.name ?? "",
    peer?.icon ?? "",
    nullBytes(peer?.avatar),
    peer?.deleted ?? false,
    e.o.topic,
    e.o.memberCount,
    true,
    false,
    msg.seq,
    Buffer.from(msg.senderUserId),
    msg.clientMessageId,
    listBody(msg),
    sqlTime(msg.sentAt),
    ownSeq,
    0,
    occurredAt,
  ];
}

/**
 * Appends a message to each recipient's log through the batcher, falling
 * back to one transaction per recipient for those whose batch failed (a
 * redelivered row, a deadlock), and then pushes it.
 */
export async function deliverBatched(d: Delivery, batches: Batcher, o: OutboxRow): Promise<void> {
  const appends: PendingAppend[] = [];
  for (const userId of o.recipients) {
    const entry = new ListEntry(userId, o);
    if (isDirect(o)) {
      entry.peerId = peerOf(o, userId);
      entry.peer = await d.peerForNewEntry(userId, o.conversationId, entry.peerId);
    }
    appends.push(batches.add(entry));
  }
  const fallback: UUID[] = [];
  for (const p of appends) {
    await p.done;
    if (p.failed) {
      fallback.push(p.entry.userId);
      continue;
    }
    if (p.stored) {
      await d.delivered(p.entry.userId, p.stored.event, p.stored.payload);
      d.messageDelivered(p.entry.userId, o, p.stored.event);
    }
  }
  for (const userId of fallback) await d.deliver(userId, o);
}
