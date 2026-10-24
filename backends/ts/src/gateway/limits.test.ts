import assert from "node:assert/strict";
import { test } from "node:test";
import { AckCursor, bucketCapacity, TokenBucket } from "./limits.ts";
import { eventOf } from "./server.ts";

test("token bucket allows a burst, then the refill rate", () => {
  const b = new TokenBucket(0);
  for (let i = 0; i < bucketCapacity; i++) assert.ok(b.take(0));
  assert.equal(b.take(0), false);
  assert.ok(b.take(20)); // 20 ms refills one token at 50 a second
  assert.equal(b.take(20), false);
});

function recorder() {
  const writes: bigint[] = [];
  let release: (() => void) | undefined;
  let hold = false;
  const write = (c: bigint) => {
    writes.push(c);
    return hold ? new Promise<void>((r) => (release = r)) : Promise.resolve();
  };
  return { writes, write, holdNext: () => (hold = true), release: () => release?.() };
}

const tick = () => new Promise((r) => setImmediate(r));

test("the first ack is written at once, later ones once the interval is up", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"] });
  let now = 1_000;
  const r = recorder();
  const a = new AckCursor(60_000, r.write, () => now);
  a.offer(1n);
  await tick();
  assert.deepEqual(r.writes, [1n]);
  a.offer(3n);
  a.offer(2n);
  await tick();
  assert.deepEqual(r.writes, [1n]);
  now += 60_000;
  t.mock.timers.tick(60_000);
  await tick();
  assert.deepEqual(r.writes, [1n, 3n]);
});

test("closing writes what is held at once", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const now = 1_000;
  const r = recorder();
  const a = new AckCursor(60_000, r.write, () => now);
  a.offer(1n);
  await tick();
  a.offer(5n);
  await tick();
  a.close();
  await tick();
  assert.deepEqual(r.writes, [1n, 5n]);
});

test("closing during a write writes the rest after it", async () => {
  const r = recorder();
  const a = new AckCursor(60_000, r.write, () => 1_000);
  r.holdNext();
  a.offer(1n);
  await tick();
  a.offer(2n);
  a.close();
  await tick();
  assert.deepEqual(r.writes, [1n]);
  r.release();
  await tick();
  await tick();
  assert.deepEqual(r.writes, [1n, 2n]);
});

test("interval 0 writes each ack as it comes", async () => {
  const r = recorder();
  const a = new AckCursor(0, r.write, () => 1_000);
  a.offer(1n);
  await tick();
  a.offer(2n);
  await tick();
  assert.deepEqual(r.writes, [1n, 2n]);
});

test("a push that is only an event is passed on undecoded", () => {
  const event = Buffer.from([1, 2, 3]);
  assert.deepEqual(eventOf(Buffer.concat([Buffer.from([0x0a, 3]), event])), event);
  assert.equal(eventOf(Buffer.from([0x12, 0])), undefined); // signed_out
  assert.equal(eventOf(Buffer.from([0x0a, 5, 1])), undefined); // truncated
});
