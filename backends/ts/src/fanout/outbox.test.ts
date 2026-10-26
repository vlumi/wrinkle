import assert from "node:assert/strict";
import { test } from "node:test";
import { create, toBinary } from "@bufbuild/protobuf";
import { EventSchema, MessageDeletedEventSchema } from "../gen/wrinkle/v1/event_pb.js";
import { newUUID } from "../internal/ids.ts";
import { isDirect, parseOutboxRow, parseUserOutboxRow, peerOf } from "./outbox.ts";

// Records as Debezium wrote them, binary columns in base64.

const b64 = (b: Uint8Array) => Buffer.from(b).toString("base64");

test("parses a conversation outbox row", () => {
  const value = Buffer.from(
    `{"conversation_id":"C4X3FkaURgebCmaEBLUfTA==","outbox_id":"6453981837480687173",` +
      `"recipients":"QdU4zkeJR2KIGb3J8piNBcm50CCOSk6KjTopI8AYktA=","conversation_kind":"group","topic":"Hikes","member_count":2,` +
      `"kind":"message","payload":"EgwIjaCu1gYQmKXepgNSQQo/ChALhfcWRpRGB5sKZoQEtR9MEAEaEEHVOM5HiUdiiBm9yfKYjQUiAW0qBmZyb20gYjIMCI2grtYGEJil3qYD","created_at":1791725581886543}`,
  );
  const o = parseOutboxRow(value);
  assert.equal(b64(o.conversationId), "C4X3FkaURgebCmaEBLUfTA==");
  assert.equal(o.outboxId, 6453981837480687173n);
  assert.equal(o.kind, "message");
  assert.equal(o.conversationKind, "group");
  assert.equal(isDirect(o), false);
  assert.equal(o.topic, "Hikes");
  assert.equal(o.memberCount, 2);
  assert.deepEqual(o.recipients.map(b64), ["QdU4zkeJR2KIGb3J8piNBQ==", "ybnQII5KToqNOikjwBiS0A=="]);
  assert.ok(peerOf(o, o.recipients[0]!).equals(o.recipients[1]!));
  assert.ok(peerOf(o, o.recipients[1]!).equals(o.recipients[0]!));
  const body = o.event.body;
  assert.equal(body.case, "message");
  const msg = body.value.message;
  assert.equal(msg?.seq, 1n);
  assert.ok(Buffer.from(msg.conversationId).equals(o.conversationId));
  assert.throws(() =>
    parseOutboxRow(
      Buffer.from(
        `{"conversation_id":"C4X3FkaURgebCmaEBLUfTA==","outbox_id":1,"recipients":"AAEC","member_count":0,"payload":""}`,
      ),
    ),
  );
});

test("parses a deletion's target and released uploads", () => {
  const payload = toBinary(
    EventSchema,
    create(EventSchema, { body: { case: "messageDeleted", value: create(MessageDeletedEventSchema, { seq: 3n }) } }),
  );
  const row = (target: string, released = "null") =>
    Buffer.from(
      `{"conversation_id":"C4X3FkaURgebCmaEBLUfTA==","outbox_id":"2","recipients":"","conversation_kind":"direct",` +
        `"member_count":2,"kind":"message_deleted","target_outbox_id":${target},"released_uploads":${released},` +
        `"payload":"${b64(payload)}"}`,
    );
  const sender = newUUID();
  const blob = newUUID();
  const o = parseOutboxRow(row(`"18446744073709551615"`, `"${b64(Buffer.concat([sender, blob]))}"`));
  assert.equal(o.targetOutboxId, 18446744073709551615n);
  assert.equal(o.event.body.case === "messageDeleted" ? o.event.body.value.seq : 0n, 3n);
  assert.ok(o.releasedBy?.equals(sender));
  assert.deepEqual(o.releasedUploads.map(b64), [b64(blob)]);
  const nulls = parseOutboxRow(row("null"));
  assert.equal(nulls.targetOutboxId, 0n);
  assert.equal(nulls.releasedBy, undefined);
  assert.deepEqual(nulls.releasedUploads, []);
  // The sender alone, or a partial id, is malformed.
  assert.throws(() => parseOutboxRow(row("null", `"${b64(sender)}"`)));
  assert.throws(() => parseOutboxRow(row("null", `"${b64(Buffer.concat([sender, blob.subarray(0, 8)]))}"`)));
  // Numbers beyond 2^53 keep every digit.
  assert.equal(parseOutboxRow(row("9007199254740993")).targetOutboxId, 9007199254740993n);
});

test("parses a user outbox row", () => {
  const value = Buffer.from(
    `{"user_id":"NhA4dCVFTBuu2mxC1WP1gw==","outbox_id":"1","kind":"profile","payload":"EgwIqqCu1gYQ0N6YiAFqLAoqChA2EDh0JUVMG67abELVY/WDEhFpY29uLTU3OWFiMWFjZjk3YRoDZm94","created_at":1791725610285618}`,
  );
  const o = parseUserOutboxRow(value);
  assert.equal(o.outboxId, 1n);
  assert.equal(o.kind, "profile");
  const user = o.event.body.case === "profile" ? o.event.body.value.user : undefined;
  assert.ok(Buffer.from(user!.userId).equals(o.userId));
  assert.equal(user!.name, "icon-579ab1acf97a");
  assert.equal(user!.icon, "fox");
});

test("rejects short ids and malformed numbers", () => {
  assert.throws(() =>
    parseUserOutboxRow(Buffer.from(`{"user_id":"AAEC","outbox_id":1,"kind":"profile","payload":""}`)),
  );
  assert.throws(() =>
    parseUserOutboxRow(
      Buffer.from(`{"user_id":"NhA4dCVFTBuu2mxC1WP1gw==","outbox_id":"x","kind":"profile","payload":""}`),
    ),
  );
});
