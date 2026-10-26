import assert from "node:assert/strict";
import { test } from "node:test";
import { pendingVerdict } from "./sweeper.ts";

test("pending verdict", () => {
  const at = new Date(Date.UTC(2026, 9, 26, 12));
  const day = 24 * 3600_000;
  const hot = 30 * day;
  for (const [name, sent, age, want] of [
    ["delivered late", true, 2 * day, "keep"],
    ["failed send", false, 2 * day, "drop"],
    // Its message may be in the cold tier by now.
    ["older than half the hot window", false, 20 * day, "keep"],
    ["sent, long ago", true, 40 * day, "keep"],
  ] as const) {
    assert.equal(pendingVerdict(sent, new Date(at.getTime() - age), at, hot), want, name);
  }
});
