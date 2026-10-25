import assert from "node:assert/strict";
import { test } from "node:test";
import { tailsOf } from "./trim.ts";

test("a page cuts each user's log at its newest event there", () => {
  const a = Buffer.alloc(16, 1);
  const b = Buffer.alloc(16, 2);
  const tails = tailsOf([
    { user_id: a, position: 3n },
    { user_id: b, position: 7n },
    { user_id: Buffer.from(a), position: 5n },
    { user_id: a, position: 4n },
  ]);
  assert.deepEqual(
    tails.map((t) => [t.userId.toString("hex"), t.tail]),
    [
      [a.toString("hex"), 5n],
      [b.toString("hex"), 7n],
    ],
  );
});
