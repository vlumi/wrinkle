// The cold tier of the history, in HBase through its REST server: Node has
// no maintained native client. Keys, columns and values travel in base64.
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { MessageSchema, type Message, type Reaction } from "../gen/wrinkle/v1/model_pb.js";
import type { Pool, PoolConnection } from "mysql2/promise";
import { microsOf, timestampOf, tx, type DbTime } from "../internal/db.ts";
import type { UUID } from "../internal/ids.ts";
import { coldReadDuration } from "../internal/metrics.ts";
import { decodeBlobIds, decodeBody } from "./state.ts";

const table = "message";
const family = "m";
const requestTimeout = 5_000;

/** A message row as the cold tier keeps it: the columns of the message table and the message's reactions. */
export interface ColdMessage {
  seq: bigint;
  sender: UUID;
  clientId: string;
  body: Buffer;
  sentAt: DbTime;
  outboxId: bigint;
  blobIds: Buffer;
  /** A serialized Message with only its reactions, oldest first; empty for none. */
  reactions: Buffer;
}

/** The row key: the conversation id, then the seq big-endian, so a conversation's history is one range in seq order. */
export function coldKey(conversationId: UUID, seq: bigint): Buffer {
  const key = Buffer.alloc(24);
  conversationId.copy(key);
  key.writeBigUInt64BE(seq, 16);
  return key;
}

const uint64 = (n: bigint): Buffer => {
  const b = Buffer.alloc(8);
  b.writeBigUInt64BE(n);
  return b;
};

/** The row's cells by qualifier. */
export function cellsOf(m: ColdMessage): Record<string, Buffer> {
  return {
    sender: m.sender,
    client_id: Buffer.from(m.clientId, "utf8"),
    body: m.body,
    sent_at: uint64(microsOf(m.sentAt)),
    outbox_id: uint64(m.outboxId),
    blob_ids: m.blobIds,
    reactions: m.reactions,
  };
}

export function coldMessageOf(key: Buffer, cells: Record<string, Buffer>): ColdMessage {
  const fixed = (q: string, n: number): Buffer => {
    const v = cells[q] ?? Buffer.alloc(0);
    if (v.length !== n) throw new Error(`cold message ${q} of ${v.length} bytes`);
    return v;
  };
  const micros = fixed("sent_at", 8).readBigUInt64BE();
  const sentAt: DbTime = new Date(Number(micros / 1000n));
  sentAt.micros = Number(micros % 1000n);
  return {
    seq: key.length === 24 ? key.readBigUInt64BE(16) : 0n,
    sender: Buffer.from(fixed("sender", 16)),
    clientId: (cells["client_id"] ?? Buffer.alloc(0)).toString("utf8"),
    body: cells["body"] ?? Buffer.alloc(0),
    sentAt,
    outboxId: fixed("outbox_id", 8).readBigUInt64BE(),
    blobIds: cells["blob_ids"] ?? Buffer.alloc(0),
    reactions: cells["reactions"] ?? Buffer.alloc(0),
  };
}

/** The message as GetMessages returns it, with its reactions unless it is deleted or a notice. */
export function coldMessage(conversationId: UUID, m: ColdMessage): Message {
  const msg = create(MessageSchema, {
    conversationId,
    seq: m.seq,
    senderUserId: m.sender,
    clientMessageId: m.clientId,
    sentAt: timestampOf(m.sentAt),
    blobIds: decodeBlobIds(m.blobIds),
  });
  decodeBody(m.body, msg);
  if (!msg.deletedAt && msg.content.case !== "notice") msg.reactions = reactionList(m);
  return msg;
}

export function reactionList(m: ColdMessage): Reaction[] {
  try {
    return fromBinary(MessageSchema, m.reactions).reactions;
  } catch (err) {
    throw new Error(`unmarshal cold reactions: ${(err as Error).message}`, { cause: err });
  }
}

export const encodeReactions = (reactions: Reaction[]): Buffer =>
  reactions.length === 0 ? Buffer.alloc(0) : Buffer.from(toBinary(MessageSchema, create(MessageSchema, { reactions })));

const b64 = (b: Uint8Array | string): string => Buffer.from(b).toString("base64");

interface RestRow {
  key: string;
  Cell?: { column: string; $?: string }[];
}

/** Every write puts a whole row, so a write can be repeated. */
export class Cold {
  private readonly url: string;

  constructor(url: string) {
    this.url = url.replace(/\/+$/, "");
  }

