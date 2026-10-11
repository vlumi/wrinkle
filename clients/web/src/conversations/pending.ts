// Messages sent but not yet confirmed, by conversation id, shown below the
// confirmed ones until the backend's copy arrives.

import { hex } from "../ids";
import { key } from "./model";

export interface Pending {
  clientMessageId: string;
  text: string;
  /** A sticker's id in hex, for a sticker message. */
  sticker?: string;
  failed?: boolean;
}

export type PendingByConversation = Record<string, Pending[]>;

/**
 * 128 random bits in hex, for client message ids. crypto.randomUUID exists
 * only on secure pages (HTTPS or localhost), and on a LAN the page is plain HTTP.
 */
export function newMessageId(): string {
  return hex(crypto.getRandomValues(new Uint8Array(16)));
}

export function addPending(pending: PendingByConversation, conversationId: string, p: Pending): PendingByConversation {
  return { ...pending, [conversationId]: [...(pending[conversationId] ?? []), p] };
}

/** Drops the pending copy of a confirmed message; unchanged if there is none. */
export function confirmPending(
  pending: PendingByConversation,
  conversationId: Uint8Array,
  clientMessageId: string,
): PendingByConversation {
  const id = key(conversationId);
  const list = pending[id];
  if (!list?.some((p) => p.clientMessageId === clientMessageId)) return pending;
  return { ...pending, [id]: list.filter((p) => p.clientMessageId !== clientMessageId) };
}

export function failPending(
  pending: PendingByConversation,
  conversationId: string,
  clientMessageId: string,
): PendingByConversation {
  const list = (pending[conversationId] ?? []).map((p) =>
    p.clientMessageId === clientMessageId ? { ...p, failed: true } : p,
  );
  return { ...pending, [conversationId]: list };
}
