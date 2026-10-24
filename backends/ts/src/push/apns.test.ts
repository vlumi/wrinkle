import assert from "node:assert/strict";
import { generateKeyPairSync, verify, type KeyObject } from "node:crypto";
import { mkdtempSync, writeFileSync } from "node:fs";
import http2 from "node:http2";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, type TestContext } from "node:test";
import { APNs, keyIdFromFileName, UnregisteredError } from "./apns.ts";

function testKey(): { pub: KeyObject; path: string } {
  const { privateKey, publicKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
  const path = join(mkdtempSync(join(tmpdir(), "apns-")), "AuthKey.p8");
  writeFileSync(path, privateKey.export({ type: "pkcs8", format: "pem" }), { mode: 0o600 });
  return { pub: publicKey, path };
}

interface Seen {
  headers: http2.IncomingHttpHeaders;
  body: Record<string, unknown>;
}

/** A local HTTP/2 server, cleartext, standing in for APNs. */
async function server(
  t: TestContext,
  respond: (res: http2.Http2ServerResponse) => void,
): Promise<{ url: string; seen: Seen[] }> {
  const seen: Seen[] = [];
  const srv = http2.createServer((req, res) => {
    let raw = "";
    req.setEncoding("utf8");
    req.on("data", (c: string) => (raw += c));
    req.on("end", () => {
      seen.push({ headers: req.headers, body: JSON.parse(raw) as Record<string, unknown> });
      respond(res);
    });
  });
  await new Promise<void>((r) => srv.listen(0, "127.0.0.1", r));
  t.after(() => srv.close());
  return { url: `http://127.0.0.1:${(srv.address() as AddressInfo).port}`, seen };
}

function verifyJwt(token: string, pub: KeyObject) {
  const parts = token.split(".");
  assert.equal(parts.length, 3);
  const sig = Buffer.from(parts[2]!, "base64url");
  assert.equal(sig.length, 64);
  assert.ok(verify("sha256", Buffer.from(`${parts[0]!}.${parts[1]!}`), { key: pub, dsaEncoding: "ieee-p1363" }, sig));
  const header = JSON.parse(Buffer.from(parts[0]!, "base64url").toString()) as Record<string, unknown>;
  assert.deepEqual(header, { alg: "ES256", kid: "KEY123" });
  const claims = JSON.parse(Buffer.from(parts[1]!, "base64url").toString()) as Record<string, unknown>;
  assert.equal(claims["iss"], "TEAM456");
  assert.equal(typeof claims["iat"], "number");
}

test("send signs and addresses the request", async (t) => {
  const { pub, path } = testKey();
  const { url, seen } = await server(t, (res) => res.writeHead(200).end());
  const a = new APNs({
    keyFile: path,
    keyId: "KEY123",
    teamId: "TEAM456",
    topic: "fi.example.app",
    productionUrl: url + "/prod",
    sandboxUrl: url,
  });
  t.after(() => a.close());
  await a.send(true, "abc123", {
    title: "bob",
    body: "hello",
    badge: 3,
    threadId: "42",
    data: { conversation_id: "42", seq: 7 },
  });
  const got = seen[0]!;
  assert.equal(got.headers[":path"], "/3/device/abc123", "the sandbox URL should be used");
  assert.equal(got.headers[":method"], "POST");
  assert.equal(got.headers["apns-topic"], "fi.example.app");
  assert.equal(got.headers["apns-push-type"], "alert");
  assert.equal(got.headers["apns-priority"], "10");
  verifyJwt(String(got.headers.authorization).replace(/^bearer /, ""), pub);
  assert.deepEqual(got.body, {
    aps: { alert: { title: "bob", body: "hello" }, badge: 3, sound: "default", "thread-id": "42" },
    conversation_id: "42",
    seq: 7,
  });

  await a.send(false, "def", { title: "x" });
  assert.equal(seen[1]!.headers[":path"], "/prod/3/device/def");
});

test("send background", async (t) => {
  const { path } = testKey();
  const { url, seen } = await server(t, (res) => res.writeHead(200).end());
  const a = new APNs({ keyFile: path, keyId: "KEY123", teamId: "TEAM456", topic: "t", productionUrl: url });
  t.after(() => a.close());
  await a.send(false, "tok", { background: true, data: { conversation_id: "42", seq: 7, deleted: true } });
  const got = seen[0]!;
  assert.equal(got.headers["apns-push-type"], "background");
  assert.equal(got.headers["apns-priority"], "5");
  assert.deepEqual(got.body, { aps: { "content-available": 1 }, conversation_id: "42", seq: 7, deleted: true });
});

test("send reports unregistered tokens", async (t) => {
  const { path } = testKey();
  for (const [status, reason] of [
    [410, "Unregistered"],
    [400, "BadDeviceToken"],
  ] as const) {
    const { url } = await server(t, (res) => res.writeHead(status).end(JSON.stringify({ reason })));
    const a = new APNs({ keyFile: path, keyId: "KEY123", teamId: "TEAM456", topic: "t", productionUrl: url });
    t.after(() => a.close());
    await assert.rejects(a.send(false, "tok", { title: "x", body: "y" }), UnregisteredError, `${status} ${reason}`);
  }
  const { url } = await server(t, (res) => res.writeHead(500).end(JSON.stringify({ reason: "InternalServerError" })));
  const a = new APNs({ keyFile: path, keyId: "KEY123", teamId: "TEAM456", topic: "t", productionUrl: url });
  t.after(() => a.close());
  await assert.rejects(a.send(false, "tok", {}), (err) => !(err instanceof UnregisteredError));
});

test("provider token is reused", () => {
  const { path } = testKey();
  const a = new APNs({ keyFile: path, keyId: "K", teamId: "T", topic: "t" });
  assert.equal(
    a.providerToken(),
    a.providerToken(),
    "a fresh token was signed within its lifetime; Apple throttles that",
  );
});

test("key id from the file name", () => {
  assert.equal(keyIdFromFileName("/x/AuthKey_ABCDE12345.p8"), "ABCDE12345");
  assert.equal(keyIdFromFileName("/x/key.p8"), "");
});
