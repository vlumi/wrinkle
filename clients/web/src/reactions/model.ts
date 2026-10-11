// Reactions as the device keeps them: each user's sealed set per message,
// with the emoji once opened, and the chips counted from them. The servers
// cannot see the emoji, so all counting happens here.

import type { Timestamp } from "@bufbuild/protobuf/wkt";
import type { Reaction, Sealed } from "../gen/wrinkle/v1/model_pb";
import { hex, sameId } from "../ids";

export const MAX_OWN = 3;
const MAX_EMOJI_BYTES = 32;
export const QUICK = ["👍", "❤️", "😂", "😮", "😢", "🙏"];

/** One user's reactions to a message. */
export interface Entry {
  sealed: Sealed;
  updatedAt?: Timestamp;
  /** As opened: undefined until then, null if this device cannot open them. */
  emoji?: string[] | null;
}

/** By message (textKey), then by the user's hex id. */
export type Reactions = Record<string, Record<string, Entry>>;

const encoder = new TextEncoder();

const graphemes = (s: string): string[] =>
  [...new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(s)].map((g) => g.segment);

/** Whether the text is one emoji: one grapheme cluster of at most 32 bytes, pictographic. */
export function isEmoji(s: string): boolean {
  return (
    s.length > 0 &&
    encoder.encode(s).length <= MAX_EMOJI_BYTES &&
    graphemes(s).length === 1 &&
    /\p{Extended_Pictographic}|\p{Regional_Indicator}|⃣/u.test(s)
  );
}

/** A received set as shown: its valid, distinct emoji, at most three. */
export function cleaned(emoji: string[]): string[] {
  const one = (e: string) => encoder.encode(e).length <= MAX_EMOJI_BYTES && graphemes(e).length === 1;
  return [...new Set(emoji.filter(one))].slice(0, MAX_OWN);
}

const sameBytes = (a: Uint8Array, b: Uint8Array) => a.length === b.length && sameId(a, b);
const sameSealed = (a: Sealed, b: Sealed) =>
  a.epoch === b.epoch && sameBytes(a.nonce, b.nonce) && sameBytes(a.ciphertext, b.ciphertext);

function entry(old: Entry | undefined, sealed: Sealed, updatedAt: Timestamp | undefined, emoji?: string[]): Entry {
  // The same sealed set keeps what opening it found.
  const known = emoji ?? (old && sameSealed(old.sealed, sealed) ? old.emoji : undefined);
  return { sealed, updatedAt, emoji: known };
}

/** Sets or, without sealed, removes a user's reactions to a message. */
export function withReaction(
  reactions: Reactions,
  messageKey: string,
  userId: Uint8Array,
  sealed: Sealed | undefined,
  updatedAt: Timestamp | undefined,
  emoji?: string[],
): Reactions {
  const u = hex(userId);
  const users = { ...(reactions[messageKey] ?? {}) };
  if (sealed) users[u] = entry(users[u], sealed, updatedAt, emoji);
  else if (users[u]) delete users[u];
  else return reactions;
  const next = { ...reactions, [messageKey]: users };
  if (Object.keys(users).length === 0) delete next[messageKey];
  return next;
}

/** Replaces a message's reactions with those listed by the backend. */
export function withListed(reactions: Reactions, messageKey: string, listed: Reaction[]): Reactions {
  const old = reactions[messageKey] ?? {};
  const users: Record<string, Entry> = {};
  for (const r of listed) {
    if (!r.sealed) continue;
    users[hex(r.userId)] = entry(old[hex(r.userId)], r.sealed, r.updatedAt);
  }
  if (Object.keys(users).length === 0 && !reactions[messageKey]) return reactions;
  const next = { ...reactions, [messageKey]: users };
  if (Object.keys(users).length === 0) delete next[messageKey];
  return next;
}

/** Forgets a message's reactions, as when it is deleted. */
export function withoutMessage(reactions: Reactions, messageKey: string): Reactions {
  if (!reactions[messageKey]) return reactions;
  const next = { ...reactions };
  delete next[messageKey];
  return next;
}

/** Records the emoji found by opening a set, if it is still the user's current one. */
export function withOpened(
  reactions: Reactions,
  messageKey: string,
  user: string,
  sealed: Sealed,
  emoji: string[] | null,
): Reactions {
  const e = reactions[messageKey]?.[user];
  if (!e || !sameSealed(e.sealed, sealed)) return reactions;
  return { ...reactions, [messageKey]: { ...reactions[messageKey], [user]: { ...e, emoji } } };
}

/** One emoji under a message: how many reacted with it, and who. */
export interface Chip {
  emoji: string;
  /** Hex ids, in the order they reacted. */
  users: string[];
  mine: boolean;
}

const seconds = (e: Entry) => Number(e.updatedAt?.seconds ?? 0n) + (e.updatedAt?.nanos ?? 0) / 1e9;

/** The message's chips, in the order their emoji were first used; sets not opened are left out. */
export function chips(users: Record<string, Entry> | undefined, me: Uint8Array | undefined): Chip[] {
  const mine = me ? hex(me) : "";
  const ordered = Object.entries(users ?? {}).sort(([a, x], [b, y]) => seconds(x) - seconds(y) || (a < b ? -1 : 1));
  const out = new Map<string, Chip>();
  for (const [u, e] of ordered) {
    for (const emoji of e.emoji ?? []) {
      let c = out.get(emoji);
      if (!c) out.set(emoji, (c = { emoji, users: [], mine: false }));
      c.users.push(u);
      if (u === mine) c.mine = true;
    }
  }
  return [...out.values()];
}

/** The user's own emoji on a message, as far as opened. */
export const ownEmoji = (users: Record<string, Entry> | undefined, me: Uint8Array | undefined): string[] =>
  (me && users?.[hex(me)]?.emoji) || [];

/** The user's set after tapping the emoji: removed if there, else added; null when already at the limit. */
export function toggled(own: string[], emoji: string): string[] | null {
  if (own.includes(emoji)) return own.filter((e) => e !== emoji);
  return own.length >= MAX_OWN ? null : [...own, emoji];
}
