import assert from "node:assert/strict";
import { test } from "node:test";
import { create } from "@bufbuild/protobuf";
import {
  AdminChangedSchema,
  GroupCreatedSchema,
  MemberLeftSchema,
  MemberRemovedSchema,
  MembersAddedSchema,
  MessageSchema,
  NoticeSchema,
  SealedSchema,
  UserSchema,
  type Message,
  type Notice,
} from "../gen/wrinkle/v1/model_pb.js";
import { newUUID, type UUID } from "../internal/ids.ts";
import { membership, type Flag } from "./deliver.ts";

test("membership flags of notices", () => {
  const [a, b, c] = [newUUID(), newUUID(), newUUID()];
  const user = (id: UUID) => create(UserSchema, { userId: id });
  const notice = (sender: UUID, kind: Notice["kind"]): Message =>
    create(MessageSchema, {
      senderUserId: sender,
      content: { case: "notice", value: create(NoticeSchema, { kind }) },
    });
  const created = notice(a, { case: "groupCreated", value: create(GroupCreatedSchema) });
  const left = notice(a, { case: "memberLeft", value: create(MemberLeftSchema, { promoted: user(b) }) });
  const unset: Flag = { set: false, value: false };
  const set = (value: boolean): Flag => ({ set: true, value });
  const cases: [string, UUID, Message, Flag, Flag][] = [
    ["creator", a, created, set(true), set(true)],
    ["invitee", b, created, set(true), set(false)],
    [
      "added",
      c,
      notice(a, { case: "membersAdded", value: create(MembersAddedSchema, { users: [user(b), user(c)] }) }),
      set(true),
      set(false),
    ],
    [
      "not added",
      a,
      notice(a, { case: "membersAdded", value: create(MembersAddedSchema, { users: [user(c)] }) }),
      unset,
      unset,
    ],
    [
      "removed",
      c,
      notice(a, { case: "memberRemoved", value: create(MemberRemovedSchema, { user: user(c) }) }),
      set(false),
      set(false),
    ],
    ["leaver", a, left, set(false), set(false)],
    ["promoted", b, left, unset, set(true)],
    ["bystander", c, left, unset, unset],
    [
      "demoted",
      b,
      notice(a, { case: "adminChanged", value: create(AdminChangedSchema, { user: user(b) }) }),
      unset,
      set(false),
    ],
    [
      "sealed",
      b,
      create(MessageSchema, {
        senderUserId: a,
        content: { case: "sealed", value: create(SealedSchema, { epoch: 1 }) },
      }),
      unset,
      unset,
    ],
  ];
  for (const [name, id, msg, member, admin] of cases) {
    assert.deepEqual(membership(id, msg), { member, admin }, name);
  }
});
