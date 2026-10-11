// The device's session: signing in, the cursor into the user's log, and the
// flow of events into the model. Connection details live outside React
// state; the screens read the store.

import { create as createMessage } from "@bufbuild/protobuf";
import { timestampNow } from "@bufbuild/protobuf/wkt";
import { create } from "zustand";
import type { Event } from "../gen/wrinkle/v1/event_pb";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import { BlobPurpose } from "../gen/wrinkle/v1/messaging_pb";
import type { DeviceCredentials } from "../gen/wrinkle/v1/messaging_pb";
import { SealedSchema } from "../gen/wrinkle/v1/model_pb";
import type { Conversation, Device, Message, User } from "../gen/wrinkle/v1/model_pb";
import { blobAddress } from "../blobs/blobs";
import { BackendError, Connection } from "../connection/connection";
import type { ConnectionState } from "../connection/connection";
import {
  addHistory,
  addMessages,
  applyEvent,
  emptyData,
  isDeleted,
  key,
  noteLatest,
  STICKER,
  textKey,
  unread,
  upsertConversation,
  withDeleted,
  withMembers,
  withStickers,
  withTexts,
  withUsers,
} from "../conversations/model";
import type { Data } from "../conversations/model";
import { addPending, confirmPending, failPending, newMessageId } from "../conversations/pending";
import type { PendingByConversation } from "../conversations/pending";
import { deviceLabel, sortDevices, withDevice } from "../devices/model";
import { available } from "../e2ee/crypto";
import { Keys } from "../e2ee/keys";
import type { BodyInit, Call } from "../e2ee/keys";
import { deleteStore, openStore, storeName } from "../e2ee/store";
import type { KeyStore } from "../e2ee/store";
import { loadVerified, saveVerified } from "../e2ee/verified";
import type { Verified } from "../e2ee/verified";
import { isFormer, isGroup, noticeUsers } from "../groups/model";
import { fromHex, hex, sameId } from "../ids";
import { cleaned, ownEmoji, toggled, withOpened, withReaction } from "../reactions/model";
import { rememberGateway } from "../signin/gateways";
import { moved, withoutSticker, withSaved } from "../stickers/model";
import { saved } from "./saved";
import type { Credentials } from "./saved";

const SYNC_LIMIT = 200;
const HISTORY_PAGE = 50;

export type AuthMode = "signIn" | "register";

/** Another device of the user that signed in and may get the message history. */
export interface NewDevice {
  deviceId: bigint;
  name: string;
  publicKey: Uint8Array;
}

