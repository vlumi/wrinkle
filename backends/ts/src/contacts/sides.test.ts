import assert from "node:assert/strict";
import { test } from "node:test";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { create } from "@bufbuild/protobuf";
import { ContactState, UserSchema } from "../gen/wrinkle/v1/model_pb.js";
import { Block, contactOf, None, payloadSide, payloadState, Request, view } from "./sides.ts";

test("view", () => {
  const cases: [string, string, ContactState][] = [
    [Block, Request, ContactState.BLOCKED],
    [Block, "", ContactState.BLOCKED],
    [Request, Request, ContactState.CONTACT],
    [Request, "", ContactState.OUTGOING],
    [Request, None, ContactState.OUTGOING],
    [Request, Block, ContactState.OUTGOING],
    ["", Request, ContactState.INCOMING],
    ["", Block, ContactState.UNSPECIFIED],
    ["", None, ContactState.UNSPECIFIED],
    ["", "", ContactState.UNSPECIFIED],
  ];
  for (const [own, peer, want] of cases) assert.equal(view(own, peer), want, `${own}/${peer}`);
});

test("payload side round trips", () => {
  for (const side of [Request, Block, None]) assert.equal(payloadSide(payloadState(side)), side);
  assert.equal(payloadState(""), ContactState.UNSPECIFIED);
});

test("contact takes the later change", () => {
  const early = new Date(Date.UTC(2026, 9, 14, 10));
  const late = new Date(early.getTime() + 60_000);
  const c = contactOf(
    create(UserSchema, { name: "bob" }),
    { state: Request, updatedAt: early },
    { state: Request, updatedAt: late },
  );
  assert.equal(c.state, ContactState.CONTACT);
  assert.equal(timestampDate(c.updatedAt!).getTime(), late.getTime());
  assert.equal(contactOf(create(UserSchema), { state: "" }, { state: "" }).updatedAt, undefined);
});
