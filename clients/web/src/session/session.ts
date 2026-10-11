// The device's session: sign-in, the cursor into the user's log, and the
// flow of events into the model. Connection details live outside React
// state; the screens read the store.

import { create } from "zustand";
import type { Event } from "../gen/wrinkle/v1/event_pb";
import type { User } from "../gen/wrinkle/v1/model_pb";
import { Connection } from "../connection/connection";
import type { ConnectionState } from "../connection/connection";
import { addMessages, applyEvent, key, noteLatest, unread, upsertConversation } from "../conversations/model";
import type { Data } from "../conversations/model";
import { addPending, confirmPending, failPending, newMessageId } from "../conversations/pending";
import type { PendingByConversation } from "../conversations/pending";
import { rememberGateway } from "../signin/gateways";
import { saved } from "./saved";

const SYNC_LIMIT = 200;
const HISTORY_PAGE = 50;

export interface Session extends Data {
  name: string | null;
  me: User | null;
  status: ConnectionState;
  /** The conversation on screen. */
  current: string | null;
  pending: PendingByConversation;
  error: string | null;
  signIn(name: string, url: string): void;
  signOut(): void;
  openConversation(peerName: string): Promise<void>;
  show(conversationId: string | null): Promise<void>;
  loadEarlier(): Promise<void>;
  send(text: string): Promise<void>;
  markReadIfVisible(): void;
  clearError(): void;
}

let conn: Connection | null = null;
let cursor = 0n;
let deviceId = 0n;

/** The state with no user's data in it. */
const blank = { conversations: {}, messages: {}, me: null, current: null, pending: {} } satisfies Partial<Session>;

const isVisible = () => typeof document === "undefined" || document.visibilityState === "visible";

export const useSession = create<Session>((set, get) => {
  const fail = (err: unknown) => set({ error: err instanceof Error ? err.message : String(err) });

  function saveCursor(next: bigint) {
    cursor = next;
    const name = get().name;
    if (name) saved.setCursor(name, cursor);
  }

  async function onOpen() {
    const c = conn;
    const name = get().name;
    if (!c || !name) return;
    const resp = await c.call("connect", {
      case: "connect",
      value: { userName: name, deviceId, deviceName: navigator.userAgent.slice(0, 64) },
    });
    deviceId = resp.deviceId;
    rememberGateway(c.url);
    saved.setDeviceId(name, deviceId);
    if (cursor === 0n) {
      // A device without history starts at the head; the past comes from the
      // conversation list and history requests.
      saveCursor(resp.logHead);
    }
    set({ me: resp.user ?? null });
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
    const resp = await conn.call("getMessages", {
      case: "getMessages",
      value: { conversationId: BigInt(conversationId), beforeSeq, limit: HISTORY_PAGE },
    });
    set({ messages: addMessages(get(), BigInt(conversationId), resp.messages).messages });
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
    name: null,
    status: "offline",
    error: null,

    signIn(name, url) {
      conn?.close();
      set({ ...blank, name, error: null });
      deviceId = saved.deviceId(name);
      cursor = saved.cursor(name);
      saved.setLastSession({ name, url });
      conn = new Connection(url, {
        state: (status) => set({ status }),
        open: () => onOpen().catch(fail),
        event: onEvent,
      });
      conn.open();
    },

    signOut() {
      conn?.close();
      conn = null;
      saved.forgetLastSession();
      set({ ...blank, name: null, status: "offline" });
    },

    async openConversation(peerName) {
      if (!conn) return;
      const resp = await conn.call("openConversation", { case: "openConversation", value: { peerUserName: peerName } });
      if (!resp.conversation) return;
      set({ conversations: upsertConversation(get(), resp.conversation).conversations });
      await get().show(key(resp.conversation.conversationId));
    },

    async show(conversationId) {
      set({ current: conversationId });
      if (!conversationId) return;
      if (!get().messages[conversationId]) await loadHistory(conversationId, 0n);
      get().markReadIfVisible();
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
          value: { conversationId: BigInt(id), clientMessageId, text },
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