export interface Session extends Data {
  credentials: Credentials | null;
  me: User | null;
  /** Connected means Connect completed, not only that the socket is open. */
  status: ConnectionState;
  /** The conversation on screen. */
  current: string | null;
  /** Conversations whose history is loaded back to its start, by id. */
  complete: Record<string, true>;
  /** Settings on screen, in place of the conversation. */
  settings: boolean;
  pending: PendingByConversation;
  /** The account's signed-in devices, once listed. */
  devices: Device[] | null;
  error: string | null;
  /** Why the device was signed out, for the sign-in screen. */
  notice: string | null;
  /** Users' fingerprints in hex, by hex id, for those looked up. */
  fingerprints: Record<string, string>;
  /** Contacts the user verified, with the fingerprint verified. */
  verified: Verified;
  /** A new device of the user waiting for the decision on sharing the history. */
  newDevice: NewDevice | null;
  /** What changed about the account's encryption, until dismissed. */
  securityNotice: string | null;
  /** The user's saved stickers, ids in hex in the user's order, once loaded. */
  palette: string[] | null;
  /** Reconnects with the saved credentials, if any. */
  resume(): void;
  /** Registers or signs in, then connects; rejects with the backend's error. */
  authenticate(mode: AuthMode, name: string, password: string, url: string): Promise<void>;
  signOut(): Promise<void>;
  updateProfile(change: { name?: string; icon?: string; avatar?: Uint8Array }): Promise<void>;
  /** Uploads a prepared picture and makes it the avatar. */
  setPicture(picture: Blob): Promise<void>;
  changePassword(current: string, next: string): Promise<void>;
  loadDevices(): Promise<void>;
  renameDevice(deviceId: bigint, name: string): Promise<void>;
  signOutDevice(deviceId: bigint): Promise<void>;
  deleteAccount(password: string): Promise<void>;
  openConversation(peerName: string): Promise<void>;
  /** Creates a group and shows it; the same clientGroupId on a retry returns the group created first. */
  createGroup(topic: string, memberNames: string[], clientGroupId: string): Promise<void>;
  /** Lists the current group's members again. */
  loadMembers(conversationId: string): Promise<void>;
  addMembers(memberNames: string[]): Promise<void>;
  removeMember(userId: Uint8Array): Promise<void>;
  setAdmin(userId: Uint8Array, admin: boolean): Promise<void>;
  setTopic(topic: string): Promise<void>;
  leaveGroup(): Promise<void>;
  show(conversationId: string | null): Promise<void>;
  showSettings(open: boolean): void;
  loadEarlier(): Promise<void>;
  send(text: string): Promise<void>;
  /** Sends a sticker, by its id in hex, to the current conversation. */
  sendSticker(stickerId: string): Promise<void>;
  /** Uploads a prepared picture as a new sticker, which the backend saves to the palette. */
  createSticker(picture: Blob): Promise<void>;
  saveSticker(stickerId: string): Promise<void>;
  removeSticker(stickerId: string): Promise<void>;
  /** Moves a saved sticker before another, or to the end when before is empty. */
  moveSticker(stickerId: string, before: string): Promise<void>;
  /** Deletes one of the user's own messages in the current conversation, for everyone. */
  deleteMessage(seq: bigint): Promise<void>;
  /** Adds the emoji to the user's reactions to a message in the current conversation, or removes it if there. */
  react(seq: bigint, emoji: string): Promise<void>;
  markReadIfVisible(): void;
  clearError(): void;
  /** Shares the message history with the new device, or not; either way it is no longer new. */
  decideSharing(share: boolean): Promise<void>;
  dismissSecurityNotice(): void;
  /** Looks up the users' devices, for their fingerprints. */
  lookUpDevices(userIds: Uint8Array[]): Promise<void>;
  /** The safety number of the user and another, from their current devices. */
  safetyNumber(userId: Uint8Array): Promise<string>;
  /** Marks a contact verified with their current fingerprint. */
  markVerified(userId: Uint8Array): Promise<void>;
}

interface PendingAuth {
  mode: AuthMode;
  name: string;
  password: string;
  resolve(): void;
  reject(err: Error): void;
}

let conn: Connection | null = null;
let cursor = 0n;
let pendingAuth: PendingAuth | null = null;
/** Conversations whose newest page of history was loaded since connecting; events alone may bring only the latest messages. */
const loaded = new Set<string>();
/** A sign-out this device asked for, with the notice to show when the server closes the connection for it. */
let leaving: { notice: string | null } | null = null;
/** The device's end-to-end keys, once signed in, and whether they started. */
let keys: Keys | null = null;
let keyStore: KeyStore | null = null;
let keysReady: Promise<boolean> = Promise.resolve(false);
/** New devices of the user after the one being asked about. */
let newDevices: NewDevice[] = [];
/** Messages being opened, by textKey. */
const opening = new Set<string>();
/** Reactions being opened, by textKey, user and nonce. */
const openingReactions = new Set<string>();

const NO_CRYPTO = "End-to-end encryption needs a secure page: open the client over HTTPS.";
const KEY_LOST = "This device lost its encryption keys. Sign in again.";

/** The state with no user's data in it. */
const blank = {
  ...emptyData(),
  me: null,
  current: null,
  complete: {},
  settings: false,
  pending: {},
  devices: null,
  fingerprints: {},
  verified: {},
  newDevice: null,
  securityNotice: null,
  palette: null,
} satisfies Partial<Session>;

const isVisible = () => typeof document === "undefined" || document.visibilityState === "visible";
const deviceName = () => (typeof navigator === "undefined" ? "" : navigator.userAgent.slice(0, 64));

