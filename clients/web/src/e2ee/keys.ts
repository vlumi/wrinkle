// A device's end-to-end keys at work: its key pair, the directory of other
// devices' public keys, each conversation's epoch keys, and when a
// conversation needs a new epoch. Sending messages and reactions seals
// through it and receiving opens through it.

import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import type { MessageInitShape } from "@bufbuild/protobuf";
import { BackendError } from "../connection/connection";
import type { Connection } from "../connection/connection";
import { ContentSchema, ReactionsSchema } from "../gen/wrinkle/v1/content_pb";
import type { Content } from "../gen/wrinkle/v1/content_pb";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import type { ReceivedKey } from "../gen/wrinkle/v1/messaging_pb";
import type { Device, Message, Sealed, SealedSchema } from "../gen/wrinkle/v1/model_pb";
import { fromHex, hex, sameId } from "../ids";
import * as crypto from "./crypto";
import type { DeviceRef } from "./crypto";
import type { KeyStore } from "./store";

export type Call = Connection["call"];
export type SealedInit = MessageInitShape<typeof SealedSchema>;
/** What a message says: a text or a sticker. */
export type Body = Exclude<Content["body"], { case: undefined }>;
export type BodyInit = Exclude<MessageInitShape<typeof ContentSchema>["body"], { case: undefined } | undefined>;

const MAX_KEYS = 500;
const MAX_USERS = 256;
const ATTEMPTS = 4;

export interface Hooks {
  /** The conversation's current members; fresh asks the backend instead of a cache. */
  members(conversationId: Uint8Array, fresh: boolean): Promise<Uint8Array[]>;
  /** These users' devices (hex ids) changed in the directory. */
  directoryChanged(userIds: string[]): void;
}

/** A conversation's key state as the backend last told it. */
export interface KeyState {
  currentEpoch: number;
  rotate: boolean;
  /** The devices the current epoch's key is wrapped for, by deviceId. */
  recipients: Set<string>;
}

/** A device as a set member: its user's hex id and its id. */
export const deviceId = (userId: Uint8Array | string, id: bigint): string =>
  `${typeof userId === "string" ? userId : hex(userId)}:${id}`;

const sameSet = (a: Set<string>, b: Set<string>): boolean => a.size === b.size && [...a].every((x) => b.has(x));

/** Whether sending needs a new epoch first: none yet, the membership changed, or the member devices did. */
export function needsEpoch(state: KeyState, devices: Set<string>, haveKey: boolean): boolean {
  return state.currentEpoch === 0 || state.rotate || !haveKey || !sameSet(state.recipients, devices);
}

type Devices = Map<bigint, Uint8Array>;

function sameDevices(a: Devices, b: Devices): boolean {
  return a.size === b.size && [...a].every(([id, k]) => sameId(b.get(id), k));
}

interface Recipient {
  ref: DeviceRef;
  publicKey: Uint8Array;
}

export class Keys {
  private pair: crypto.DeviceKeyPair | null = null;
  /** The user's own devices this device has seen with keys. */
  private known = new Set<string>();
  /** Signed-in devices with keys by user, for the users asked about. */
  private directory = new Map<string, Devices>();
  /** Every device key seen, by deviceId; a device's key never changes. */
  private seen = new Map<string, Uint8Array>();
  private epochs = new Map<string, Map<number, CryptoKey>>();
  private states = new Map<string, KeyState>();
  /** Epochs, conversation/epoch, whose key the last look did not bring. */
  private missing = new Set<string>();
  private locks = new Map<string, Promise<unknown>>();

  constructor(
    private readonly call: Call,
    readonly self: DeviceRef,
    private readonly store: KeyStore,
    private readonly hooks: Hooks,
  ) {}

  get ready(): boolean {
    return this.pair !== null;
  }

