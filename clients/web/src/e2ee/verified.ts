// Contacts the user marked as verified, with the fingerprint they had then,
// remembered in this browser per user.

import { storage } from "../storage";

/** Fingerprints in hex, by the contact's hex id. */
export type Verified = Record<string, string>;

const name = (me: string) => `verified.${me}`;

export function loadVerified(me: string): Verified {
  try {
    const v: unknown = JSON.parse(storage.get(name(me)) ?? "{}");
    return typeof v === "object" && v !== null ? (v as Verified) : {};
  } catch {
    return {};
  }
}

export function saveVerified(me: string, verified: Verified): void {
  storage.set(name(me), JSON.stringify(verified));
}

/** Whether a verified contact's devices changed since: their fingerprint differs from the one verified. */
export function changedSinceVerified(verified: Verified, fingerprints: Record<string, string>, user: string): boolean {
  const v = verified[user];
  const now = fingerprints[user];
  return !!v && !!now && v !== now;
}
