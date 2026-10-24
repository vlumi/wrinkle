import assert from "node:assert/strict";
import { test } from "node:test";
import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { sqlTime } from "./sqltime.ts";

test("timestamps keep their microseconds", () => {
  const ts = create(TimestampSchema, { seconds: 1791725581n, nanos: 886543210 });
  assert.equal(sqlTime(ts), "2026-10-11 13:33:01.886543");
  assert.equal(sqlTime(undefined), "1970-01-01 00:00:00.000000");
});
