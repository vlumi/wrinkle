// The device's view of its conversations, and how events change it. Pure
// functions over plain data, so they are tested without a connection.

import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { attachmentsText } from "../attachments/model";
import type { Attachments } from "../gen/wrinkle/v1/content_pb";
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
import { withListed, withoutMessage, withReaction } from "../reactions/model";
import type { Reactions } from "../reactions/model";

export interface Data {
  conversations: Record<string, Conversation>;
  /** Conversation id to messages by seq. */
  messages: Record<string, Record<string, Message>>;
  /** Groups' current members, by conversation id, once listed. */
  members: Record<string, Member[]>;
  users: Users;
  /** Sealed messages' texts as this device opened them, by textKey; null for those it cannot open. */
  texts: Texts;
  /** Opened messages that are stickers, by textKey: the sticker's id in hex. Their text is STICKER. */
  stickers: Stickers;
  /** Opened messages with attachments, by textKey. Their text is the caption, or what they hold. */
  attachments: AttachmentsByMessage;
  reactions: Reactions;
}

export type Texts = Record<string, string | null>;
export type Stickers = Record<string, string>;
export type AttachmentsByMessage = Record<string, Attachments>;

export const emptyData = (): Data => ({
  conversations: {},
  messages: {},
  members: {},
  users: {},
  texts: {},
  stickers: {},
  attachments: {},
  reactions: {},
});

/** The key of a message's text: its conversation and seq. */
export const textKey = (m: { conversationId: Uint8Array; seq: bigint }): string => `${hex(m.conversationId)}/${m.seq}`;

/** What a message this device cannot open shows in its place. */
export const UNDECRYPTABLE = "Can't decrypt this message";

/** A message's text: empty for a notice, a deleted message or one still being opened. */
export function textOf(m: Message, texts: Texts): string {
  if (m.content.case !== "sealed" || isDeleted(m)) return "";
  const t = texts[textKey(m)];
  return t === undefined ? "" : (t ?? UNDECRYPTABLE);
}

/** Whether the message is sealed and this device could not open it. */
export const isUndecryptable = (m: Message, texts: Texts): boolean =>
  m.content.case === "sealed" && !isDeleted(m) && texts[textKey(m)] === null;

/** Records opened texts. */
export function withTexts(data: Data, texts: Texts): Data {
  return Object.keys(texts).length === 0 ? data : { ...data, texts: { ...data.texts, ...texts } };
}

/** What a sticker message reads as, as in the list's preview. */
export const STICKER = "Sticker";

/** The id in hex of the sticker a message shows; undefined for anything else. */
export function stickerOf(m: Message, stickers: Stickers): string | undefined {
  if (m.content.case !== "sealed" || isDeleted(m)) return undefined;
  return stickers[textKey(m)];
}

/** Records opened stickers, by textKey, with their text. */
export function withStickers(data: Data, stickers: Stickers): Data {
  if (Object.keys(stickers).length === 0) return data;
  const texts = Object.fromEntries(Object.keys(stickers).map((k) => [k, STICKER]));
  return { ...withTexts(data, texts), stickers: { ...data.stickers, ...stickers } };
}

/** The attachments a message shows; undefined for a message without them. */
export function attachmentsOf(m: Message, attachments: AttachmentsByMessage): Attachments | undefined {
  if (m.content.case !== "sealed" || isDeleted(m)) return undefined;
  return attachments[textKey(m)];
}

/** Records opened messages with attachments, by textKey, with their text. */
export function withAttachments(data: Data, attachments: AttachmentsByMessage): Data {
  if (Object.keys(attachments).length === 0) return data;
  const texts = Object.fromEntries(Object.entries(attachments).map(([k, a]) => [k, attachmentsText(a)]));
  return { ...withTexts(data, texts), attachments: { ...data.attachments, ...attachments } };
}

/** What a deleted message shows in its place. */
export const REMOVED = "Message removed";

export const isDeleted = (m: Message): boolean => m.deletedAt !== undefined;

/** The message as deleted: its content gone. */
const tombstone = (m: Message, deletedAt: Timestamp | undefined): Message => ({
  ...m,
  content: { case: undefined },
  blobIds: [],
  deletedAt: deletedAt ?? create(TimestampSchema),
});

/** Keeps a known deletion over a copy of the message from before it. */
const newer = (old: Message | undefined, m: Message): Message =>
  old && isDeleted(old) && !isDeleted(m) && old.seq === m.seq ? tombstone(m, old.deletedAt) : m;

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
  for (const m of messages) known[key(m.seq)] = newer(known[key(m.seq)], m);
  return { ...data, messages: { ...data.messages, [id]: known } };
}

/** Records a page of history: its messages and their reactions. */
export function addHistory(data: Data, conversationId: Uint8Array, messages: Message[]): Data {
  const next = addMessages(data, conversationId, messages);
  let reactions = next.reactions;
  for (const m of messages) {
    const k = textKey(m);
    reactions = isDeleted(next.messages[key(conversationId)][key(m.seq)])
      ? withoutMessage(reactions, k)
      : withListed(reactions, k, m.reactions);
  }
  return reactions === next.reactions ? next : { ...next, reactions };
}

/** Records a message as the conversation's latest if it is newer. */
export function noteLatest(data: Data, m: Message, readByMe: boolean): Data {
  const c = data.conversations[key(m.conversationId)];
  if (!c || m.seq < c.lastSeq) return data;
  return withConversation(data, {
    ...c,
    lastSeq: m.seq,
    lastMessage: c.lastMessage ? newer(c.lastMessage, m) : m,
    readSeq: readByMe ? max(m.seq, c.readSeq) : c.readSeq,
  });
}

/** Marks a message deleted, in the conversation and in the list's preview. */
export function withDeleted(data: Data, conversationId: Uint8Array, seq: bigint, deletedAt?: Timestamp): Data {
  const id = key(conversationId);
  const reactions = withoutMessage(data.reactions, textKey({ conversationId, seq }));
  let next = reactions === data.reactions ? data : { ...data, reactions };
  const m = data.messages[id]?.[key(seq)];
  if (m && !isDeleted(m)) {
    next = {
      ...next,
      messages: { ...next.messages, [id]: { ...next.messages[id], [key(seq)]: tombstone(m, deletedAt) } },
    };
  }
  const c = next.conversations[id];
  const last = c?.lastMessage;
  if (c && last && last.seq === seq && !isDeleted(last)) {
    next = withConversation(next, { ...c, lastMessage: tombstone(last, deletedAt) });
  }
  return next;
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
    case "messageDeleted": {
      const d = event.body.value;
      const known = !!data.conversations[key(d.conversationId)];
      return { data: withDeleted(data, d.conversationId, d.seq, d.deletedAt), unknownConversation: !known };
    }
    case "reaction": {
      const r = event.body.value;
      const reactions = withReaction(data.reactions, textKey(r), r.userId, r.sealed, r.updatedAt);
      return { data: reactions === data.reactions ? data : { ...data, reactions }, unknownConversation: false };
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
function withPeer(data: Data, user: User): Data {
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
export function preview(c: Conversation, me: Uint8Array | undefined, users: Users, texts: Texts): string {
  const m = c.lastMessage;
  if (!m) return "No messages yet";
  const notice = noticeOf(m);
  if (notice) return noticeText(notice, m.senderUserId, me, users);
  if (isDeleted(m)) return REMOVED;
  const text = textOf(m, texts);
  if (sameId(m.senderUserId, me)) return `You: ${text}`;
  const sender = isGroup(c) ? users[hex(m.senderUserId)] : undefined;
  return sender ? `${displayName(sender)}: ${text}` : text;
}
