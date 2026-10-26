// Background work over every shard of a keyspace, each shard in its own
// loop, shared among the instances that run it: a shard's round needs its
// lease, so one instance works it at a time and the others take the rest.
import { setTimeout as delay } from "node:timers/promises";
import type { Pool } from "mysql2/promise";
import type { Logger } from "./log.ts";
import { shardRoundDuration, shardRounds, shardWork } from "./metrics.ts";
import { closeShardPools, openShardPools, type Shard } from "./shards.ts";

const firstRound = 60_000;
// Bounds how long a stopped instance keeps its shards; a round renews its
// lease at a third of it.
const leaseTTL = 30_000;
const dropDeadline = 5_000;

/** Leases held by name, for this instance. */
export interface Leases {
  takeLease(name: string, ttlMs: number): Promise<boolean>;
  /** Extends a lease this instance holds, and reports whether it still did. */
  renewLease(name: string, ttlMs: number): Promise<boolean>;
  dropLease(name: string, signal: AbortSignal): Promise<void>;
}

/**
 * One kind of work: round does what is due on a shard and returns how much
 * it did, in unit. metric labels its metrics.
 */
export interface Job {
  name: string;
  metric: string;
  keyspace: string;
  intervalMs: number;
  unit: string;
  round: (shard: Shard, signal: AbortSignal) => Promise<number>;
  leases: Leases;
  log: Logger;
  /** The lease's time to live; 30 s unless a test shortens it. */
  leaseMs?: number;
}

/**
 * Runs each shard's rounds, the first after a minute, or the interval when
 * shorter, then at every interval. The shards are found before the first
 * rounds, and again after that failed. The stop function ends the loops,
 * waits for their rounds and closes the shards' pools.
 */
export function startShardWork(db: Pool, mysql: string, job: Job): () => Promise<void> {
  const stop = new AbortController();
  let shards: Shard[] | undefined;
  const loops: Promise<void>[] = [];
  const find = () => {
    openShardPools(db, mysql, job.keyspace).then(
      (found) => {
        if (stop.signal.aborted) return void closeShardPools(db, found).catch(() => undefined);
        shards = found;
        for (const shard of found) loops.push(loop(job, shard, stop.signal));
      },
      (err: unknown) => {
        if (stop.signal.aborted) return;
        job.log.warn(job.name + " failed", { err });
        timer = setTimeout(find, job.intervalMs);
      },
    );
  };
  let timer = setTimeout(find, Math.min(firstRound, job.intervalMs));
  return async () => {
    stop.abort();
    clearTimeout(timer);
    await Promise.all(loops);
    if (shards) await closeShardPools(db, shards).catch(() => undefined);
  };
}

async function loop(job: Job, shard: Shard, signal: AbortSignal): Promise<void> {
  const log = job.log.with({ shard: shard.name });
  // The shard shows before its first round ends.
  shardRounds.inc({ job: job.metric, shard: shard.name, result: "ok" }, 0);
  for (;;) {
    try {
      const n = await leased(job, shard, signal);
      if (n > 0) log.info(job.name, { [job.unit]: n });
    } catch (err) {
      if (!signal.aborted) log.warn(job.name + " failed", { err });
    }
    const stopped = await delay(job.intervalMs, undefined, { signal }).then(
      () => false,
      () => true,
    );
    if (stopped) return;
  }
}

/**
 * Runs a round under the shard's lease, if this instance gets it. Losing
 * the lease midway stops the round; whoever took it goes on.
 */
export async function leased(job: Job, shard: Shard, signal: AbortSignal): Promise<number> {
  const name = `${job.name}:${job.keyspace}/${shard.name}`;
  const ttl = job.leaseMs ?? leaseTTL;
  let taken: boolean;
  try {
    taken = await job.leases.takeLease(name, ttl);
  } catch (err) {
    shardRounds.inc({ job: job.metric, shard: shard.name, result: "error" });
    throw err;
  }
  if (!taken) {
    shardRounds.inc({ job: job.metric, shard: shard.name, result: "skipped" });
    return 0;
  }
  const end = shardRoundDuration.startTimer({ job: job.metric });
  const lost = new AbortController();
  let done = false;
  let renewing: Promise<void> = Promise.resolve();
  let timer: NodeJS.Timeout | undefined;
  const renew = () => {
    renewing = job.leases.renewLease(name, ttl).then(
      (held) => {
        if (!held) lost.abort(new Error("lease lost"));
        else if (!done) timer = setTimeout(renew, ttl / 3);
      },
      () => {
        if (!done) timer = setTimeout(renew, ttl / 3);
      },
    );
  };
  timer = setTimeout(renew, ttl / 3);
  let n = 0;
  let failed = false;
  try {
    n = await job.round(shard, AbortSignal.any([signal, lost.signal]));
    return n;
  } catch (err) {
    failed = true;
    throw err;
  } finally {
    done = true;
    clearTimeout(timer);
    await renewing;
    end();
    shardWork.inc({ job: job.metric }, n);
    const result = lost.signal.aborted ? "lost" : failed ? "error" : "ok";
    shardRounds.inc({ job: job.metric, shard: shard.name, result });
    await job.leases.dropLease(name, AbortSignal.timeout(dropDeadline)).catch(() => undefined);
  }
}