  /** Writes the conversation's messages. */
  async put(conversationId: UUID, messages: ColdMessage[]): Promise<void> {
    if (messages.length === 0) return;
    const Row = messages.map((m) => ({
      key: b64(coldKey(conversationId, m.seq)),
      Cell: Object.entries(cellsOf(m)).map(([q, v]) => ({ column: b64(`${family}:${q}`), $: b64(v) })),
    }));
    // The row in the path is ignored; the body names them.
    const res = await fetch(`${this.url}/${table}/fakerow`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ Row }),
      signal: AbortSignal.timeout(requestTimeout),
    });
    const text = await res.text();
    if (!res.ok) throw new Error(`cold put: ${res.status} ${text}`);
  }

  /** The message at seq, if the cold tier has it. */
  async get(conversationId: UUID, seq: bigint): Promise<ColdMessage | undefined> {
    return (await this.range(coldKey(conversationId, seq), coldKey(conversationId, seq + 1n), "message"))[0];
  }

  /**
   * Up to limit messages with seq from lo to hi, newest first. The REST
   * scanner only reads forward, so this reads the last limit seqs of the
   * range and turns them around: it relies on a conversation's cold seqs
   * being contiguous, which they are, since messages are only tombstoned
   * and every seq up to the watermark moves.
   */
  async scan(conversationId: UUID, lo: bigint, hi: bigint, limit: number): Promise<ColdMessage[]> {
    if (lo > hi || limit <= 0) return [];
    const from = hi - lo + 1n > BigInt(limit) ? hi - BigInt(limit) + 1n : lo;
    const found = await this.range(coldKey(conversationId, from), coldKey(conversationId, hi + 1n), "range");
    return found.reverse();
  }

  // The rows from start up to end, exclusive, through a scanner. A response
  // may end within a row, so cells of one key are gathered across them.
  private async range(start: Buffer, end: Buffer, kind: "range" | "message"): Promise<ColdMessage[]> {
    const observe = coldReadDuration.startTimer({ kind });
    try {
      return await this.scanRows(start, end);
    } finally {
      observe();
    }
  }

  private async scanRows(start: Buffer, end: Buffer): Promise<ColdMessage[]> {
    const opened = await fetch(`${this.url}/${table}/scanner`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ startRow: b64(start), endRow: b64(end), batch: 1000 }),
      signal: AbortSignal.timeout(requestTimeout),
    });
    const scanner = opened.headers.get("location");
    const text = await opened.text();
    if (opened.status !== 201 || !scanner) throw new Error(`cold scan: ${opened.status} ${text}`);
    const rows: { key: Buffer; cells: Record<string, Buffer> }[] = [];
    try {
      for (;;) {
        const res = await fetch(scanner, {
          headers: { Accept: "application/json" },
          signal: AbortSignal.timeout(requestTimeout),
        });
        if (res.status === 204) break;
        if (res.status !== 200) throw new Error(`cold scan: ${res.status} ${await res.text()}`);
        const { Row } = (await res.json()) as { Row?: RestRow[] };
        for (const r of Row ?? []) {
          const key = Buffer.from(r.key, "base64");
          let last = rows[rows.length - 1];
          if (!last?.key.equals(key)) rows.push((last = { key, cells: {} }));
          for (const c of r.Cell ?? []) {
            const column = Buffer.from(c.column, "base64").toString("utf8");
            if (column.startsWith(`${family}:`))
              last.cells[column.slice(family.length + 1)] = Buffer.from(c.$ ?? "", "base64");
          }
        }
      }
    } finally {
      await fetch(scanner, { method: "DELETE", signal: AbortSignal.timeout(requestTimeout) })
        .then((res) => res.text())
        .catch(() => undefined);
    }
    return rows.map((r) => coldMessageOf(r.key, r.cells));
  }
}

/** Writes a changed cold row of the conversation. */
export type ColdChange = (conversationId: UUID, old: ColdMessage, changed: ColdMessage) => Promise<void>;

/**
 * Runs fn in a transaction that tells of a change to a cold row, which it
 * writes through change while holding the conversation's row. Unless the
 * transaction commits, the old row is put back: before the rollback, so
 * still under the lock, or after a commit that failed.
 */
export async function coldTx<T>(
  db: Pool,
  cold: Cold,
  fn: (c: PoolConnection, change: ColdChange) => Promise<T>,
): Promise<T> {
  let undo: (() => Promise<void>) | undefined;
  const putBack = async () => {
    const u = undo;
    undo = undefined;
    await u?.().catch(() => undefined);
  };
  const change: ColdChange = async (conversationId, old, changed) => {
    await cold.put(conversationId, [changed]);
    undo = () => cold.put(conversationId, [old]);
  };
  try {
    return await tx(db, async (c) => {
      try {
        return await fn(c, change);
      } catch (err) {
        await putBack();
        throw err;
      }
    });
  } catch (err) {
    await putBack();
    throw err;
  }
}
