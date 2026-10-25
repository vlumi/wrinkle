import assert from "node:assert/strict";
import { test } from "node:test";
import { create } from "@bufbuild/protobuf";
import { timestampNow } from "@bufbuild/protobuf/wkt";
import { MessageSchema, ReactionSchema, SealedSchema, type Message } from "../gen/wrinkle/v1/model_pb.js";
import { timestampOf, type DbTime } from "../internal/db.ts";
import { cellsOf, coldKey, coldMessage, coldMessageOf, encodeReactions, type ColdMessage } from "./cold.ts";
import { coldRange } from "./service.ts";
import { encodeBody } from "./state.ts";

const id = (first: number, last = 0): Buffer => {
  const b = Buffer.alloc(16);
  b[0] = first;
  b[15] = last;
  return b;
};

test("cold keys sort a conversation's rows by seq, within its id", () => {
  const conv = id(0xab, 0x01);
  assert.deepEqual(coldKey(conv, 0x0102n), Buffer.concat([conv, Buffer.from([0, 0, 0, 0, 0, 0, 1, 2])]));
  assert.ok(Buffer.compare(coldKey(conv, 255n), coldKey(conv, 256n)) < 0);
  assert.ok(Buffer.compare(coldKey(conv, (1n << 64n) - 1n), coldKey(id(0xab, 0x02), 0n)) < 0);
});

test("a cold row reads back as written", () => {
  const conv = id(0x42);
  const body = Buffer.from(
    encodeBody({
      content: {
        case: "sealed",
        value: create(SealedSchema, { epoch: 3, nonce: Buffer.from("n"), ciphertext: Buffer.from("c") }),
      },
      deletedAt: undefined,
    }),
  );
  const reactedAt: DbTime = new Date(0);
  reactedAt.micros = 5;
  const reaction = create(ReactionSchema, {
    userId: id(0x07),
    sealed: create(SealedSchema, { epoch: 3, nonce: Buffer.from("r") }),
    updatedAt: timestampOf(reactedAt),
  });
  const sentAt: DbTime = new Date(1_700_000_000_123);
  sentAt.micros = 456;
  const m: ColdMessage = {
    seq: 9n,
    sender: id(0x01),
    clientId: "c1",
    body,
    sentAt,
    outboxId: 77n,
    blobIds: Buffer.alloc(32, 0xbb),
    reactions: encodeReactions([reaction]),
  };
  const got = coldMessageOf(coldKey(conv, m.seq), cellsOf(m));
  assert.deepEqual({ ...got, sentAt: got.sentAt.getTime() }, { ...m, sentAt: sentAt.getTime() });
  assert.equal(got.sentAt.micros, 456);

  const msg = coldMessage(conv, got);
  assert.equal(msg.seq, 9n);
  assert.equal(msg.content.case === "sealed" && msg.content.value.epoch, 3);
  assert.equal(msg.blobIds.length, 2);
  assert.deepEqual(msg.sentAt, timestampOf(sentAt));
  assert.equal(msg.reactions.length, 1);
  assert.deepEqual(msg.reactions[0], reaction);

  // A deleted message keeps no reactions.
  got.body = Buffer.from(encodeBody({ content: { case: undefined }, deletedAt: timestampNow() }));
  const deleted = coldMessage(conv, got);
  assert.ok(deleted.deletedAt);
  assert.equal(deleted.reactions.length, 0);
  assert.equal(encodeReactions([]).length, 0);
});

test("a page goes on in the cold tier below what MySQL gave", () => {
  const page = (...seqs: number[]): Message[] => seqs.map((s) => create(MessageSchema, { seq: BigInt(s) }));
  const all = (1n << 64n) - 1n;
  const cases: [string, Message[], bigint, bigint, bigint, bigint, number, bigint, boolean, boolean][] = [
    // name, page, joined, upTo, before, cold, limit, hi, more, again
    ["all hot, page full", page(12, 11), 1n, all, all, 10n, 2, 10n, false, false],
    ["goes on below the lowest", page(12, 11), 1n, all, all, 10n, 5, 10n, true, false],
    ["none in MySQL", [], 1n, all, all, 10n, 5, 10n, true, false],
    ["before the watermark", [], 1n, all, 4n, 10n, 5, 3n, true, false],
    ["until a member left", [], 1n, 6n, all, 10n, 5, 6n, true, false],
    ["nothing cold", page(3, 2, 1), 1n, all, all, 0n, 5, 0n, false, false],
    ["joined after the watermark", page(12), 11n, all, all, 10n, 5, 10n, false, false],
    ["before the first", [], 1n, all, 1n, 10n, 5, 0n, false, false],
    ["moved meanwhile", page(12, 11, 10), 1n, all, all, 10n, 5, 0n, false, true],
  ];
  for (const [name, p, joined, upTo, before, cold, limit, hi, more, again] of cases) {
    const got = coldRange(p, joined, upTo, before, cold, limit);
    assert.equal(got.more, more, name);
    assert.equal(got.again, again, name);
    if (more) assert.equal(got.hi, hi, name);
  }
});
