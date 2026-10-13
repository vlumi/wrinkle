// Groups: notices as sentences, how they change a conversation, the members
// cache and read counts. Pure functions over plain data, so they are tested
// without a connection.

import { displayName } from "../account/icons";
import { nameProblem } from "../account/rules";
import { ConversationKind } from "../gen/wrinkle/v1/model_pb";
import type { Conversation, Member, Message, Notice, User } from "../gen/wrinkle/v1/model_pb";
import { hex, sameId } from "../ids";

/** Users seen so far, by hex id: members, peers and the users in notices. */
export type Users = Record<string, User>;

const MAX_MEMBERS = 256;

export const isGroup = (c: Conversation | undefined): boolean => c?.kind === ConversationKind.GROUP;

/** A group the user left or was removed from. */
export const isFormer = (c: Conversation | undefined): boolean => isGroup(c) && !c?.member;

export function noticeOf(m: Message | undefined): Notice | undefined {
  return m?.content.case === "notice" ? m.content.value : undefined;
}

/** "A", "A and B", "A, B and C". */
export function nameList(names: string[]): string {
  if (names.length <= 1) return names[0] ?? "";
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/** A user in a sentence: "you" for the signed-in user. */
function person(user: User | undefined, me: Uint8Array | undefined): string {
  if (!user) return "someone";
  if (sameId(user.userId, me)) return "you";
  return displayName(user) || "someone";
}

/** A notice as a sentence, such as "Alice added Bob and Carol". */
export function noticeText(n: Notice, sender: Uint8Array, me: Uint8Array | undefined, users: Users): string {
  const known = users[hex(sender)];
  const actor = sameId(sender, me) ? "You" : (known && displayName(known)) || "Someone";
  const one = (u: User | undefined) => person(u, me);
  const k = n.kind;
  switch (k.case) {
    case "groupCreated": {
      const created = `${actor} created the group “${k.value.topic}”`;
      return k.value.members.length > 0 ? `${created} with ${nameList(k.value.members.map(one))}` : created;
    }
    case "membersAdded":
      return `${actor} added ${nameList(k.value.users.map(one))}`;
    case "memberRemoved":
      return `${actor} removed ${one(k.value.user)}`;
    case "memberLeft": {
      const promoted = k.value.promoted;
      if (!promoted) return `${actor} left`;
      const p = one(promoted);
      return `${actor} left; ${p} ${sameId(promoted.userId, me) ? "are" : "is"} now an admin`;
    }
    case "topicChanged":
      return `${actor} changed the topic to “${k.value.topic}”`;
    case "adminChanged":
      return k.value.admin
        ? `${actor} made ${one(k.value.user)} an admin`
        : `${actor} removed ${one(k.value.user)} as an admin`;
    default:
      return `${actor} changed the group`;
  }
}

/** The user snapshots a notice carries. */
export function noticeUsers(n: Notice): User[] {
  const k = n.kind;
  switch (k.case) {
    case "groupCreated":
      return k.value.members;
    case "membersAdded":
      return k.value.users;
    case "memberRemoved":
    case "adminChanged":
      return k.value.user ? [k.value.user] : [];
    case "memberLeft":
      return k.value.promoted ? [k.value.promoted] : [];
    default:
      return [];
  }
}

/** Whether a notice changes who is in the group or who is an admin. */
export function changesMembers(n: Notice): boolean {
  return n.kind.case !== undefined && n.kind.case !== "topicChanged";
}

/** The conversation after a notice newer than its latest message. */
export function applyNotice(c: Conversation, n: Notice, sender: Uint8Array, me: Uint8Array): Conversation {
  const k = n.kind;
  const count = (delta: number) => Math.max(0, c.memberCount + delta);
  switch (k.case) {
    case "groupCreated":
      return {
        ...c,
        topic: k.value.topic,
        memberCount: 1 + k.value.members.length,
        member: true,
        admin: sameId(sender, me),
      };
    case "membersAdded": {
      const mine = k.value.users.some((u) => sameId(u.userId, me));
      return {
        ...c,
        memberCount: count(k.value.users.length),
        ...(mine ? { member: true, admin: false } : {}),
      };
    }
    case "memberRemoved":
      return {
        ...c,
        memberCount: count(-1),
        ...(sameId(k.value.user?.userId, me) ? { member: false, admin: false } : {}),
      };
    case "memberLeft":
      return {
        ...c,
        memberCount: count(-1),
        ...(sameId(sender, me) ? { member: false, admin: false } : {}),
        ...(sameId(k.value.promoted?.userId, me) ? { admin: true } : {}),
      };
    case "topicChanged":
      return { ...c, topic: k.value.topic };
    case "adminChanged":
      return sameId(k.value.user?.userId, me) ? { ...c, admin: k.value.admin } : c;
    default:
      return c;
  }
}

/** The members cache after a notice: those who left or were removed are dropped, admin flags follow. */
export function membersAfter(members: Member[], n: Notice, sender: Uint8Array): Member[] {
  const k = n.kind;
  switch (k.case) {
    case "memberRemoved":
      return members.filter((m) => !sameId(m.user?.userId, k.value.user?.userId));
    case "memberLeft":
      return members
        .filter((m) => !sameId(m.user?.userId, sender))
        .map((m) => (sameId(m.user?.userId, k.value.promoted?.userId) ? { ...m, admin: true } : m));
    case "adminChanged":
      return members.map((m) => (sameId(m.user?.userId, k.value.user?.userId) ? { ...m, admin: k.value.admin } : m));
    default:
      return members;
  }
}

/** The members cache after a member's read position moved; positions never go backwards. */
export function withMemberRead(members: Member[], userId: Uint8Array, seq: bigint): Member[] {
  let changed = false;
  const next = members.map((m) => {
    if (!sameId(m.user?.userId, userId) || m.readSeq >= seq) return m;
    changed = true;
    return { ...m, readSeq: seq };
  });
  return changed ? next : members;
}

/** How many members other than the user have read up to the seq. */
export function readBy(members: Member[] | undefined, me: Uint8Array | undefined, seq: bigint): number {
  return (members ?? []).filter((m) => !sameId(m.user?.userId, me) && m.readSeq >= seq).length;
}

/** Splits a comma-separated list of names, dropping empty entries. */
export function parseNames(input: string): string[] {
  return input
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

/** What is wrong with a list of names, or null. */
export function namesProblem(names: string[]): string | null {
  if (names.length >= MAX_MEMBERS) return `A group has at most ${MAX_MEMBERS} members.`;
  for (const n of names) {
    if (nameProblem(n)) return `“${n}” is not a valid name.`;
  }
  return null;
}

const bytes = (s: string) => new TextEncoder().encode(s).length;

/** What is wrong with a topic, or null. */
export function topicProblem(topic: string): string | null {
  if (topic === "") return "Enter a topic.";
  if (bytes(topic) > 200) return "Topics are at most 200 bytes.";
  if (/^\p{White_Space}|\p{White_Space}$/u.test(topic)) return "Topics cannot start or end with a space.";
  if (/\p{Cc}/u.test(topic)) return "Topics cannot contain control characters.";
  return null;
}
