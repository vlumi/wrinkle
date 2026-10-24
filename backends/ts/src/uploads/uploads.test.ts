import assert from "node:assert/strict";
import { test } from "node:test";
import { validMediaType } from "./uploads.ts";

test("media types", () => {
  for (const s of [
    "image/png",
    "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    "text/plain;charset=utf-8",
  ]) {
    assert.ok(validMediaType(s), s);
  }
  for (const s of ["", "image", "/png", "image/", "image/png/x", "image/ png", "imäge/png", "a/" + "b".repeat(99)]) {
    assert.equal(validMediaType(s), false, s);
  }
});
