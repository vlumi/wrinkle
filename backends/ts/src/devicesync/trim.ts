// Drops events older than the retention from the user logs, a page at a time
// on each shard, oldest first. Any number may run: raising a tail and
// dropping what is under it can be done twice.
import type { Pool } from "mysql2/promise";
import { exec, now, rows, tx } from "../internal/db.ts";
import type { UUID } from "../internal/ids.ts";
import type { Logger } from "../internal/log.ts";
import { startShardWork, type Leases } from "../internal/shardwork.ts";

const pageSize = 1000;

export interface TrimOptions {
  /** vtgate, for a connection to each shard alone. */
  mysql: string;
  retentionMs: number;
  /** Time between two trims of each shard. */
  intervalMs: number;
}

/** Trims each shard under its lease, so instances share the shards. */
export const startTrimmer = (db: Pool, leases: Leases, log: Logger, opts: TrimOptions): (() => Promise<void>) =>
  startShardWork(db, opts.mysql, {
    name: "trimming the user logs",
    keyspace: "user",
    intervalMs: opts.intervalMs,
    unit: "events",
    round: (shard, signal) => trimShard(db, shard.pool, opts.retentionMs, signal),
    leases,
    log,
  });

/** Drops the shard's events older than the retention and returns how many. */
export async function trimShard(db: Pool, shard: Pool, retentionMs: number, signal: AbortSignal): Promise<number> {
  const cutoff = new Date(now().getTime() - retentionMs);
  let total = 0;
  for (;;) {
    signal.throwIfAborted();
    const n = await trimPage(db, shard, cutoff, signal);
    total += n;
    if (n < pageSize) return total;
  }
}

export interface Trimmed {
  userId: UUID;
  tail: bigint;
}

/** Each user's newest position among the page's events, in order of first appearance. */
export function tailsOf(page: { user_id: UUID; position: bigint }[]): Trimmed[] {
  const users = new Map<string, Trimmed>();
  for (const e of page) {
    const key = e.user_id.toString("hex");
    const u = users.get(key);
    if (u) {
      if (e.position > u.tail) u.tail = e.position;
    } else users.set(key, { userId: e.user_id, tail: e.position });
  }
  return [...users.values()];
}

// Drops the shard's oldest events, up to a page of them. A user's log is cut
// below the newest of its events in the page, so it stays contiguous even
// where an event's time is a little out of order.
async function trimPage(db: Pool, shard: Pool, cutoff: Date, signal: AbortSignal): Promise<number> {
  const page = await rows<{ user_id: UUID; position: bigint }>(
    shard,
    `SELECT user_id, position FROM user_event WHERE occurred_at < ? ORDER BY occurred_at LIMIT ?`,
    [cutoff, pageSize],
  );
  // The tail is raised in the transaction that drops the events under it,
  // so a sync that misses them sees the new tail.
  for (const u of tailsOf(page)) {
    signal.throwIfAborted();
    await tx(db, async (c) => {
      await exec(c, `UPDATE user SET log_tail = GREATEST(log_tail, ?) WHERE user_id = ?`, [u.tail, u.userId]);
      await exec(c, `DELETE FROM user_event WHERE user_id = ? AND position <= ?`, [u.userId, u.tail]);
    });
  }
  return page.length;
}
