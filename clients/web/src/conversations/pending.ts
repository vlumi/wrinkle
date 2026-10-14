// Messages sent but not yet confirmed, by conversation id, shown below the
// confirmed ones until the backend's copy arrives.

import { hex } from "../ids";
import { key } from "./model";

export interface Pending {
  clientMessageId: string;
  /** The text, or the caption of attachments. */
  text: string;
  /** A sticker's id in hex, for a sticker message. */
  sticker?: string;
  attachments?: PendingFile[];
  failed?: boolean;
  /** Why it failed, when sending again will not help. */
  failure?: string;
}

/** A file being sent. */
export interface PendingFile {
  name: string;
  /** An object URL of the chosen image, empty for other files. */
  preview: string;
  /** The share uploaded, 0 to 1. */
  progress: number;
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

function changed(
  pending: PendingByConversation,
  conversationId: string,
  clientMessageId: string,
  change: (p: Pending) => Pending,
): PendingByConversation {
  const list = pending[conversationId];
  if (!list?.some((p) => p.clientMessageId === clientMessageId)) return pending;
  return { ...pending, [conversationId]: list.map((p) => (p.clientMessageId === clientMessageId ? change(p) : p)) };
}

export function failPending(
  pending: PendingByConversation,
  conversationId: string,
  clientMessageId: string,
  failure?: string,
): PendingByConversation {
  return changed(pending, conversationId, clientMessageId, (p) => ({
    ...p,
    failed: true,
    ...(failure ? { failure } : {}),
  }));
}

/** Shows a failed message as sending again. */
export function retryPending(
  pending: PendingByConversation,
  conversationId: string,
  clientMessageId: string,
): PendingByConversation {
  return changed(pending, conversationId, clientMessageId, ({ failure: _, ...p }) => ({ ...p, failed: false }));
}

/** Records the share of a pending message's file uploaded. */
export function withProgress(
  pending: PendingByConversation,
  conversationId: string,
  clientMessageId: string,
  index: number,
  progress: number,
): PendingByConversation {
  return changed(pending, conversationId, clientMessageId, (p) => ({
    ...p,
    attachments: p.attachments?.map((f, i) => (i === index ? { ...f, progress } : f)),
  }));
}
