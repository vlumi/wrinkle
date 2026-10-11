// What the browser remembers between page loads: the signed-in device's
// credentials, resumed on load until signed out, and its cursor into the
// user's log.

import { storage } from "../storage";

export interface Credentials {
  /** Hex. */
  userId: string;
  /** Decimal; a uint64 does not fit a JSON number. */
  deviceId: string;
  deviceToken: string;
  /** The gateway signed in through. */
  url: string;
}

// Settings written for an earlier contract, with numeric ids and no tokens,
// are dropped; the gateways used before stay.
const VERSION = "2";
const KEEP = ["version", "gateways"];

const CREDENTIALS = "credentials";
const CURSOR = "cursor";

if (storage.get("version") !== VERSION) {
  storage.clear(KEEP);
  storage.set("version", VERSION);
}

function isCredentials(c: unknown): c is Credentials {
  if (typeof c !== "object" || c === null) return false;
  const r = c as Record<string, unknown>;
  return ["userId", "deviceId", "deviceToken", "url"].every((k) => typeof r[k] === "string");
}

export const saved = {
  cursor: (): bigint => BigInt(storage.get(CURSOR) ?? "0"),
  setCursor: (cursor: bigint): void => storage.set(CURSOR, cursor.toString()),

  credentials(): Credentials | null {
    try {
      const c: unknown = JSON.parse(storage.get(CREDENTIALS) ?? "null");
      return isCredentials(c) ? c : null;
    } catch {
      return null;
    }
  },
  setCredentials(c: Credentials): void {
    storage.set(CREDENTIALS, JSON.stringify(c));
    storage.remove(CURSOR);
  },
  forget(): void {
    storage.remove(CREDENTIALS);
    storage.remove(CURSOR);
  },
};
