import assert from "node:assert/strict";
import { test } from "node:test";
import { create } from "@bufbuild/protobuf";
import { MessageSchema, SealedSchema, type Message } from "../gen/wrinkle/v1/model_pb.js";
import { maxPayload, payload } from "./apns.ts";
import { messageAlert } from "./notifier.ts";

const sealedMessage = (ciphertext: number): Message =>
  create(MessageSchema, {
    seq: 7n,
    senderUserId: Buffer.alloc(16, 0xab),
    clientMessageId: "m-1",
    content: {
      case: "sealed",
      value: create(SealedSchema, { epoch: 3, nonce: Buffer.alloc(12, 1), ciphertext: Buffer.alloc(ciphertext, 2) }),
    },
  });

const conversationId = Buffer.concat([Buffer.from([0x42]), Buffer.alloc(15)]);

test("a message's alert carries the sealed content", () => {
  const body = JSON.parse(payload(messageAlert("bob", 2, conversationId, sealedMessage(100))).toString()) as Record<
    string,
    unknown
  >;
  const aps = body["aps"] as Record<string, unknown>;
  assert.equal(aps["mutable-content"], 1);
  assert.deepEqual(aps["alert"], { title: "bob", body: "New message" });
  assert.equal(aps["badge"], 2);
  assert.equal(aps["thread-id"], conversationId.toString("hex"));
  for (const [k, v] of Object.entries({
    conversation_id: conversationId.toString("hex"),
    seq: 7,
    epoch: 3,
    nonce: Buffer.alloc(12, 1).toString("base64"),
    ciphertext: Buffer.alloc(100, 2).toString("base64"),
    sender_user_id: "abababababababababababababababab",
    client_message_id: "m-1",
  })) {
    assert.equal(body[k], v, k);
  }
});

test("a message's alert leaves out content that does not fit", () => {
  const raw = payload(messageAlert("bob", 2, conversationId, sealedMessage(4000)));
  assert.ok(raw.length <= maxPayload, `payload of ${raw.length} bytes`);
  const body = JSON.parse(raw.toString()) as Record<string, unknown>;
  for (const k of ["epoch", "nonce", "ciphertext", "sender_user_id", "client_message_id"]) assert.ok(!(k in body), k);
  assert.equal(body["seq"], 7);
  assert.equal((body["aps"] as Record<string, unknown>)["mutable-content"], 1);
});
