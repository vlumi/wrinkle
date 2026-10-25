// Where Vitess's xxhash vindex places ids among a keyspace's shards, so work
// can be grouped into one transaction per shard.
import type { Pool } from "mysql2/promise";
import { openKeyspace, rows, type Querier } from "./db.ts";
import { xxh64 } from "./xxhash.ts";

/** Where the xxhash vindex places an id: its 64-bit xxhash, little-endian. */
export function keyspaceId(id: Uint8Array): Buffer {
  const b = Buffer.alloc(8);
  b.writeBigUInt64LE(xxh64(id));
  return b;
}

export class Shards {
  /** The shards' names in keyspace order; none when the keyspace is not sharded. */
  readonly names: string[];
  // Each shard's first keyspace id, ascending; empty for the first.
  private readonly starts: Buffer[];

  /** No shards, or a keyspace that is not sharded, puts every id in one shard. */
  constructor(names: string[] = [], starts: Buffer[] = []) {
    this.names = names;
    this.starts = starts;
  }

  /** The name of the shard holding the id. */
  of(id: Uint8Array): string {
    if (this.starts.length === 0) return "";
    const ksid = keyspaceId(id);
    let shard = "";
    this.starts.forEach((start, i) => {
      if (Buffer.compare(ksid, start) >= 0) shard = this.names[i]!;
    });
    return shard;
  }
}

/** Reads the keyspace's shards from vtgate; a server that is not vtgate gives one shard. */
export async function loadShards(q: Querier, keyspace: string): Promise<Shards> {
  let found: Record<string, unknown>[];
  try {
    found = await rows(q, "SHOW VITESS_SHARDS");
  } catch {
    return new Shards();
  }
  const shards: { name: string; start: Buffer }[] = [];
  for (const r of found) {
    const full = String(Object.values(r)[0]);
    const slash = full.indexOf("/");
    if (slash < 0 || full.slice(0, slash) !== keyspace) continue;
    const name = full.slice(slash + 1);
    const start = name.split("-")[0]!;
    if (!/^([0-9a-fA-F]{2})*$/.test(start)) throw new Error(`shard ${JSON.stringify(full)}: not hex`);
    shards.push({ name, start: Buffer.from(start, "hex") });
  }
  shards.sort((a, b) => Buffer.compare(a.start, b.start));
  return new Shards(
    shards.map((s) => s.name),
    shards.map((s) => s.start),
  );
}

/** A pool of one shard, by its name; "-" for a keyspace that is not sharded. */
export interface Shard {
  name: string;
  pool: Pool;
}

/**
 * A pool of one connection to each shard of the keyspace that db reaches,
 * so a query sees that shard alone; without vtgate, db itself.
 */
export async function openShardPools(db: Pool, mysql: string, keyspace: string): Promise<Shard[]> {
  const names = (await loadShards(db, keyspace)).names;
  const opened = await Promise.allSettled(names.map((name) => openKeyspace(mysql, `${keyspace}:${name}`, 1)));
  const pools = opened.flatMap((r) => (r.status === "fulfilled" ? [r.value] : []));
  const failed = opened.find((r) => r.status === "rejected");
  if (failed) {
    await Promise.all(pools.map((p) => p.end()));
    throw failed.reason;
  }
  return pools.length > 0 ? names.map((name, i) => ({ name, pool: pools[i]! })) : [{ name: "-", pool: db }];
}

export const closeShardPools = (db: Pool, shards: Shard[]) =>
  Promise.all(shards.filter((s) => s.pool !== db).map((s) => s.pool.end()));
