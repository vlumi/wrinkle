import assert from "node:assert/strict";
import { test } from "node:test";
import { validLabel } from "./label.ts";

test("labels", () => {
  for (const s of ["a", "Bob", "Ana María", "n".repeat(64), "😀".repeat(16), "émile"]) assert.ok(validLabel(s, 64), s);
  for (const s of [
    "",
    "n".repeat(65),
    "😀".repeat(17),
    " bob",
    "bob ",
    "bob ",
    "　bob",
    "bo\x00b",
    "bo\nb",
    "bo\u0085b",
    "bo\u007fb",
    "\ud800",
  ])
    assert.equal(validLabel(s, 64), false, JSON.stringify(s));
});