  /** Loads the stored keys and publishes this device's key; false if the backend has another one for it. */
  async start(): Promise<boolean> {
    for (const e of await this.store.epochs()) this.keep(e.conversationId, e.epoch, e.key);
    let pair = await this.store.deviceKey();
    if (!pair) {
      pair = await crypto.generateDeviceKey();
      await this.store.setDeviceKey(pair);
    }
    try {
      await this.call("setDeviceKey", { case: "setDeviceKey", value: { publicKey: pair.publicKey } });
    } catch (err) {
      if (err instanceof BackendError && err.code === ErrorCode.FAILED_PRECONDITION) return false;
      throw err;
    }
    this.pair = pair;
    this.seen.set(deviceId(this.self.userId, this.self.deviceId), pair.publicKey);
    const known = await this.store.knownDevices();
    if (known) {
      this.known = new Set(known);
    } else {
      // The devices from before this one need nothing from it.
      const mine = (await this.devicesOf([this.self.userId], true)).get(hex(this.self.userId));
      this.known = new Set([this.self.deviceId, ...(mine?.keys() ?? [])].map(String));
      await this.store.setKnownDevices([...this.known]);
    }
    return true;
  }

  /** The users' signed-in devices with keys, from the directory or, if not there or fresh, the backend. */
  async devicesOf(userIds: Uint8Array[], fresh = false): Promise<Map<string, Devices>> {
    const wanted = [...new Map(userIds.map((u) => [hex(u), u])).values()];
    const ask = fresh ? wanted : wanted.filter((u) => !this.directory.has(hex(u)));
    for (let i = 0; i < ask.length; i += MAX_USERS) {
      const chunk = ask.slice(i, i + MAX_USERS);
      const resp = await this.call("getDeviceKeys", { case: "getDeviceKeys", value: { userIds: chunk } });
      const listed = new Map(resp.users.map((u) => [hex(u.userId), u.devices]));
      const changed: string[] = [];
      for (const u of chunk) {
        const id = hex(u);
        const devices: Devices = new Map();
        for (const d of listed.get(id) ?? []) {
          if (d.publicKey.length !== 32) continue;
          devices.set(d.deviceId, d.publicKey);
          this.seen.set(deviceId(id, d.deviceId), d.publicKey);
        }
        const old = this.directory.get(id);
        this.directory.set(id, devices);
        if (!old || !sameDevices(old, devices)) changed.push(id);
      }
      if (changed.length > 0) this.hooks.directoryChanged(changed);
    }
    return new Map(wanted.map((u) => [hex(u), this.directory.get(hex(u)) ?? new Map()]));
  }

  /**
   * Applies a device event to the directory. True for one of the user's own
   * devices, with a key, that this device has not seen before: a new device
   * the user may share the history with.
   */
  deviceChanged(userId: Uint8Array, device: Device, signedOut: boolean): boolean {
    const id = hex(userId);
    const key = device.publicKey.length === 32 ? device.publicKey : undefined;
    if (key) this.seen.set(deviceId(id, device.deviceId), key);
    const devices = this.directory.get(id);
    if (devices) {
      const next = new Map(devices);
      if (signedOut) next.delete(device.deviceId);
      else if (key) next.set(device.deviceId, key);
      if (!sameDevices(devices, next)) {
        this.directory.set(id, next);
        this.hooks.directoryChanged([id]);
      }
    }
    return (
      this.ready &&
      sameId(userId, this.self.userId) &&
      !signedOut &&
      !!key &&
      !this.known.has(device.deviceId.toString())
    );
  }

  /** Remembers that the user decided about sharing with the device. */
  async markKnown(id: bigint): Promise<void> {
    this.known.add(id.toString());
    await this.store.setKnownDevices([...this.known]);
  }

  /** The user's fingerprint over their devices in the directory. */
  async fingerprint(userId: Uint8Array, fresh = false): Promise<Uint8Array> {
    const devices = (await this.devicesOf([userId], fresh)).get(hex(userId)) ?? new Map();
    return crypto.fingerprint(
      userId,
      [...devices].map(([id, publicKey]) => ({ deviceId: id, publicKey })),
    );
  }

