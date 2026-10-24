import assert from "node:assert/strict";
import { test } from "node:test";
import { newUUID } from "../internal/ids.ts";
import { DirectMembers } from "./direct.ts";
import { kindDirect, kindNotes } from "./state.ts";

test("direct members", () => {
  const c = new DirectMembers();
  const [conv, a, b] = [newUUID(), newUUID(), newUUID()];
  assert.equal(c.get(conv), undefined);
  c.add(conv, kindDirect, [a, b]);
  const e = c.get(conv);
  assert.equal(e?.members.length, 2);
  assert.ok(e.direct);
  const notes = newUUID();
  c.add(notes, kindNotes, [a]);
  assert.equal(c.get(notes)?.direct, false, "notes taken for a direct conversation");
});
