// Drops events older than the retention from the user logs, a page at a time
// on each shard, oldest first. Any number may run: raising a tail and
// dropping what is under it can be done twice.
import type { Pool } from "mysql2/promise";
import { exec, now, rows, tx } from "../internal/db.ts";
import type { UUID } from "../internal/ids.ts";
import type { Logger } from "../internal/log.ts";
import { closeShardPools, openShardPools } from "../internal/shards.ts";

const trimFirst = 60_000;
const pageSize = 1000;

export interface TrimOptions {
  /** vtgate, for a connection to each shard alone. */
  mysql: string;
  retentionMs: number;
  intervalMs: number;
}

/**
 * Trims after a minute, or the interval when shorter, then at every interval.
 * The shards are found on the first round, and again after that failed.
 */
export function startTrimmer(db: Pool, log: Logger, opts: TrimOptions): () => void {
  let shards: Promise<Pool[]> | undefined;
  let stopped = false;
  let timer = setTimeout(
    function run() {
      shards ??= openShardPools(db, opts.mysql, "user").catch((err: unknown) => {
        shards = undefined;
        throw err;
      });
      shards
        .then((s) => trim(db, s, opts.retentionMs))
        .then((n) => {
          if (n > 0) log.info("trimmed the user logs", { events: n });
        })
        .catch((err: unknown) => {
          if (!stopped) log.warn("trimming the user logs failed", { err });
        })
        .finally(() => {
          if (!stopped) timer = setTimeout(run, opts.intervalMs);
        });
    },
    Math.min(trimFirst, opts.intervalMs),
  );
  return () => {
    stopped = true;
    clearTimeout(timer);
    void shards?.then((s) => closeShardPools(db, s)).catch(() => undefined);
  };
}

/** Drops every event older than the retention and returns how many. */
export async function trim(db: Pool, shards: Pool[], retentionMs: number): Promise<number> {
  const cutoff = new Date(now().getTime() - retentionMs);
  let total = 0;
  for (const shard of shards)
    for (;;) {
      const n = await trimPage(db, shard, cutoff);
      total += n;
      if (n < pageSize) break;
    }
  return total;
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
async function trimPage(db: Pool, shard: Pool, cutoff: Date): Promise<number> {
  const page = await rows<{ user_id: UUID; position: bigint }>(
    shard,
    `SELECT user_id, position FROM user_event WHERE occurred_at < ? ORDER BY occurred_at LIMIT ?`,
    [cutoff, pageSize],
  );
  // The tail is raised in the transaction that drops the events under it,
  // so a sync that misses them sees the new tail.
  for (const u of tailsOf(page))
    await tx(db, async (c) => {
      await exec(c, `UPDATE user SET log_tail = GREATEST(log_tail, ?) WHERE user_id = ?`, [u.tail, u.userId]);
      await exec(c, `DELETE FROM user_event WHERE user_id = ? AND position <= ?`, [u.userId, u.tail]);
    });
  return page.length;
}
