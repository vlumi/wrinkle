import assert from "node:assert/strict";
import { test } from "node:test";
import { Blobs } from "./blobs.ts";

// The vectors every backend shares.
const blobs = new Blobs({
  url: "http://127.0.0.1:9200",
  keyId: "GK7772696e6b6c6500000000",
  secret: "667c7e6aafc73808d30a89b14f18816b605d7d340823d9493b32b97aa7079fc4",
  region: "garage",
});
const blob = Buffer.from("00112233445566778899aabbccddeeff", "hex");
const at = new Date("2026-01-02T03:04:05Z");

test("presigned PUT", () => {
  assert.equal(
    blobs.presignPut(blob, "image/png", 1234n, at, 900),
    "/blobs/00112233445566778899aabbccddeeff?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=GK7772696e6b6c6500000000%2F20260102%2Fgarage%2Fs3%2Faws4_request&X-Amz-Date=20260102T030405Z&X-Amz-Expires=900&X-Amz-SignedHeaders=content-length%3Bcontent-type%3Bhost&X-Amz-Signature=41f91cfa6894289b36a84ba3dd2382a597515b97882580eba97a7beead3006e3",
  );
});

test("presigned DELETE", () => {
  assert.equal(
    blobs.presign("DELETE", "/blobs/00112233445566778899aabbccddeeff", {}, at, 60),
    "/blobs/00112233445566778899aabbccddeeff?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=GK7772696e6b6c6500000000%2F20260102%2Fgarage%2Fs3%2Faws4_request&X-Amz-Date=20260102T030405Z&X-Amz-Expires=60&X-Amz-SignedHeaders=host&X-Amz-Signature=63ca246240ae5e8ba3f157a95f71ddd3e094ee79692510754b5f3406b17b5a15",
  );
});
