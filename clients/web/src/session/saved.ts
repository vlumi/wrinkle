// What the browser remembers between page loads: per user, the device id the
// backend assigned and the cursor into the user's log; and the last session,
// resumed on load until signed out.

import { storage } from "../storage";

export interface LastSession {
  name: string;
  url: string;
}

const LAST = "last";

function loadBigInt(key: string): bigint {
  return BigInt(storage.get(key) ?? "0");
}

export const saved = {
  deviceId: (user: string): bigint => loadBigInt(`${user}.device`),
  setDeviceId: (user: string, id: bigint): void => storage.set(`${user}.device`, id.toString()),
  cursor: (user: string): bigint => loadBigInt(`${user}.cursor`),
  setCursor: (user: string, cursor: bigint): void => storage.set(`${user}.cursor`, cursor.toString()),

  lastSession(): LastSession | null {
    try {
      return JSON.parse(storage.get(LAST) ?? "null");
    } catch {
      return null;
    }
  },
  setLastSession: (last: LastSession): void => storage.set(LAST, JSON.stringify(last)),
  forgetLastSession: (): void => storage.remove(LAST),
};