  /** The safety number of the user and another, from the backend's current directory. */
  async safetyNumber(other: Uint8Array): Promise<string> {
    await this.devicesOf([this.self.userId, other], true);
    return crypto.safetyNumber(await this.fingerprint(this.self.userId), await this.fingerprint(other));
  }

  private keep(conversationId: string, epoch: number, key: CryptoKey): void {
    let epochs = this.epochs.get(conversationId);
    if (!epochs) this.epochs.set(conversationId, (epochs = new Map()));
    epochs.set(epoch, key);
  }

  private async remember(conversationId: string, epoch: number, key: CryptoKey): Promise<void> {
    this.keep(conversationId, epoch, key);
    await this.store.putEpoch({ conversationId, epoch, key });
  }

  private keyOf(conversationId: string, epoch: number): CryptoKey | undefined {
    return this.epochs.get(conversationId)?.get(epoch);
  }

  private highest(conversationId: string): number {
    return Math.max(0, ...(this.epochs.get(conversationId)?.keys() ?? []));
  }

  /** Runs one key change of the conversation at a time. */
  private locked<T>(conversationId: string, fn: () => Promise<T>): Promise<T> {
    const next = (this.locks.get(conversationId) ?? Promise.resolve()).then(fn, fn);
    this.locks.set(
      conversationId,
      next.catch(() => {}),
    );
    return next;
  }

  /** Asks for the conversation's key state and the keys for this device after the epoch. */
  private async fetchKeys(conversationId: Uint8Array, after: number): Promise<KeyState> {
    const id = hex(conversationId);
    const resp = await this.call("getConversationKeys", {
      case: "getConversationKeys",
      value: { conversationId, afterEpoch: after },
    });
    for (const k of resp.keys) {
      if (this.keyOf(id, k.epoch)) continue;
      const key = await this.unwrap(conversationId, k).catch(() => null);
      if (key) await this.remember(id, k.epoch, key);
    }
    const state = {
      currentEpoch: resp.currentEpoch,
      rotate: resp.rotate,
      recipients: new Set(resp.currentRecipients.map((r) => deviceId(r.userId, r.deviceId))),
    };
    this.states.set(id, state);
    return state;
  }

  /** A received key, if its sender's key is the one the directory knows. */
  private async unwrap(conversationId: Uint8Array, k: ReceivedKey): Promise<CryptoKey | null> {
    if (!this.pair) return null;
    const sender = deviceId(k.senderUserId, k.senderDeviceId);
    if (!this.seen.has(sender)) await this.devicesOf([k.senderUserId], true);
    const known = this.seen.get(sender);
    if (!known || !sameId(known, k.senderPublicKey)) return null;
    const raw = await crypto.unwrapKey(k.wrapped, this.pair.privateKey, known, {
      conversationId,
      epoch: k.epoch,
      sender: { userId: k.senderUserId, deviceId: k.senderDeviceId },
      recipient: this.self,
    });
    return crypto.epochKey(raw);
  }

  /** The conversation's key of the epoch, fetched if not held and not missing before. */
  private async keyFor(conversationId: Uint8Array, epoch: number): Promise<CryptoKey | undefined> {
    const id = hex(conversationId);
    const missing = `${id}/${epoch}`;
    const key = this.keyOf(id, epoch);
    if (key || this.missing.has(missing) || epoch <= 0) return key;
    return this.locked(id, async () => {
      if (this.keyOf(id, epoch) || this.missing.has(missing)) return this.keyOf(id, epoch);
      await this.fetchKeys(conversationId, Math.min(epoch - 1, this.highest(id)));
      const got = this.keyOf(id, epoch);
      if (!got) this.missing.add(missing);
      return got;
    });
  }

