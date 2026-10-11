// The device's view of its conversations, and how events change it. Pure
// functions over plain data, so they are tested without a connection.

import type { Event } from "../gen/wrinkle/v1/event_pb";
import type { Conversation, Message, User } from "../gen/wrinkle/v1/model_pb";
import { hex, sameId } from "../ids";

export interface Data {
  conversations: Record<string, Conversation>;
  /** Conversation id to messages by seq. */
  messages: Record<string, Record<string, Message>>;
}

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

export function upsertConversation(data: Data, c: Conversation): Data {
  const old = data.conversations[key(c.conversationId)];
  if (!old) return withConversation(data, c);
  return withConversation(data, {
    ...c,
    lastSeq: max(c.lastSeq, old.lastSeq),
    lastMessage: c.lastSeq >= old.lastSeq ? c.lastMessage : old.lastMessage,
    readSeq: max(c.readSeq, old.readSeq),
    peerReadSeq: max(c.peerReadSeq, old.peerReadSeq),
  });
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
}

/** Applies one event from the user's log. */
export function applyEvent(data: Data, event: Event, me: Uint8Array): Applied {
  switch (event.body.case) {
    case "message": {
      const m = event.body.value.message;
      if (!m) return { data, unknownConversation: false };
      let next = addMessages(data, m.conversationId, [m]);
      if (!next.conversations[key(m.conversationId)]) return { data: next, unknownConversation: true };
      next = noteLatest(next, m, sameId(m.senderUserId, me));
      return { data: next, unknownConversation: false };
    }
    case "read": {
      const r = event.body.value;
      const c = data.conversations[key(r.conversationId)];
      if (!c) return { data, unknownConversation: true };
      const next: Conversation = sameId(r.userId, me)
        ? { ...c, readSeq: max(r.seq, c.readSeq) }
        : { ...c, peerReadSeq: max(r.seq, c.peerReadSeq) };
      return { data: withConversation(data, next), unknownConversation: false };
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
  let next = data;
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
