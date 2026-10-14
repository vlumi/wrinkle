import { create } from "@bufbuild/protobuf";
import { BackendError } from "../connection/connection";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import { ContactSchema, ContactState, SealedSchema, UserSchema } from "../gen/wrinkle/v1/model_pb";
import { hex } from "../ids";
import type { Entry } from "../reactions/model";
import {
  blockedIds,
  incomingCount,
  listed,
  mergeListed,
  notContact,
  pickable,
  sections,
  stateOf,
  withContact,
  withoutBlocked,
  withProfile,
} from "./model";

const id = (n: number) => new Uint8Array(16).fill(n);
const user = (n: number, name: string) => create(UserSchema, { userId: id(n), name });
const contact = (n: number, name: string, state: ContactState) => create(ContactSchema, { user: user(n, name), state });
const names = (list: { user?: { name: string } }[]) => list.map((c) => c.user?.name);

test("a listing keeps every state but none", () => {
  const contacts = listed([
    contact(2, "bob", ContactState.CONTACT),
    contact(3, "carol", ContactState.UNSPECIFIED),
    contact(4, "dave", ContactState.BLOCKED),
  ]);
  expect(Object.keys(contacts)).toEqual([hex(id(2)), hex(id(4))]);
  expect(stateOf(contacts, id(2))).toBe(ContactState.CONTACT);
  expect(stateOf(contacts, id(3))).toBe(ContactState.UNSPECIFIED);
  expect(stateOf(null, id(2))).toBe(ContactState.UNSPECIFIED);
});

test("events move a user between states, and none removes them", () => {
  let contacts = withContact({}, contact(2, "bob", ContactState.OUTGOING));
  expect(stateOf(contacts, id(2))).toBe(ContactState.OUTGOING);
  contacts = withContact(contacts, contact(2, "bob", ContactState.CONTACT));
  expect(stateOf(contacts, id(2))).toBe(ContactState.CONTACT);
  contacts = withContact(contacts, contact(2, "bob", ContactState.UNSPECIFIED));
  expect(contacts).toEqual({});
  expect(withContact(contacts, contact(3, "carol", ContactState.UNSPECIFIED))).toBe(contacts);
});

test("a listing gives way to events that arrived while it was on the way", () => {
  const local = listed([contact(2, "bob", ContactState.CONTACT), contact(5, "erin", ContactState.BLOCKED)]);
  const list = [
    contact(2, "bob", ContactState.OUTGOING),
    contact(3, "carol", ContactState.INCOMING),
    contact(4, "dave", ContactState.CONTACT),
  ];
  // Bob accepted and Carol withdrew during the listing; Dave did nothing.
  const merged = mergeListed(local, list, new Set([hex(id(2)), hex(id(3))]));
  expect(stateOf(merged, id(2))).toBe(ContactState.CONTACT);
  expect(stateOf(merged, id(3))).toBe(ContactState.UNSPECIFIED);
  expect(stateOf(merged, id(4))).toBe(ContactState.CONTACT);
  // Untouched users follow the listing.
  expect(stateOf(merged, id(5))).toBe(ContactState.UNSPECIFIED);
});

test("a profile change renames a contact, and leaves others alone", () => {
  const contacts = listed([contact(2, "bob", ContactState.CONTACT)]);
  expect(withProfile(contacts, user(2, "robert"))[hex(id(2))].user?.name).toBe("robert");
  expect(withProfile(contacts, user(3, "carol"))).toBe(contacts);
});

test("the screen's sections and the badge's count", () => {
  const contacts = listed([
    contact(2, "zed", ContactState.CONTACT),
    contact(3, "amy", ContactState.CONTACT),
    contact(4, "carol", ContactState.INCOMING),
    contact(5, "dave", ContactState.INCOMING),
    contact(6, "erin", ContactState.OUTGOING),
    contact(7, "fay", ContactState.BLOCKED),
  ]);
  const s = sections(contacts);
  expect(names(s.contacts)).toEqual(["amy", "zed"]);
  expect(names(s.incoming)).toEqual(["carol", "dave"]);
  expect(names(s.outgoing)).toEqual(["erin"]);
  expect(names(s.blocked)).toEqual(["fay"]);
  expect(incomingCount(contacts)).toBe(2);
  expect([...blockedIds(contacts)]).toEqual([hex(id(7))]);
});

test("members are picked from contacts only, filtered by name and without those excluded", () => {
  const contacts = listed([
    contact(2, "Bob", ContactState.CONTACT),
    contact(3, "bobby", ContactState.CONTACT),
    contact(4, "carol", ContactState.CONTACT),
    contact(5, "bo", ContactState.OUTGOING),
    contact(6, "boris", ContactState.BLOCKED),
  ]);
  expect(pickable(contacts, "", new Set()).map((u) => u.name)).toEqual(["Bob", "bobby", "carol"]);
  expect(pickable(contacts, " BO", new Set()).map((u) => u.name)).toEqual(["Bob", "bobby"]);
  expect(pickable(contacts, "bo", new Set([hex(id(3))])).map((u) => u.name)).toEqual(["Bob"]);
  expect(pickable(null, "", new Set())).toEqual([]);
});

test("blocked users' reactions are left out of a message's", () => {
  const entry: Entry = { sealed: create(SealedSchema, { epoch: 1 }), emoji: ["👍"] };
  const users = { [hex(id(2))]: entry, [hex(id(3))]: entry };
  expect(Object.keys(withoutBlocked(users, new Set([hex(id(3))])) ?? {})).toEqual([hex(id(2))]);
  expect(withoutBlocked(users, new Set([hex(id(9))]))).toBe(users);
  expect(withoutBlocked(undefined, new Set([hex(id(3))]))).toBeUndefined();
});

test("a group refused for a non-contact names them", () => {
  expect(notContact(new BackendError(ErrorCode.FAILED_PRECONDITION, "not a contact: Bob"))).toBe("Bob");
  expect(notContact(new BackendError(ErrorCode.FAILED_PRECONDITION, "too many members"))).toBeNull();
  expect(notContact(new BackendError(ErrorCode.NOT_FOUND, "not a contact: Bob"))).toBeNull();
  expect(notContact(new Error("not a contact: Bob"))).toBeNull();
});