  /** A sealed message's body, or null if this device cannot open it or does not know its kind. */
  async open(m: Message): Promise<Body | null> {
    if (m.content.case !== "sealed") return null;
    const sealed = m.content.value;
    const key = await this.keyFor(m.conversationId, sealed.epoch);
    if (!key) return null;
    try {
      const plaintext = await crypto.open(key, sealed.nonce, sealed.ciphertext, {
        conversationId: m.conversationId,
        epoch: sealed.epoch,
        senderUserId: m.senderUserId,
        clientMessageId: m.clientMessageId,
      });
      const content = fromBinary(ContentSchema, plaintext);
      return content.body.case === undefined ? null : content.body;
    } catch {
      return null;
    }
  }

  /** A user's sealed reactions to a message, or null if this device cannot open them. */
  async openReactions(
    conversationId: Uint8Array,
    seq: bigint,
    reactorUserId: Uint8Array,
    sealed: Sealed,
  ): Promise<string[] | null> {
    const key = await this.keyFor(conversationId, sealed.epoch);
    if (!key) return null;
    try {
      const plaintext = await crypto.openReactions(key, sealed.nonce, sealed.ciphertext, {
        conversationId,
        epoch: sealed.epoch,
        reactorUserId,
        seq,
      });
      return fromBinary(ReactionsSchema, plaintext).emoji;
    } catch {
      return null;
    }
  }

  /** Lets the conversation's messages that could not be opened try again, as after a device shared its keys. */
  retry(conversationId: string): void {
    for (const m of [...this.missing]) if (m.startsWith(conversationId + "/")) this.missing.delete(m);
  }

  /** Forgets the conversation's key state, so the next send asks again. */
  invalidate(conversationId: string): void {
    this.states.delete(conversationId);
  }

  /** Seals a message body with the conversation's current epoch, making the next epoch first if needed. */
  async seal(conversationId: Uint8Array, clientMessageId: string, body: BodyInit): Promise<SealedInit> {
    const { epoch, key } = await this.locked(hex(conversationId), () => this.current(conversationId));
    const nonce = crypto.randomBytes(crypto.NONCE_BYTES);
    const plaintext = toBinary(ContentSchema, create(ContentSchema, { body }));
    const ciphertext = await crypto.seal(key, nonce, plaintext, {
      conversationId,
      epoch,
      senderUserId: this.self.userId,
      clientMessageId,
    });
    return { epoch, nonce, ciphertext };
  }

  /** Seals the user's reactions to a message with the conversation's current epoch. */
  async sealReactions(conversationId: Uint8Array, seq: bigint, emoji: string[]): Promise<SealedInit> {
    const { epoch, key } = await this.locked(hex(conversationId), () => this.current(conversationId));
    const nonce = crypto.randomBytes(crypto.NONCE_BYTES);
    const plaintext = toBinary(ReactionsSchema, create(ReactionsSchema, { emoji }));
    const ciphertext = await crypto.sealReactions(key, nonce, plaintext, {
      conversationId,
      epoch,
      reactorUserId: this.self.userId,
      seq,
    });
    return { epoch, nonce, ciphertext };
  }

  /** The epoch to seal with: the current one, if its recipients are the member devices, or a new one. */
  private async current(conversationId: Uint8Array): Promise<{ epoch: number; key: CryptoKey }> {
    const id = hex(conversationId);
    for (let attempt = 1; ; attempt++) {
      const state = this.states.get(id) ?? (await this.fetchKeys(conversationId, this.highest(id)));
      const key = this.keyOf(id, state.currentEpoch);
      if (key && !needsEpoch(state, new Set((await this.roster(conversationId, false)).keys()), true)) {
        return { epoch: state.currentEpoch, key };
      }
      // Before replacing the epoch, or making the first, look at the members and their devices afresh: the caches
      // may be behind.
      const devices = await this.roster(conversationId, true);
      if (key && !needsEpoch(state, new Set(devices.keys()), true)) return { epoch: state.currentEpoch, key };
      try {
        return await this.publish(conversationId, state.currentEpoch + 1, devices);
      } catch (err) {
        // Another device published first, or the members changed: look again.
        const retry =
          err instanceof BackendError &&
          (err.code === ErrorCode.FAILED_PRECONDITION || err.code === ErrorCode.INVALID_ARGUMENT);
        if (!retry || attempt >= ATTEMPTS) throw err;
        this.states.delete(id);
      }
    }
  }

