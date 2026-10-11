// The device's view of its conversations, and how events change it. Pure
// functions over plain data, so they are tested without a connection.

import type { Event } from "../gen/wrinkle/v1/event_pb";
import type { Conversation, Member, Message, User } from "../gen/wrinkle/v1/model_pb";
import { displayName } from "../account/icons";
import {
  applyNotice,
  changesMembers,
  isGroup,
  membersAfter,
  noticeOf,
  noticeText,
  noticeUsers,
  withMemberRead,
} from "../groups/model";
import type { Users } from "../groups/model";
import { hex, sameId } from "../ids";

export interface Data {
  conversations: Record<string, Conversation>;
  /** Conversation id to messages by seq. */
  messages: Record<string, Record<string, Message>>;
  /** Groups' current members, by conversation id, once listed. */
  members: Record<string, Member[]>;
  users: Users;
}

export const emptyData = (): Data => ({ conversations: {}, messages: {}, members: {}, users: {} });

/** A message's text; empty for a notice. */
export const textOf = (m: Message): string => (m.content.case === "text" ? m.content.value : "");

/** The key of a conversation, or of a message by its seq. */
export const key = (id: Uint8Array | bigint): string => (typeof id === "bigint" ? id.toString() : hex(id));

const max = (a: bigint, b: bigint): bigint => (a > b ? a : b);
const compare = (a: bigint, b: bigint): number => (a < b ? -1 : a > b ? 1 : 0);

function withConversation(data: Data, c: Conversation): Data {
  return { ...data, conversations: { ...data.conversations, [key(c.conversationId)]: c } };
}

export function unread(c: Conversation): bigint {
  return c.lastSeq > c.readSeq ? c.lastSeq - c.readSeq : 0n;
}

/** Remembers users by id; the newest copy wins. */
export function withUsers(data: Data, users: (User | undefined)[]): Data {
  const known = users.filter((u): u is User => !!u && u.userId.length > 0);
  if (known.length === 0) return data;
  const next = { ...data.users };
  for (const u of known) next[hex(u.userId)] = u;
  return { ...data, users: next };
}

export function upsertConversation(data: Data, c: Conversation): Data {
  const old = data.conversations[key(c.conversationId)];
  const notice = noticeOf(c.lastMessage);
  const next = withUsers(data, [c.peer, ...(notice ? noticeUsers(notice) : [])]);
  if (!old) return withConversation(next, c);
  // The group's state goes with the latest message it reflects.
  const group = c.lastSeq >= old.lastSeq ? c : old;
  return withConversation(next, {
    ...c,
    lastSeq: max(c.lastSeq, old.lastSeq),
    lastMessage: group.lastMessage,
    readSeq: max(c.readSeq, old.readSeq),
    peerReadSeq: max(c.peerReadSeq, old.peerReadSeq),
    topic: group.topic,
    memberCount: group.memberCount,
    member: group.member,
    admin: group.admin,
  });
}

/** Records a group's current members, as listed by the backend. */
export function withMembers(data: Data, conversationId: Uint8Array, members: Member[]): Data {
  const id = key(conversationId);
  const next = withUsers(
    data,
    members.map((m) => m.user),
  );
  const c = next.conversations[id];
  const conversations = c ? { ...next.conversations, [id]: { ...c, memberCount: members.length } } : next.conversations;
  return { ...next, conversations, members: { ...next.members, [id]: members } };
}

export function addMessages(data: Data, conversationId: Uint8Array, messages: Message[]): Data {
  if (messages.length === 0) return data;
  const id = key(conversationId);
  const known = { ...(data.messages[id] ?? {}) };
  for (const m of messages) known[key(m.seq)] = m;
  return { ...data, messages: { ...data.messages, [id]: known } };
}

