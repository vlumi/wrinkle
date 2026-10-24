import assert from "node:assert/strict";
import { test } from "node:test";
import { eachLimited, Limit } from "./limit.ts";
import { byKey } from "./worker.ts";

test("groups records by key in offset order", () => {
  const rec = (key: string | null, offset: string) => ({ key: key === null ? null : Buffer.from(key), offset });
  const groups = byKey([rec("a", "1"), rec("b", "2"), rec("a", "3"), rec(null, "4"), rec("b", "5")]);
  assert.deepEqual(
    groups.map((g) => g.map((r) => r.offset)),
    [["1", "3"], ["2", "5"], ["4"]],
  );
});

test("limits concurrency", async () => {
  const limit = new Limit(2);
  let running = 0;
  let most = 0;
  await Promise.all(
    Array.from({ length: 6 }, () =>
      limit.run(async () => {
        running++;
        most = Math.max(most, running);
        await new Promise((r) => setTimeout(r, 5));
        running--;
      }),
    ),
  );
  assert.equal(most, 2);
});

test("eachLimited stops after a failure and reports it", async () => {
  const started: number[] = [];
  await assert.rejects(
    eachLimited([1, 2, 3, 4, 5, 6], 2, async (n) => {
      started.push(n);
      await new Promise((r) => setTimeout(r, 1));
      if (n === 2) throw new Error("two");
    }),
    /two/,
  );
  assert.ok(started.length < 6);
});
