import assert from "node:assert/strict";
import { test } from "node:test";
import { newUUID } from "../internal/ids.ts";
import { argonMemory, argonThreads, argonTime, encodeHash, hashPassword, verifyPassword } from "./password.ts";
import { parseToken, newToken, tokenHash, tokenString } from "./token.ts";
import { validIcon, validName } from "./validate.ts";

// argon2id of "correct horse" with the salt "wrinkle-salt-16b" and the
// standard parameters. Every backend's test checks the same string, so each
// verifies what the others write.
const knownHash = "$argon2id$v=19$m=19456,t=2,p=1$d3JpbmtsZS1zYWx0LTE2Yg$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc";

test("known hash", async () => {
  assert.equal(
    await encodeHash("correct horse", Buffer.from("wrinkle-salt-16b"), argonMemory, argonTime, argonThreads),
    knownHash,
  );
  assert.ok(await verifyPassword("correct horse", knownHash));
  assert.equal(await verifyPassword("correct horse!", knownHash), false);
});

test("hash round trip", async () => {
  const hash = await hashPassword("battery staple");
  assert.ok(hash.startsWith("$argon2id$v=19$m=19456,t=2,p=1$"), hash);
  assert.ok(await verifyPassword("battery staple", hash));
  assert.notEqual(await hashPassword("battery staple"), hash);
});

test("malformed hashes", async () => {
  for (const h of [
    "",
    "$argon2i$v=19$m=19456,t=2,p=1$d3JpbmtsZS1zYWx0LTE2Yg$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc",
    "$argon2id$v=16$m=19456,t=2,p=1$d3JpbmtsZS1zYWx0LTE2Yg$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc",
    "$argon2id$v=19$m=19456,t=2,p=1x$d3JpbmtsZS1zYWx0LTE2Yg$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc",
    "$argon2id$v=19$m=19456,t=2,p=1$d3JpbmtsZS1zYWx0LTE2Yg==$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc",
    "$argon2id$v=19$m=99999999,t=2,p=1$d3JpbmtsZS1zYWx0LTE2Yg$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc",
  ])
    await assert.rejects(verifyPassword("correct horse", h), h);
});

test("token", () => {
  const userId = newUUID();
  const token = newToken(userId, 42n);
  const s = tokenString(token);
  assert.ok(s.startsWith("v1.") && s.length === 3 + 75, s);
  const parsed = parseToken(s);
  assert.ok(parsed);
  assert.deepEqual(parsed.userId, userId);
  assert.equal(parsed.deviceId, 42n);
  assert.deepEqual(tokenHash(parsed), tokenHash(token));
  for (const bad of ["", "v1.", "v2." + s.slice(3), s + "A", s.slice(0, -1), s.replace("v1.", "v1.+")])
    assert.equal(parseToken(bad), undefined, bad);
});

test("token layout", () => {
  // user id, device id big-endian, secret: fixed bytes give a fixed token.
  const userId = Buffer.alloc(16);
  userId[0] = 0xff;
  userId[15] = 0x01;
  const token = { userId, deviceId: 0x0102030405060708n, secret: Buffer.alloc(32) };
  assert.equal(tokenString(token), "v1._wAAAAAAAAAAAAAAAAAAAQECAwQFBgcIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA");
});

test("names", () => {
  for (const name of ["a", "Bob", "Ana María", "n".repeat(64), "😀".repeat(16), "émile"])
    assert.ok(validName(name), name);
  for (const name of [
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
  ])
    assert.equal(validName(name), false, JSON.stringify(name));
});

test("icons", () => {
  for (const icon of ["", "cat", "unicorn"]) assert.ok(validIcon(icon), icon);
  for (const icon of ["Cat", "dragon", " cat"]) assert.equal(validIcon(icon), false, icon);
});
