import assert from "node:assert/strict";
import { test } from "node:test";
import { keyspaceId, Shards } from "./shards.ts";
import { xxh64 } from "./xxhash.ts";

test("keyspace ids match Vitess", () => {
  // Pairs read from vtgate's xxhash vindex function.
  for (const [id, want] of [
    ["004eb6acc9704010b1387b332394a2db", "ac758b75e4c3665c"],
    ["005dff9967944895b74f28fa37d8661a", "cd0363ddcc380d5b"],
  ] as const) {
    assert.equal(keyspaceId(Buffer.from(id, "hex")).toString("hex"), want);
  }
});

test("xxh64 reference values", () => {
  // From the reference implementation, seed 0.
  assert.equal(xxh64(Buffer.alloc(0)), 0xef46db3751d8e999n);
  assert.equal(xxh64(Buffer.from("a")), 0xd24ec4f1a98c6e5bn);
  assert.equal(xxh64(Buffer.from("abc")), 0x44bc2cf5ad770999n);
  assert.equal(xxh64(Buffer.from("Nobody inspects the spammish repetition")), 0xfbcea83c8a378bf1n);
});

test("shards place ids", () => {
  const s = new Shards(["-80", "80-"], [Buffer.alloc(0), Buffer.from([0x80])]);
  const id = Buffer.from("004eb6acc9704010b1387b332394a2db", "hex"); // keyspace id ac...
  assert.equal(s.of(id), "80-");
  assert.equal(new Shards().of(id), "");
});
