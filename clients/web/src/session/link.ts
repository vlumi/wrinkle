// What the signed-in device holds outside React state: its connection, its
// end-to-end keys and its cursor into the user's log, with the sign-in or
// sign-out in flight. Dropped on sign-out; the screens read the store.

import type { Connection } from "../connection/connection";
import type { Call, Keys } from "../e2ee/keys";
import type { KeyStore } from "../e2ee/store";
import { saved } from "./saved";
import type { AuthMode } from "./session";

/** A register or sign-in waiting for its connection to open. */
export interface PendingAuth {
  mode: AuthMode;
  name: string;
  password: string;
  resolve(): void;
  reject(err: Error): void;
}

export class Link {
  conn: Connection | null = null;
  cursor = 0n;
  pendingAuth: PendingAuth | null = null;
  /** A sign-out this device asked for, with the notice to show when the server closes the connection for it. */
  leaving: { notice: string | null } | null = null;
  /** The device's end-to-end keys, once signed in, and whether they started. */
  keys: Keys | null = null;
  keyStore: KeyStore | null = null;
  keysReady: Promise<boolean> = Promise.resolve(false);

  /** Sends a request over the connection; rejects when there is none. */
  readonly call: Call = (expected, body) => {
    if (!this.conn) return Promise.reject(new Error("not connected"));
    return this.conn.call(expected, body);
  };

  /** Moves the cursor, remembered across page loads. */
  saveCursor(next: bigint): void {
    this.cursor = next;
    saved.setCursor(next);
  }

  /** Drops the connection and the keys, closing the key store. */
  reset(): void {
    this.pendingAuth = null;
    this.leaving = null;
    this.conn?.close();
    this.conn = null;
    this.keyStore?.close();
    this.keys = null;
    this.keyStore = null;
    this.keysReady = Promise.resolve(false);
  }
}
