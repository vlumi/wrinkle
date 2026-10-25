import assert from "node:assert/strict";
import { test } from "node:test";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb.js";
import { codeOf } from "../internal/apierror.ts";
import { newUUID, type UUID } from "../internal/ids.ts";
import { checkClientGroupId, checkTopic } from "./group.ts";
import { maxBlobIds, parseBlobIds } from "./service.ts";
import {
  decodeBlobIds,
  decodeMembers,
  encodeBlobIds,
  encodeMembers,
  has,
  membershipChanged,
  noticeId,
  without,
  type State,
} from "./state.ts";

const id = (first: number): UUID => {
  const b = Buffer.alloc(16);
  b[0] = first;
  return b;
};

const fails = (fn: () => unknown): ErrorCode => {
  try {
    fn();
  } catch (err) {
    return codeOf(err);
  }
  return ErrorCode.UNSPECIFIED;
};

test("members", () => {
  const [a, b, c] = [id(0x80), id(0x01), id(0x7f)];
  const encoded = encodeMembers([a, b, c]);
  assert.deepEqual(encoded, Buffer.concat([b, c, a]), "sorted unsigned");
  const members = decodeMembers(encoded);
  assert.ok(has(members, a) && has(members, b) && has(members, c));
  assert.ok(!has(members, id(0x02)));
  assert.deepEqual(without(members, c), [b, a]);
  assert.equal(members.length, 3);
  assert.throws(() => decodeMembers(Buffer.alloc(17)));
});

test("topic", () => {
  const cases: [string, boolean][] = [
    ["Hikes", true],
    ["", false],
    [" a", false],
    ["a ", false],
    ["a\tb", false],
    ["é".repeat(100), true],
    ["t".repeat(201), false],
    ["\ud800", false],
  ];
  for (const [topic, ok] of cases)
    assert.equal(fails(() => checkTopic(topic)) === ErrorCode.UNSPECIFIED, ok, JSON.stringify(topic));
});

test("client group id", () => {
  assert.equal(
    fails(() => checkClientGroupId("g1")),
    ErrorCode.UNSPECIFIED,
  );
  assert.equal(
    fails(() => checkClientGroupId("")),
    ErrorCode.INVALID_ARGUMENT,
  );
  assert.equal(
    fails(() => checkClientGroupId("x".repeat(65))),
    ErrorCode.INVALID_ARGUMENT,
  );
});

test("membership change rotates only after the first epoch", () => {
  const st: State = { kind: "group", topic: "", members: [], lastSeq: 0n, keyEpoch: 0, rotate: false, coldSeq: 0n };
  membershipChanged(st);
  assert.ok(!st.rotate, "rotate before any epoch");
  st.keyEpoch = 1;
  membershipChanged(st);
  assert.ok(st.rotate, "no rotate after epoch 1");
});

test("blob ids", () => {
  const [a, b] = [newUUID(), newUUID()];
  assert.deepEqual(parseBlobIds([a, b]), [a, b]);
  for (const bad of [[a, a], [a.subarray(0, 15)], Array<Uint8Array>(maxBlobIds + 1).fill(a)])
    assert.equal(
      fails(() => parseBlobIds(bad)),
      ErrorCode.INVALID_ARGUMENT,
    );
  const stored = encodeBlobIds([a, b]);
  assert.deepEqual(
    decodeBlobIds(stored).map((x) => Buffer.from(x)),
    [a, b],
  );
  assert.deepEqual(decodeBlobIds(null), []);
  assert.deepEqual(decodeBlobIds(Buffer.alloc(0)), []);
});

test("notice ids", () => {
  const n = noticeId();
  assert.match(n, /^notice-[0-9a-f]{16}$/);
  assert.notEqual(noticeId(), n);
});
