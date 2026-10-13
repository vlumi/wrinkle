// The device's end-to-end keys in the session: started once signed in, the
// fingerprints of the users looked up, and marking a contact verified.

import { fromHex, hex } from "../ids";
import type { Api } from "../session/api";
import type { Credentials } from "../session/saved";
import type { Session } from "../session/session";
import { available } from "./crypto";
import { Keys } from "./keys";
import type { Hooks } from "./keys";
import { openStore, storeName } from "./store";
import { loadVerified, saveVerified } from "./verified";

export const NO_CRYPTO = "End-to-end encryption needs a secure page: open the client over HTTPS.";
const KEY_LOST = "This device lost its encryption keys. Sign in again.";

export function keyActions({ set, get, link, fail, signedOut }: Api, members: Hooks["members"]) {
  function directoryChanged(userIds: string[]) {
    const k = link.keys;
    if (!k) return;
    for (const u of userIds) {
      k.fingerprint(fromHex(u))
        .then((fp) => {
          if (link.keys === k) set({ fingerprints: { ...get().fingerprints, [u]: hex(fp) } });
        })
        .catch(fail);
    }
  }

  /** Starts the device's keys for the signed-in device; false if it signed out for having lost them. */
  async function startKeys(credentials: Credentials): Promise<boolean> {
    if (link.keys?.ready) return true;
    if (!available()) {
      set({ error: NO_CRYPTO });
      return true;
    }
    if (!link.keys) {
      const self = { userId: fromHex(credentials.userId), deviceId: BigInt(credentials.deviceId) };
      link.keyStore = await openStore(storeName(credentials.userId, credentials.deviceId));
      link.keys = new Keys(link.call, self, link.keyStore, { members, directoryChanged });
    }
    const k = link.keys;
    link.keysReady = k.start();
    let ok;
    try {
      ok = await link.keysReady;
    } catch (err) {
      link.keysReady = Promise.resolve(false);
      fail(err);
      return true;
    }
    if (!ok) {
      // The backend has another key for this device: it signs in again as a new one.
      link.leaving = { notice: KEY_LOST };
      await link.conn?.call("signOut", { case: "signOut", value: {} }).catch(() => {});
      signedOut(KEY_LOST);
      return false;
    }
    set({ verified: loadVerified(credentials.userId) });
    // Verified contacts' devices, so a change since shows.
    const verified = Object.keys(get().verified).map(fromHex);
    if (verified.length > 0) k.devicesOf(verified, true).catch(fail);
    return true;
  }

  const actions = {
    async lookUpDevices(userIds) {
      await link.keys?.devicesOf(userIds);
    },

    async safetyNumber(userId) {
      if (!link.keys?.ready) throw new Error(NO_CRYPTO);
      return link.keys.safetyNumber(userId);
    },

    async markVerified(userId) {
      const me = get().credentials?.userId;
      if (!link.keys?.ready || !me) throw new Error(NO_CRYPTO);
      const fp = hex(await link.keys.fingerprint(userId, true));
      const u = hex(userId);
      const verified = { ...get().verified, [u]: fp };
      saveVerified(me, verified);
      set({ verified, fingerprints: { ...get().fingerprints, [u]: fp } });
    },
  } satisfies Partial<Session>;

  return { actions, startKeys };
}
