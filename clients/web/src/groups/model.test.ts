import { create } from "@bufbuild/protobuf";
import type { MessageInitShape } from "@bufbuild/protobuf";
import { ConversationKind, ConversationSchema, MemberSchema, NoticeSchema } from "../gen/wrinkle/v1/model_pb";
import type { Notice, User } from "../gen/wrinkle/v1/model_pb";
import { hex } from "../ids";
import {
  applyNotice,
  membersAfter,
  nameList,
  namesProblem,
  noticeText,
  parseNames,
  readBy,
  topicProblem,
  withMemberRead,
} from "./model";
import type { Users } from "./model";

const id = (n: number) => new Uint8Array(16).fill(n);
const user = (n: number, name: string): User => ({ userId: id(n), name }) as User;
const alice = user(1, "Alice"),
  bob = user(2, "Bob"),
  carol = user(3, "Carol");
const users: Users = Object.fromEntries([alice, bob, carol].map((u) => [hex(u.userId), u]));
const notice = (kind: MessageInitShape<typeof NoticeSchema>["kind"]) => create(NoticeSchema, { kind });
const text = (n: Notice, sender: User, me: User = carol) => noticeText(n, sender.userId, me.userId, users);

test("names are listed with commas and a final and", () => {
  expect(nameList([])).toBe("");
  expect(nameList(["A"])).toBe("A");
  expect(nameList(["A", "B"])).toBe("A and B");
  expect(nameList(["A", "B", "C"])).toBe("A, B and C");
});

test("notices read as sentences", () => {
  expect(text(notice({ case: "groupCreated", value: { topic: "Trip", members: [bob] } }), alice, bob)).toBe(
    "Alice created the group “Trip” with you",
  );
  expect(text(notice({ case: "membersAdded", value: { users: [bob, carol] } }), alice, alice)).toBe(
    "You added Bob and Carol",
  );
  expect(text(notice({ case: "membersAdded", value: { users: [bob, carol] } }), alice, user(9, "Zed"))).toBe(
    "Alice added Bob and Carol",
  );
  expect(text(notice({ case: "memberRemoved", value: { user: bob } }), alice)).toBe("Alice removed Bob");
  expect(text(notice({ case: "memberLeft", value: {} }), bob)).toBe("Bob left");
  expect(text(notice({ case: "memberLeft", value: { promoted: alice } }), bob)).toBe("Bob left; Alice is now an admin");
  expect(text(notice({ case: "memberLeft", value: { promoted: carol } }), bob)).toBe("Bob left; you are now an admin");
  expect(text(notice({ case: "topicChanged", value: { topic: "Trip 2" } }), alice)).toBe(
    "Alice changed the topic to “Trip 2”",
  );
  expect(text(notice({ case: "adminChanged", value: { user: bob, admin: true } }), alice)).toBe(
    "Alice made Bob an admin",
  );
  expect(text(notice({ case: "adminChanged", value: { user: bob, admin: false } }), alice)).toBe(
    "Alice removed Bob as an admin",
  );
});

test("an unknown sender is someone", () => {
  expect(noticeText(notice({ case: "memberLeft", value: {} }), id(9), id(1), users)).toBe("Someone left");
});

test("notices change the group as the user sees it", () => {
  const group = create(ConversationSchema, {
    kind: ConversationKind.GROUP,
    topic: "Trip",
    memberCount: 3,
    member: true,
    admin: false,
  });
  const me = carol.userId;
  const added = applyNotice(
    group,
    notice({ case: "membersAdded", value: { users: [user(4, "Dan")] } }),
    alice.userId,
    me,
  );
  expect(added.memberCount).toBe(4);
  const removed = applyNotice(group, notice({ case: "memberRemoved", value: { user: carol } }), alice.userId, me);
  expect([removed.member, removed.memberCount]).toEqual([false, 2]);
  const promoted = applyNotice(group, notice({ case: "memberLeft", value: { promoted: carol } }), bob.userId, me);
  expect([promoted.member, promoted.admin, promoted.memberCount]).toEqual([true, true, 2]);
  const left = applyNotice(group, notice({ case: "memberLeft", value: {} }), carol.userId, me);
  expect([left.member, left.admin]).toEqual([false, false]);
  expect(applyNotice(group, notice({ case: "topicChanged", value: { topic: "New" } }), bob.userId, me).topic).toBe(
    "New",
  );
  const made = applyNotice(
    group,
    notice({ case: "adminChanged", value: { user: carol, admin: true } }),
    alice.userId,
    me,
  );
  expect(made.admin).toBe(true);
});

test("the members cache follows removals, departures and admin changes", () => {
  const members = [alice, bob, carol].map((u, i) => create(MemberSchema, { user: u, admin: i === 0 }));
  const names = (ms: typeof members) => ms.map((m) => `${m.user?.name}${m.admin ? "*" : ""}`);
  expect(names(membersAfter(members, notice({ case: "memberRemoved", value: { user: bob } }), alice.userId))).toEqual([
    "Alice*",
    "Carol",
  ]);
  expect(names(membersAfter(members, notice({ case: "memberLeft", value: { promoted: bob } }), alice.userId))).toEqual([
    "Bob*",
    "Carol",
  ]);
  expect(
    names(membersAfter(members, notice({ case: "adminChanged", value: { user: carol, admin: true } }), alice.userId)),
  ).toEqual(["Alice*", "Bob", "Carol*"]);
});

test("read by counts the other members who read up to the message", () => {
  let members = [alice, bob, carol].map((u) => create(MemberSchema, { user: u, readSeq: 1n }));
  expect(readBy(members, alice.userId, 3n)).toBe(0);
  members = withMemberRead(members, bob.userId, 3n);
  members = withMemberRead(members, alice.userId, 5n);
  expect(readBy(members, alice.userId, 3n)).toBe(1);
  members = withMemberRead(members, carol.userId, 4n);
  expect(readBy(members, alice.userId, 3n)).toBe(2);
  expect(withMemberRead(members, carol.userId, 2n)).toBe(members);
  expect(readBy(undefined, alice.userId, 1n)).toBe(0);
});

test("names are split on commas", () => {
  expect(parseNames(" bob, Carol Smith ,,")).toEqual(["bob", "Carol Smith"]);
  expect(namesProblem(["bob"])).toBeNull();
  expect(namesProblem([" bob"])).not.toBeNull();
});

test("topics are 1 to 200 bytes without surrounding space or control characters", () => {
  expect(topicProblem("Trip")).toBeNull();
  expect(topicProblem("")).not.toBeNull();
  expect(topicProblem("a".repeat(200))).toBeNull();
  expect(topicProblem("a".repeat(201))).not.toBeNull();
  expect(topicProblem(" Trip")).not.toBeNull();
  expect(topicProblem("Tr\u0007ip")).not.toBeNull();
});
