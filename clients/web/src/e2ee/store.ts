// Where a device keeps its keys: its key pair, with a private key that
// cannot be exported, and the conversations' epoch keys, in IndexedDB, one
// database per signed-in device, deleted when it signs out.
//
// The device's private key is a non-extractable CryptoKey: a script on the
// page can use it but not read it. The epoch keys are extractable, as they
// are wrapped for the user's new devices, and the device token is in
// localStorage; any script running on the origin can read both. The keys
// are safe from the backend and the network, not from the page's own code.

import type { DeviceKeyPair } from "./crypto";

export interface StoredEpoch {
  /** Hex. */
  conversationId: string;
  epoch: number;
  key: CryptoKey;
}

export interface KeyStore {
  deviceKey(): Promise<DeviceKeyPair | undefined>;
  setDeviceKey(pair: DeviceKeyPair): Promise<void>;
  epochs(): Promise<StoredEpoch[]>;
  putEpoch(e: StoredEpoch): Promise<void>;
  /** The user's devices this device has seen with keys, decimal ids; undefined before the first look. */
  knownDevices(): Promise<string[] | undefined>;
  setKnownDevices(ids: string[]): Promise<void>;
  close(): void;
}

const DEVICE = "device";
const KNOWN = "known";
const EPOCH = "epoch/";
const epochId = (e: { conversationId: string; epoch: number }) => `${EPOCH}${e.conversationId}/${e.epoch}`;

/** A store in memory, for tests and where IndexedDB is missing. */
export function memoryStore(): KeyStore {
  const items = new Map<string, unknown>();
  return {
    deviceKey: () => Promise.resolve(items.get(DEVICE) as DeviceKeyPair | undefined),
    setDeviceKey: (pair) => Promise.resolve(void items.set(DEVICE, pair)),
    epochs: () => Promise.resolve([...items].filter(([k]) => k.startsWith(EPOCH)).map(([, v]) => v as StoredEpoch)),
    putEpoch: (e) => Promise.resolve(void items.set(epochId(e), e)),
    knownDevices: () => Promise.resolve(items.get(KNOWN) as string[] | undefined),
    setKnownDevices: (ids) => Promise.resolve(void items.set(KNOWN, ids)),
    close: () => {},
  };
}

const ITEMS = "items";

const request = <T>(r: IDBRequest<T>): Promise<T> =>
  new Promise((resolve, reject) => {
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error ?? new Error("IndexedDB request failed"));
  });

/** The device's database name. */
export const storeName = (userId: string, deviceId: string): string => `wrinkle-keys-${userId}-${deviceId}`;

/** The device's store in IndexedDB, or in memory when the browser has none. */
export async function openStore(name: string): Promise<KeyStore> {
  if (typeof indexedDB === "undefined") return memoryStore();
  const open = indexedDB.open(name, 1);
  open.onupgradeneeded = () => open.result.createObjectStore(ITEMS);
  const db = await request(open);
  const get = async <T>(key: string) => {
    const tx = db.transaction(ITEMS, "readonly");
    return (await request(tx.objectStore(ITEMS).get(key))) as T | undefined;
  };
  const put = async (key: string, value: unknown) => {
    const tx = db.transaction(ITEMS, "readwrite");
    tx.objectStore(ITEMS).put(value, key);
    await new Promise<void>((resolve, reject) => {
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error ?? new Error("IndexedDB transaction failed"));
      tx.onabort = () => reject(tx.error ?? new Error("IndexedDB transaction aborted"));
    });
  };
  return {
    deviceKey: () => get<DeviceKeyPair>(DEVICE),
    setDeviceKey: (pair) => put(DEVICE, pair),
    async epochs() {
      const tx = db.transaction(ITEMS, "readonly");
      const range = IDBKeyRange.bound(EPOCH, EPOCH + "\uffff");
      return (await request(tx.objectStore(ITEMS).getAll(range))) as StoredEpoch[];
    },
    putEpoch: (e) => put(epochId(e), e),
    knownDevices: () => get<string[]>(KNOWN),
    setKnownDevices: (ids) => put(KNOWN, ids),
    close: () => db.close(),
  };
}

/** Deletes a signed-out device's keys. */
export function deleteStore(name: string): void {
  if (typeof indexedDB !== "undefined") indexedDB.deleteDatabase(name);
}