  /** The member devices with keys, this one always among them. */
  private async roster(conversationId: Uint8Array, fresh: boolean) {
    const members = await this.hooks.members(conversationId, fresh);
    const directory = await this.devicesOf(members, fresh);
    // This device first: of two devices publishing the same epoch, the
    // backend refuses the one whose batch includes itself but did not make it.
    const out = new Map<string, Recipient>();
    if (this.pair) {
      out.set(deviceId(this.self.userId, this.self.deviceId), { ref: this.self, publicKey: this.pair.publicKey });
    }
    for (const u of members) {
      for (const [id, publicKey] of directory.get(hex(u)) ?? []) {
        out.set(deviceId(u, id), { ref: { userId: u, deviceId: id }, publicKey });
      }
    }
    return out;
  }

  /** Makes a new epoch key and publishes it wrapped for every device. */
  private async publish(conversationId: Uint8Array, epoch: number, devices: Map<string, Recipient>) {
    const pair = this.pair;
    if (!pair) throw new Error("This device has no key yet.");
    const raw = crypto.randomBytes(crypto.KEY_BYTES);
    const keys = await Promise.all(
      [...devices.values()].map(async (d) => ({
        recipientUserId: d.ref.userId,
        recipientDeviceId: d.ref.deviceId,
        wrapped: await crypto.wrapKey(raw, pair.privateKey, d.publicKey, {
          conversationId,
          epoch,
          sender: this.self,
          recipient: d.ref,
        }),
      })),
    );
    // The first batch starts the epoch; any more add to it.
    for (let i = 0; i < keys.length; i += MAX_KEYS) {
      await this.call("publishKeys", {
        case: "publishKeys",
        value: { conversationId, epoch, keys: keys.slice(i, i + MAX_KEYS) },
      });
    }
    const id = hex(conversationId);
    const key = await crypto.epochKey(raw);
    await this.remember(id, epoch, key);
    this.states.set(id, { currentEpoch: epoch, rotate: false, recipients: new Set(devices.keys()) });
    return { epoch, key };
  }

  /** Wraps every epoch key this device holds for another device of the user; returns the conversations covered. */
  async share(device: { deviceId: bigint; publicKey: Uint8Array }): Promise<number> {
    const pair = this.pair;
    if (!pair) return 0;
    const recipient = { userId: this.self.userId, deviceId: device.deviceId };
    let shared = 0;
    for (const [id, epochs] of this.epochs) {
      const conversationId = fromHex(id);
      let any = false;
      for (const [epoch, key] of [...epochs].sort(([a], [b]) => a - b)) {
        const wrapped = await crypto.wrapKey(await crypto.rawKey(key), pair.privateKey, device.publicKey, {
          conversationId,
          epoch,
          sender: this.self,
          recipient,
        });
        try {
          await this.call("publishKeys", {
            case: "publishKeys",
            value: {
              conversationId,
              epoch,
              keys: [{ recipientUserId: recipient.userId, recipientDeviceId: recipient.deviceId, wrapped }],
            },
          });
          any = true;
        } catch (err) {
          if (!(err instanceof BackendError)) throw err;
          // No longer a member: nothing more to add there.
          if (err.code === ErrorCode.PERMISSION_DENIED) break;
        }
      }
      if (any) shared++;
      this.states.delete(id);
    }
    return shared;
  }
}
