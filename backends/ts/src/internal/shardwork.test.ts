import assert from "node:assert/strict";
import { test } from "node:test";
import type { Pool } from "mysql2/promise";
import { Logger } from "./log.ts";
import { leased, type Job, type Leases } from "./shardwork.ts";
import type { Shard } from "./shards.ts";

// Leases of one instance; others take them by holding the name.
class FakeLeases implements Leases {
  readonly held = new Map<string, string>();
  readonly me: string;
  constructor(me: string) {
    this.me = me;
  }
  takeLease(name: string): Promise<boolean> {
    if (this.held.has(name)) return Promise.resolve(false);
    this.held.set(name, this.me);
    return Promise.resolve(true);
  }
  renewLease(name: string): Promise<boolean> {
    return Promise.resolve(this.held.get(name) === this.me);
  }
  dropLease(name: string): Promise<void> {
    if (this.held.get(name) === this.me) this.held.delete(name);
    return Promise.resolve();
  }
}

const shard = (name: string): Shard => ({ name, pool: {} as Pool });

const job = (leases: Leases, round: Job["round"]): Job => ({
  name: "work",
  keyspace: "ks",
  intervalMs: 3600_000,
  unit: "items",
  round,
  leases,
  log: new Logger(),
  leaseMs: 300,
});

test("a leased round runs once and drops the lease", async () => {
  const leases = new FakeLeases("a");
  let rounds = 0;
  const n = await leased(
    job(leases, () => Promise.resolve(++rounds && 3)),
    shard("-80"),
    new AbortController().signal,
  );
  assert.equal(n, 3);
  assert.equal(rounds, 1);
  assert.equal(leases.held.size, 0);
});

test("a shard leased elsewhere is skipped", async () => {
  const leases = new FakeLeases("a");
  leases.held.set("work:ks/-80", "b");
  const ran: string[] = [];
  const j = job(leases, (s) => {
    ran.push(s.name);
    return Promise.resolve(0);
  });
  const signal = new AbortController().signal;
  assert.equal(await leased(j, shard("-80"), signal), 0);
  assert.equal(await leased(j, shard("80-"), signal), 0);
  assert.deepEqual(ran, ["80-"]);
  assert.deepEqual([...leases.held], [["work:ks/-80", "b"]]);
});

test("losing the lease stops the round", async () => {
  const leases = new FakeLeases("a");
  const j = job(leases, (_, signal) => {
    leases.held.set("work:ks/-", "b");
    return new Promise((resolve, reject) => {
      const late = setTimeout(() => reject(new Error("round went on after its lease was lost")), 5_000);
      signal.addEventListener("abort", () => {
        clearTimeout(late);
        resolve(1);
      });
    });
  });
  assert.equal(await leased(j, shard("-"), new AbortController().signal), 1);
  assert.equal(leases.held.get("work:ks/-"), "b");
});
