import assert from "node:assert/strict";
import { test } from "node:test";
import { microsOf, parseDateTime, timestampOf, type DbTime } from "./db.ts";

test("timestamps keep the microseconds read", () => {
  const d: DbTime = new Date("2026-10-24T08:47:15.049Z");
  d.micros = 692;
  assert.equal(microsOf(d), 1792831635049692n);
  const ts = timestampOf(d);
  assert.equal(ts.seconds, 1792831635n);
  assert.equal(ts.nanos, 49692000);
  assert.equal(timestampOf(new Date(1500)).nanos, 500_000_000);
});

test("datetimes parse to the microsecond", () => {
  const d = parseDateTime("2026-10-24 08:47:15.049692");
  assert.equal(microsOf(d), 1792831635049692n);
  assert.equal(microsOf(parseDateTime("2026-10-24 08:47:15")), 1792831635000000n);
});
