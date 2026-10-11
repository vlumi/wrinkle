// The device's session: signing in, the cursor into the user's log, and the
// flow of events into the model. Connection details live outside React
// state; the screens read the store.

import { create } from "zustand";
import type { Event } from "../gen/wrinkle/v1/event_pb";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import { BlobPurpose } from "../gen/wrinkle/v1/messaging_pb";
import type { DeviceCredentials } from "../gen/wrinkle/v1/messaging_pb";
import type { Device, User } from "../gen/wrinkle/v1/model_pb";
import { blobAddress } from "../blobs/blobs";
import { BackendError, Connection } from "../connection/connection";
import type { ConnectionState } from "../connection/connection";
import { addMessages, applyEvent, key, noteLatest, unread, upsertConversation } from "../conversations/model";
import type { Data } from "../conversations/model";
import { addPending, confirmPending, failPending, newMessageId } from "../conversations/pending";
import type { PendingByConversation } from "../conversations/pending";
import { sortDevices, withDevice } from "../devices/model";
import { fromHex, hex, sameId } from "../ids";
import { rememberGateway } from "../signin/gateways";
import { saved } from "./saved";
import type { Credentials } from "./saved";

const SYNC_LIMIT = 200;
const HISTORY_PAGE = 50;

export type AuthMode = "signIn" | "register";

export interface Session extends Data {
  credentials: Credentials | null;
  me: User | null;
  /** Connected means Connect completed, not only that the socket is open. */
  status: ConnectionState;
  /** The conversation on screen. */
  current: string | null;
  /** Settings on screen, in place of the conversation. */
  settings: boolean;
  pending: PendingByConversation;
  /** The account's signed-in devices, once listed. */
  devices: Device[] | null;
  error: string | null;
  /** Why the device was signed out, for the sign-in screen. */
  notice: string | null;
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
  show(conversationId: string | null): Promise<void>;
  showSettings(open: boolean): void;
  loadEarlier(): Promise<void>;
  send(text: string): Promise<void>;
  markReadIfVisible(): void;
  clearError(): void;
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
/** A sign-out this device asked for, with the notice to show when the server closes the connection for it. */
let leaving: { notice: string | null } | null = null;

/** The state with no user's data in it. */
const blank = {
  conversations: {},
  messages: {},
  me: null,
  current: null,
  settings: false,
  pending: {},
  devices: null,
} satisfies Partial<Session>;

const isVisible = () => typeof document === "undefined" || document.visibilityState === "visible";
const deviceName = () => (typeof navigator === "undefined" ? "" : navigator.userAgent.slice(0, 64));

export const useSession = create<Session>((set, get) => {
  const fail = (err: unknown) => set({ error: err instanceof Error ? err.message : String(err) });

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
    conn?.close();
    conn = null;
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
    await refreshConversations();
    await syncFromCursor();
    const current = get().current;
    if (current) await loadHistory(current, 0n);
  }

  async function refreshConversations() {
    if (!conn) return;
    const resp = await conn.call("listConversations", { case: "listConversations", value: {} });
    let data: Data = get();
    for (const c of resp.conversations) data = upsertConversation(data, c);
    set({ conversations: data.conversations });
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
    if (event.body.case === "device") {
      const { device, signedOut } = event.body.value;
      const devices = get().devices;
      if (device && devices) set({ devices: withDevice(devices, device, signedOut) });
      return;
    }
    const { data, unknownConversation } = applyEvent(get(), event, me.userId);
    const m = event.body.case === "message" ? event.body.value.message : undefined;
    const pending = m ? confirmPending(get().pending, m.conversationId, m.clientMessageId) : get().pending;
    set({ conversations: data.conversations, messages: data.messages, pending });
    if (unknownConversation) refreshConversations().catch(fail);
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
    set({ messages: addMessages(get(), id, resp.messages).messages });
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
      const c = conn;
      const address = c && blobAddress(c.url);
      if (!c || !address) throw new Error("not connected");
      const upload = await c.call("createUpload", {
        case: "createUpload",
        value: { purpose: BlobPurpose.AVATAR, mediaType: picture.type, size: BigInt(picture.size) },
      });
      // The browser sets Content-Length from the body, which is the size asked for.
      const resp = await fetch(address + upload.uploadPath, {
        method: "PUT",
        headers: { "Content-Type": picture.type },
        body: picture,
      });
      if (!resp.ok) throw new Error(`The upload failed (HTTP ${resp.status}).`);
      await get().updateProfile({ avatar: upload.blobId });
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
      await get().show(key(resp.conversation.conversationId));
    },

    async show(conversationId) {
      set({ current: conversationId, settings: false });
      if (!conversationId) return;
      if (!get().messages[conversationId]) await loadHistory(conversationId, 0n);
      get().markReadIfVisible();
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

    async send(text) {
      const id = get().current;
      if (!id || !conn) return;
      const clientMessageId = newMessageId();
      set({ pending: addPending(get().pending, id, { clientMessageId, text }) });
      try {
        const resp = await conn.call("sendMessage", {
          case: "sendMessage",
          value: { conversationId: fromHex(id), clientMessageId, text },
        });
        const m = resp.message;
        if (!m) return;
        const data = noteLatest(addMessages(get(), m.conversationId, [m]), m, true);
        set({ ...data, pending: confirmPending(get().pending, m.conversationId, m.clientMessageId) });
      } catch (err) {
        set({ pending: failPending(get().pending, id, clientMessageId) });
        throw err;
      }
    },

    markReadIfVisible() {
      const id = get().current;
      const c = id ? get().conversations[id] : undefined;
      if (!id || !c || !conn || unread(c) === 0n || !isVisible()) return;
      const seq = c.lastSeq;
      set({ conversations: { ...get().conversations, [id]: { ...c, readSeq: seq } } });
      conn.call("markRead", { case: "markRead", value: { conversationId: c.conversationId, seq } }).catch(fail);
    },

    clearError() {
      set({ error: null });
    },
  };
});