/** Records a message as the conversation's latest if it is newer. */
export function noteLatest(data: Data, m: Message, readByMe: boolean): Data {
  const c = data.conversations[key(m.conversationId)];
  if (!c || m.seq < c.lastSeq) return data;
  return withConversation(data, {
    ...c,
    lastSeq: m.seq,
    lastMessage: m,
    readSeq: readByMe ? max(m.seq, c.readSeq) : c.readSeq,
  });
}

export interface Applied {
  data: Data;
  /** The event concerns a conversation this device does not know yet. */
  unknownConversation: boolean;
  /** The event changed a group's members: the cache needs listing again. */
  membersChanged?: boolean;
}

/** Applies one event from the user's log. */
export function applyEvent(data: Data, event: Event, me: Uint8Array): Applied {
  switch (event.body.case) {
    case "message": {
      const m = event.body.value.message;
      if (!m) return { data, unknownConversation: false };
      const id = key(m.conversationId);
      const notice = noticeOf(m);
      let next = addMessages(data, m.conversationId, [m]);
      if (notice) next = withUsers(next, noticeUsers(notice));
      const c = next.conversations[id];
      if (!c) return { data: next, unknownConversation: true };
      let membersChanged = false;
      if (notice && m.seq > c.lastSeq) {
        next = withConversation(next, applyNotice(c, notice, m.senderUserId, me));
        const members = next.members[id];
        if (members)
          next = { ...next, members: { ...next.members, [id]: membersAfter(members, notice, m.senderUserId) } };
        membersChanged = changesMembers(notice);
      }
      next = noteLatest(next, m, sameId(m.senderUserId, me));
      return { data: next, unknownConversation: false, membersChanged };
    }
    case "read": {
      const r = event.body.value;
      const id = key(r.conversationId);
      const c = data.conversations[id];
      if (!c) return { data, unknownConversation: true };
      if (sameId(r.userId, me)) {
        return { data: withConversation(data, { ...c, readSeq: max(r.seq, c.readSeq) }), unknownConversation: false };
      }
      if (isGroup(c)) {
        const members = data.members[id];
        if (!members) return { data, unknownConversation: false };
        const next = withMemberRead(members, r.userId, r.seq);
        if (next === members) return { data, unknownConversation: false };
        return { data: { ...data, members: { ...data.members, [id]: next } }, unknownConversation: false };
      }
      return {
        data: withConversation(data, { ...c, peerReadSeq: max(r.seq, c.peerReadSeq) }),
        unknownConversation: false,
      };
    }
    case "profile": {
      const u = event.body.value.user;
      return { data: u ? withPeer(data, u) : data, unknownConversation: false };
    }
    default:
      return { data, unknownConversation: false };
  }
}

/** Updates the user's name and icon in the conversations with them. */
export function withPeer(data: Data, user: User): Data {
  let next = data.users[hex(user.userId)] ? withUsers(data, [user]) : data;
  for (const c of Object.values(data.conversations)) {
    if (sameId(c.peer?.userId, user.userId)) next = withConversation(next, { ...c, peer: user });
  }
  return next;
}

/** Conversations with the most recently active first. */
export function byActivity(conversations: Record<string, Conversation>): Conversation[] {
  const time = (c: Conversation) => c.lastMessage?.sentAt?.seconds ?? 0n;
  return Object.values(conversations).sort((a, b) => compare(time(b), time(a)));
}

/** A conversation's messages in sequence order. */
export function inOrder(messages: Record<string, Message> | undefined): Message[] {
  return Object.values(messages ?? {}).sort((a, b) => compare(a.seq, b.seq));
}

/** The list's line under a conversation: its latest message, with who sent it. */
export function preview(c: Conversation, me: Uint8Array | undefined, users: Users): string {
  const m = c.lastMessage;
  if (!m) return "No messages yet";
  const notice = noticeOf(m);
  if (notice) return noticeText(notice, m.senderUserId, me, users);
  const text = textOf(m);
  if (sameId(m.senderUserId, me)) return `You: ${text}`;
  const sender = isGroup(c) ? users[hex(m.senderUserId)] : undefined;
  return sender ? `${displayName(sender)}: ${text}` : text;
}