export const useSession = create<Session>((set, get) => {
  const fail = (err: unknown) => set({ error: err instanceof Error ? err.message : String(err) });

  const call: Call = (expected, body) => {
    if (!conn) return Promise.reject(new Error("not connected"));
    return conn.call(expected, body);
  };

  /** Starts the device's keys for the signed-in device; false if it signed out for having lost them. */
  async function startKeys(credentials: Credentials): Promise<boolean> {
    if (keys?.ready) return true;
    if (!available()) {
      set({ error: NO_CRYPTO });
      return true;
    }
    if (!keys) {
      const self = { userId: fromHex(credentials.userId), deviceId: BigInt(credentials.deviceId) };
      keyStore = await openStore(storeName(credentials.userId, credentials.deviceId));
      keys = new Keys(call, self, keyStore, { members: membersOf, directoryChanged });
    }
    const k = keys;
    keysReady = k.start();
    let ok;
    try {
      ok = await keysReady;
    } catch (err) {
      keysReady = Promise.resolve(false);
      fail(err);
      return true;
    }
    if (!ok) {
      // The backend has another key for this device: it signs in again as a new one.
      leaving = { notice: KEY_LOST };
      await conn?.call("signOut", { case: "signOut", value: {} }).catch(() => {});
      signedOut(KEY_LOST);
      return false;
    }
    set({ verified: loadVerified(credentials.userId) });
    // Verified contacts' devices, so a change since shows.
    const verified = Object.keys(get().verified).map(fromHex);
    if (verified.length > 0) k.devicesOf(verified, true).catch(fail);
    return true;
  }

  /** The conversation's current members, for the key epochs. */
  async function membersOf(conversationId: Uint8Array, fresh: boolean): Promise<Uint8Array[]> {
    const id = key(conversationId);
    const c = get().conversations[id];
    const me = get().me;
    if (!c || !me) throw new Error("unknown conversation");
    if (!isGroup(c)) return c.peer ? [me.userId, c.peer.userId] : [me.userId];
    if (fresh || !get().members[id]) await refreshMembers(id);
    return (get().members[id] ?? []).flatMap((m) => (m.user ? [m.user.userId] : []));
  }

  function directoryChanged(userIds: string[]) {
    const k = keys;
    if (!k) return;
    for (const u of userIds) {
      k.fingerprint(fromHex(u))
        .then((fp) => {
          if (keys === k) set({ fingerprints: { ...get().fingerprints, [u]: hex(fp) } });
        })
        .catch(fail);
    }
  }

  /** Opens sealed messages not opened yet; again, those that could not be opened before. */
  function openMessages(messages: (Message | undefined)[], again = false) {
    const k = keys;
    if (!k?.ready) return;
    for (const m of messages) {
      if (!m || m.content.case !== "sealed" || isDeleted(m)) continue;
      const t = textKey(m);
      const known = get().texts[t];
      if (opening.has(t) || (known !== undefined && !(again && known === null))) continue;
      opening.add(t);
      k.open(m)
        .then((body) => {
          if (keys !== k || (body === null && get().texts[t] != null)) return;
          if (body?.case === "sticker") set(withStickers(get(), { [t]: hex(body.value.stickerId) }));
          else set(withTexts(get(), { [t]: body?.case === "text" ? body.value : null }));
        })
        .catch(() => {
          // Not reachable now: tried again on the next look.
        })
        .finally(() => opening.delete(t));
    }
  }

  /** Opens the reactions of the conversation not opened yet; again, those that could not be opened before. */
  function openReactions(conversationId: string, again = false) {
    const k = keys;
    if (!k?.ready) return;
    const prefix = conversationId + "/";
    for (const [messageKey, users] of Object.entries(get().reactions)) {
      if (!messageKey.startsWith(prefix)) continue;
      const seq = BigInt(messageKey.slice(prefix.length));
      for (const [user, e] of Object.entries(users)) {
        if (e.emoji !== undefined && !(again && e.emoji === null)) continue;
        const sealed = e.sealed;
        const o = `${messageKey}/${user}/${hex(sealed.nonce)}`;
        if (openingReactions.has(o)) continue;
        openingReactions.add(o);
        k.openReactions(fromHex(conversationId), seq, fromHex(user), sealed)
          .then((emoji) => {
            if (keys !== k) return;
            const opened = emoji && cleaned(emoji);
            set({ reactions: withOpened(get().reactions, messageKey, user, sealed, opened) });
          })
          .catch(() => {
            // Not reachable now: tried again on the next look.
          })
          .finally(() => openingReactions.delete(o));
      }
    }
  }

  function openLatest() {
    openMessages(Object.values(get().conversations).map((c) => c.lastMessage));
  }

  function offerSharing(device: Device) {
    const d = { deviceId: device.deviceId, name: deviceLabel(device), publicKey: device.publicKey };
    if (get().newDevice?.deviceId === d.deviceId || newDevices.some((x) => x.deviceId === d.deviceId)) return;
    if (get().newDevice) newDevices.push(d);
    else set({ newDevice: d });
  }

  function saveCursor(next: bigint) {
    cursor = next;
    saved.setCursor(cursor);
  }

  function connectTo(url: string) {
    conn?.close();
    conn = new Connection(url, {
      // Online once Connect completes.
      state: (status) => {
        if (status === "offline" && pendingAuth) {
          failAuth(pendingAuth, new Error("Could not reach the gateway."));
          return;
        }
        set({ status: status === "connected" ? "connecting" : status });
      },
      open: () => onOpen().catch(fail),
      event: onEvent,
      signedOut: () => signedOut(leaving ? leaving.notice : "Signed out."),
    });
    conn.open();
  }

  function failAuth(a: PendingAuth, err: Error) {
    pendingAuth = null;
    conn?.close();
    conn = null;
    set({ status: "offline" });
    a.reject(err);
  }

  /** Forgets the device and returns to the sign-in screen. */
  function signedOut(notice: string | null) {
    pendingAuth = null;
    leaving = null;
    loaded.clear();
    conn?.close();
    conn = null;
    // A signed-out device's keys are of no further use.
    const credentials = get().credentials;
    keyStore?.close();
    if (credentials) deleteStore(storeName(credentials.userId, credentials.deviceId));
    keys = null;
    keyStore = null;
    keysReady = Promise.resolve(false);
    newDevices = [];
    opening.clear();
    openingReactions.clear();
    saved.forget();
    set({ ...blank, credentials: null, status: "offline", error: null, notice });
  }

  async function authenticateOnOpen(c: Connection, a: PendingAuth) {
    // A connection lost from here on fails the call instead.
    pendingAuth = null;
    const value = { name: a.name, password: a.password, deviceName: deviceName() };
    let creds: DeviceCredentials | undefined;
    try {
      creds =
        a.mode === "register"
          ? (await c.call("register", { case: "register", value })).credentials
          : (await c.call("signIn", { case: "signIn", value })).credentials;
      if (!creds) throw new Error("no credentials in the response");
    } catch (err) {
      failAuth(a, err as Error);
      return false;
    }
    const credentials = {
      userId: hex(creds.userId),
      deviceId: creds.deviceId.toString(),
      deviceToken: creds.deviceToken,
      url: c.url,
    };
    saved.setCredentials(credentials);
    cursor = 0n;
    set({ credentials, notice: null });
    a.resolve();
    return true;
  }

  async function onOpen() {
    const c = conn;
    if (!c) return;
    // Connect must follow Register or SignIn within the connect deadline.
    if (pendingAuth && !(await authenticateOnOpen(c, pendingAuth))) return;
    const credentials = get().credentials;
    if (!credentials || conn !== c) return;
    let resp;
    try {
      resp = await c.call("connect", { case: "connect", value: { deviceToken: credentials.deviceToken } });
    } catch (err) {
      if (err instanceof BackendError && err.code === ErrorCode.UNAUTHENTICATED) {
        signedOut("This device was signed out. Sign in again.");
        return;
      }
      // Not connected, such as when pushes could not be set up: try again.
      c.restart();
      throw err;
    }
    rememberGateway(c.url);
    if (cursor === 0n) {
      // A device without history starts at the head; the past comes from the
      // conversation list and history requests.
      saveCursor(resp.logHead);
    }
    set({ me: resp.user ?? null, status: "connected", error: null });
    if (!(await startKeys(credentials))) return;
    refreshPalette().catch(fail);
    await refreshConversations();
    await syncFromCursor();
    const current = get().current;
    if (current) {
      await loadHistory(current, 0n);
      await refreshMembers(current);
    }
  }

  async function refreshConversations() {
    if (!conn) return;
    const resp = await conn.call("listConversations", { case: "listConversations", value: {} });
    let data: Data = get();
    for (const c of resp.conversations) data = upsertConversation(data, c);
    set({ conversations: data.conversations, users: data.users });
    for (const c of resp.conversations) learnSender(key(c.conversationId));
    openLatest();
  }

  async function refreshPalette() {
    if (!conn) return;
    const resp = await conn.call("getPalette", { case: "getPalette", value: {} });
    set({ palette: resp.stickers.map((st) => hex(st.stickerId)) });
  }

  /** Uploads a blob for the purpose; resolves with its id. */
  async function upload(purpose: BlobPurpose, blob: Blob): Promise<Uint8Array> {
    const c = conn;
    const address = c && blobAddress(c.url);
    if (!c || !address) throw new Error("not connected");
    const u = await c.call("createUpload", {
      case: "createUpload",
      value: { purpose, mediaType: blob.type, size: BigInt(blob.size) },
    });
    // The browser sets Content-Length from the body, which is the size asked for.
    const resp = await fetch(address + u.uploadPath, {
      method: "PUT",
      headers: { "Content-Type": blob.type },
      body: blob,
    });
    if (!resp.ok) throw new Error(`The upload failed (HTTP ${resp.status}).`);
    return u.blobId;
  }

  /** Seals and sends a message to the current conversation, shown pending until confirmed. */
  async function sendBody(body: BodyInit, text: string, sticker?: string) {
    const id = get().current;
    if (!id || !conn) return;
    const clientMessageId = newMessageId();
    set({ pending: addPending(get().pending, id, { clientMessageId, text, sticker }) });
    try {
      const conversationId = fromHex(id);
      let resp;
      for (let attempt = 1; ; attempt++) {
        if (!keys?.ready) throw new Error(available() ? "Encryption is not ready yet." : NO_CRYPTO);
        const sealed = await keys.seal(conversationId, clientMessageId, body);
        try {
          resp = await call("sendMessage", {
            case: "sendMessage",
            value: { conversationId, clientMessageId, sealed },
          });
          break;
        } catch (err) {
          // Another device made a newer epoch, or the members changed: look again, once.
          if (attempt > 1 || !(err instanceof BackendError) || err.code !== ErrorCode.FAILED_PRECONDITION) throw err;
          keys.invalidate(id);
        }
      }
      const m = resp.message;
      if (!m) return;
      const t = textKey(m);
      const sent = noteLatest(addMessages(get(), m.conversationId, [m]), m, true);
      const data = sticker ? withStickers(sent, { [t]: sticker }) : withTexts(sent, { [t]: text });
      set({ ...data, pending: confirmPending(get().pending, m.conversationId, m.clientMessageId) });
    } catch (err) {
      set({ pending: failPending(get().pending, id, clientMessageId) });
      throw err;
    }
  }

  /** Groups listing members right now, so each is asked once at a time. */
  const listing = new Set<string>();

  /** Lists a group's members when its latest message is from someone not known yet, for the list's preview. */
  function learnSender(conversationId: string) {
    const { conversations, users, members, me } = get();
    const c = conversations[conversationId];
    const m = c?.lastMessage;
    if (!m || !isGroup(c) || sameId(m.senderUserId, me?.userId) || users[hex(m.senderUserId)]) return;
    if (members[conversationId] || listing.has(conversationId)) return;
    refreshMembers(conversationId).catch(fail);
  }

  /** Lists a group's members, if the user is still in it. */
  async function refreshMembers(conversationId: string) {
    const c = get().conversations[conversationId];
    if (!conn || !c || !isGroup(c) || !c.member) return;
    listing.add(conversationId);
    let resp;
    try {
      resp = await conn.call("getMembers", { case: "getMembers", value: { conversationId: c.conversationId } });
    } finally {
      listing.delete(conversationId);
    }
    const data = withMembers(get(), c.conversationId, resp.members);
    set({ conversations: data.conversations, members: data.members, users: data.users });
  }

  function current(): Conversation {
    const id = get().current;
    const c = id ? get().conversations[id] : undefined;
    if (!c || !conn) throw new Error("not connected");
    return c;
  }

  async function syncFromCursor() {
    if (!conn) return;
    for (;;) {
      const resp = await conn.call("sync", { case: "sync", value: { cursor, limit: SYNC_LIMIT } });
      for (const ev of resp.events) apply(ev);
      saveCursor(resp.cursor);
      if (!resp.hasMore) break;
    }
    conn.ack(cursor);
    get().markReadIfVisible();
  }

  function apply(event: Event) {
    const me = get().me;
    if (!me) return;
    if (event.body.case === "profile" && sameId(event.body.value.user?.userId, me.userId)) {
      set({ me: event.body.value.user ?? me });
      return;
    }
    if (event.body.case === "palette") {
      set({ palette: event.body.value.stickerIds.map(hex) });
      return;
    }
    if (event.body.case === "device") {
      const { device, signedOut } = event.body.value;
      // Others' devices reach the user too, with only their keys.
      const owner = event.body.value.userId.length > 0 ? event.body.value.userId : me.userId;
      const devices = get().devices;
      if (device && devices && sameId(owner, me.userId)) set({ devices: withDevice(devices, device, signedOut) });
      if (device) {
        keysReady
          .then((ok) => {
            if (ok && keys?.deviceChanged(owner, device, signedOut)) offerSharing(device);
          })
          .catch(fail);
      }
      return;
    }
    const { data, unknownConversation, membersChanged } = applyEvent(get(), event, me.userId);
    const m = event.body.case === "message" ? event.body.value.message : undefined;
    const pending = m ? confirmPending(get().pending, m.conversationId, m.clientMessageId) : get().pending;
    set({
      conversations: data.conversations,
      messages: data.messages,
      members: data.members,
      users: data.users,
      reactions: data.reactions,
      pending,
    });
    if (event.body.case === "reaction") openReactions(key(event.body.value.conversationId));
    if (unknownConversation) refreshConversations().catch(fail);
    if (m) {
      const id = key(m.conversationId);
      // A membership change needs a new epoch before the next send.
      if (membersChanged) keys?.invalidate(id);
      if (membersChanged && (get().members[id] || get().current === id)) refreshMembers(id).catch(fail);
      else learnSender(id);
      openMessages([m]);
    }
  }

  function onEvent(event: Event) {
    // Pushes and sync overlap; the cursor tells them apart. A gap means
    // something was missed: sync fills it.
    if (event.cursor <= cursor) return;
    if (event.cursor !== cursor + 1n) {
      syncFromCursor().catch(fail);
      return;
    }
    apply(event);
    saveCursor(event.cursor);
    conn?.ack(cursor);
    get().markReadIfVisible();
  }

  async function loadHistory(conversationId: string, beforeSeq: bigint) {
    if (!conn) return;
    const id = fromHex(conversationId);
    const resp = await conn.call("getMessages", {
      case: "getMessages",
      value: { conversationId: id, beforeSeq, limit: HISTORY_PAGE },
    });
    if (beforeSeq === 0n) loaded.add(conversationId);
    const data = addHistory(get(), id, resp.messages);
    const notices = resp.messages.flatMap((m) => (m.content.case === "notice" ? [m.content.value] : []));
    const users = withUsers(data, notices.flatMap(noticeUsers)).users;
    // A short page reaches the start: of the conversation, or of what a group member may see.
    const complete =
      resp.messages.length < HISTORY_PAGE ? { ...get().complete, [conversationId]: true as const } : get().complete;
    set({ messages: data.messages, reactions: data.reactions, users, complete });
    openMessages(resp.messages);
    openReactions(conversationId);
  }

  // Back on screen: reconnect now instead of waiting out the backoff.
  if (typeof document !== "undefined") {
    document.addEventListener("visibilitychange", () => {
      if (isVisible()) {
        conn?.wake();
        get().markReadIfVisible();
      }
    });
  }

  return {
    ...blank,
    credentials: saved.credentials(),
    status: "offline",
    error: null,
    notice: null,

    resume() {
      const credentials = get().credentials;
      if (!credentials || conn) return;
      cursor = saved.cursor();
      connectTo(credentials.url);
    },

    authenticate(mode, name, password, url) {
      signedOut(null);
      return new Promise<void>((resolve, reject) => {
        pendingAuth = { mode, name, password, resolve, reject };
        connectTo(url);
      });
    },

    async signOut() {
      const c = conn;
      leaving = { notice: null };
      if (c && get().status === "connected") {
        await c.call("signOut", { case: "signOut", value: {} }).catch(() => {});
      }
      signedOut(null);
    },

    async updateProfile(change) {
      if (!conn) throw new Error("not connected");
      const resp = await conn.call("updateProfile", { case: "updateProfile", value: change });
      if (resp.user) set({ me: resp.user });
    },

    async setPicture(picture) {
      const avatar = await upload(BlobPurpose.AVATAR, picture);
      await get().updateProfile({ avatar });
    },

    async changePassword(current, next) {
      if (!conn) throw new Error("not connected");
      await conn.call("changePassword", {
        case: "changePassword",
        value: { currentPassword: current, newPassword: next },
      });
    },

    async loadDevices() {
      if (!conn) return;
      const resp = await conn.call("listDevices", { case: "listDevices", value: {} });
      set({ devices: sortDevices(resp.devices) });
    },

    async renameDevice(deviceId, name) {
      if (!conn) throw new Error("not connected");
      const resp = await conn.call("renameDevice", { case: "renameDevice", value: { deviceId, name } });
      const devices = get().devices;
      if (resp.device && devices) set({ devices: withDevice(devices, resp.device, false) });
    },

    async signOutDevice(deviceId) {
      if (!conn) throw new Error("not connected");
      await conn.call("signOutDevice", { case: "signOutDevice", value: { deviceId } });
      const devices = get().devices;
      if (devices) set({ devices: devices.filter((d) => d.deviceId !== deviceId) });
    },

    async deleteAccount(password) {
      if (!conn) throw new Error("not connected");
      const notice = "Your account was deleted.";
      leaving = { notice };
      try {
        await conn.call("deleteAccount", { case: "deleteAccount", value: { password } });
      } catch (err) {
        // The server may close the connection before the response arrives.
        if (!get().credentials) return;
        leaving = null;
        throw err;
      }
      signedOut(notice);
    },

    async openConversation(peerName) {
      if (!conn) return;
      const resp = await conn.call("openConversation", { case: "openConversation", value: { peerName } });
      if (!resp.conversation) return;
      set({ conversations: upsertConversation(get(), resp.conversation).conversations });
      openLatest();
      await get().show(key(resp.conversation.conversationId));
    },

    async createGroup(topic, memberNames, clientGroupId) {
      if (!conn) throw new Error("not connected");
      const resp = await conn.call("createGroup", {
        case: "createGroup",
        value: { topic, memberNames, clientGroupId },
      });
      if (!resp.conversation) return;
      set({ conversations: upsertConversation(get(), resp.conversation).conversations });
      await get().show(key(resp.conversation.conversationId));
    },

    loadMembers: refreshMembers,

    async addMembers(memberNames) {
      const c = current();
      await conn!.call("addMembers", { case: "addMembers", value: { conversationId: c.conversationId, memberNames } });
      await refreshMembers(key(c.conversationId));
    },

    async removeMember(userId) {
      const c = current();
      await conn!.call("removeMember", { case: "removeMember", value: { conversationId: c.conversationId, userId } });
      await refreshMembers(key(c.conversationId));
    },

    async setAdmin(userId, admin) {
      const c = current();
      await conn!.call("setAdmin", { case: "setAdmin", value: { conversationId: c.conversationId, userId, admin } });
      await refreshMembers(key(c.conversationId));
    },

    async setTopic(topic) {
      const c = current();
      await conn!.call("setTopic", { case: "setTopic", value: { conversationId: c.conversationId, topic } });
    },

    async leaveGroup() {
      const c = current();
      await conn!.call("leaveGroup", { case: "leaveGroup", value: { conversationId: c.conversationId } });
    },

    async show(conversationId) {
      set({ current: conversationId, settings: false });
      if (!conversationId) return;
      // Keys may have arrived since, as from another device of the user.
      keys?.retry(conversationId);
      const c = get().conversations[conversationId];
      openMessages([...Object.values(get().messages[conversationId] ?? {}), c?.lastMessage], true);
      openReactions(conversationId, true);
      if (!loaded.has(conversationId)) await loadHistory(conversationId, 0n);
      get().markReadIfVisible();
      await refreshMembers(conversationId).catch(fail);
      membersOf(fromHex(conversationId), false)
        .then((members) => get().lookUpDevices(members))
        .catch(fail);
    },

    showSettings(open) {
      set({ settings: open });
    },

    async loadEarlier() {
      const id = get().current;
      if (!id) return;
      const seqs = Object.values(get().messages[id] ?? {}).map((m) => m.seq);
      if (seqs.length === 0) return;
      const oldest = seqs.reduce((a, b) => (a < b ? a : b));
      await loadHistory(id, oldest);
    },

    send(text) {
      return sendBody({ case: "text", value: text }, text);
    },

    sendSticker(stickerId) {
      return sendBody({ case: "sticker", value: { stickerId: fromHex(stickerId) } }, STICKER, stickerId);
    },

    async createSticker(picture) {
      const blobId = await upload(BlobPurpose.STICKER, picture);
      await call("createSticker", { case: "createSticker", value: { blobId } });
      // The palette event follows; showing it now does not wait for it.
      const palette = get().palette;
      if (palette) set({ palette: withSaved(palette, hex(blobId)) });
    },

    async saveSticker(stickerId) {
      await call("saveSticker", { case: "saveSticker", value: { stickerId: fromHex(stickerId) } });
      const palette = get().palette;
      if (palette) set({ palette: withSaved(palette, stickerId) });
    },

    async removeSticker(stickerId) {
      await call("removeSticker", { case: "removeSticker", value: { stickerId: fromHex(stickerId) } });
      const palette = get().palette;
      if (palette) set({ palette: withoutSticker(palette, stickerId) });
    },

    async moveSticker(stickerId, before) {
      await call("moveSticker", {
        case: "moveSticker",
        value: { stickerId: fromHex(stickerId), beforeStickerId: fromHex(before) },
      });
      const palette = get().palette;
      if (palette) set({ palette: moved(palette, stickerId, before) });
    },

    async deleteMessage(seq) {
      const c = current();
      await conn!.call("deleteMessage", { case: "deleteMessage", value: { conversationId: c.conversationId, seq } });
      // The event follows; showing it now does not wait for it.
      const data = withDeleted(get(), c.conversationId, seq, timestampNow());
      set({ conversations: data.conversations, messages: data.messages, reactions: data.reactions });
    },

    async react(seq, emoji) {
      const c = current();
      const conversationId = c.conversationId;
      const id = key(conversationId);
      const messageKey = textKey({ conversationId, seq });
      const next = toggled(ownEmoji(get().reactions[messageKey], get().me?.userId), emoji);
      if (!next) return;
      for (let attempt = 1; ; attempt++) {
        if (!keys?.ready) throw new Error(available() ? "Encryption is not ready yet." : NO_CRYPTO);
        const sealed = next.length > 0 ? await keys.sealReactions(conversationId, seq, next) : undefined;
        try {
          await call("setReaction", { case: "setReaction", value: { conversationId, seq, sealed } });
          // The event follows; showing it now does not wait for it.
          const me = get().me;
          if (me) {
            const s = sealed && createMessage(SealedSchema, sealed);
            set({ reactions: withReaction(get().reactions, messageKey, me.userId, s, timestampNow(), next) });
          }
          return;
        } catch (err) {
          // Another device made a newer epoch, or the members changed: look again, once.
          const stale = err instanceof BackendError && err.code === ErrorCode.FAILED_PRECONDITION && sealed;
          if (attempt > 1 || !stale) throw err;
          keys.invalidate(id);
        }
      }
    },

    markReadIfVisible() {
      const id = get().current;
      const c = id ? get().conversations[id] : undefined;
      if (!id || !c || !conn || unread(c) === 0n || !isVisible()) return;
      const seq = c.lastSeq;
      set({ conversations: { ...get().conversations, [id]: { ...c, readSeq: seq } } });
      // Former members cannot move their read position on the backend.
      if (isFormer(c)) return;
      conn.call("markRead", { case: "markRead", value: { conversationId: c.conversationId, seq } }).catch(fail);
    },

    clearError() {
      set({ error: null });
    },

    async decideSharing(share) {
      const d = get().newDevice;
      const k = keys;
      if (!d || !k) return;
      let notice;
      if (share) {
        await k.share(d);
        notice = `Your message history was shared with ${d.name}.`;
      } else {
        notice = `${d.name} signed in. Your message history was not shared with it.`;
      }
      await k.markKnown(d.deviceId);
      set({ newDevice: newDevices.shift() ?? null, securityNotice: notice });
    },

    dismissSecurityNotice() {
      set({ securityNotice: null });
    },

    async lookUpDevices(userIds) {
      await keys?.devicesOf(userIds);
    },

    async safetyNumber(userId) {
      if (!keys?.ready) throw new Error(NO_CRYPTO);
      return keys.safetyNumber(userId);
    },

    async markVerified(userId) {
      const me = get().credentials?.userId;
      if (!keys?.ready || !me) throw new Error(NO_CRYPTO);
      const fp = hex(await keys.fingerprint(userId, true));
      const u = hex(userId);
      const verified = { ...get().verified, [u]: fp };
      saveVerified(me, verified);
      set({ verified, fingerprints: { ...get().fingerprints, [u]: fp } });
    },
  };
});
