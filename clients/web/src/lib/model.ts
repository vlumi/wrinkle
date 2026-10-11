// The device's view of its conversations, and how events change it. Pure
// functions over plain data, so they are tested without a connection.

import type { Event } from "../gen/wrinkle/v1/event_pb";
import type { Conversation, Message } from "../gen/wrinkle/v1/model_pb";

export interface Data {
  conversations: Record<string, Conversation>;
  /** Conversation id to messages by seq. */
  messages: Record<string, Record<string, Message>>;
}

export const key = (id: bigint): string => id.toString();

export function unread(c: Conversation): bigint {
  return c.lastSeq > c.readSeq ? c.lastSeq - c.readSeq : 0n;
}

export function upsertConversation(data: Data, c: Conversation): Data {
  const old = data.conversations[key(c.conversationId)];
  const merged: Conversation = old
    ? {
        ...c,
        lastSeq: c.lastSeq > old.lastSeq ? c.lastSeq : old.lastSeq,
        lastMessage: c.lastSeq >= old.lastSeq ? c.lastMessage : old.lastMessage,
        readSeq: c.readSeq > old.readSeq ? c.readSeq : old.readSeq,
        peerReadSeq: c.peerReadSeq > old.peerReadSeq ? c.peerReadSeq : old.peerReadSeq,
      }
    : c;
  return { ...data, conversations: { ...data.conversations, [key(c.conversationId)]: merged } };
}

export function addMessages(data: Data, conversationId: bigint, messages: Message[]): Data {
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
  const next: Conversation = {
    ...c,
    lastSeq: m.seq,
    lastMessage: m,
    readSeq: readByMe && m.seq > c.readSeq ? m.seq : c.readSeq,
  };
  return { ...data, conversations: { ...data.conversations, [key(c.conversationId)]: next } };
}

export interface Applied {
  data: Data;
  /** The event concerns a conversation this device does not know yet. */
  unknownConversation: boolean;
}

/** Applies one event from the user's log. */
export function applyEvent(data: Data, event: Event, me: bigint): Applied {
  switch (event.body.case) {
    case "message": {
      const m = event.body.value.message;
      if (!m) return { data, unknownConversation: false };
      let next = addMessages(data, m.conversationId, [m]);
      if (!next.conversations[key(m.conversationId)]) return { data: next, unknownConversation: true };
      next = noteLatest(next, m, m.senderUserId === me);
      return { data: next, unknownConversation: false };
    }
    case "read": {
      const r = event.body.value;
      const c = data.conversations[key(r.conversationId)];
      if (!c) return { data, unknownConversation: true };
      const next: Conversation =
        r.userId === me
          ? { ...c, readSeq: r.seq > c.readSeq ? r.seq : c.readSeq }
          : { ...c, peerReadSeq: r.seq > c.peerReadSeq ? r.seq : c.peerReadSeq };
      return { data: { ...data, conversations: { ...data.conversations, [key(c.conversationId)]: next } }, unknownConversation: false };
    }
    default:
      return { data, unknownConversation: false };
  }
}

/** Conversations with the most recently active first. */
export function byActivity(conversations: Record<string, Conversation>): Conversation[] {
  const time = (c: Conversation) => c.lastMessage?.sentAt?.seconds ?? 0n;
  return Object.values(conversations).sort((a, b) => {
    const d = time(b) - time(a);
    return d > 0n ? 1 : d < 0n ? -1 : 0;
  });
}

/** A conversation's messages in sequence order. */
export function inOrder(messages: Record<string, Message> | undefined): Message[] {
  return Object.values(messages ?? {}).sort((a, b) => (a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0));
}
