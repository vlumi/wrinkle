// Wrinkle web client: one device, speaking the contract over a WebSocket.
// The .proto files are loaded at runtime, so this tracks the contract
// without a build step. Serve the repository root (make web) so that
// /proto/ is reachable.

"use strict";

const PROTO_ROOT = "/proto/";
const SYNC_LIMIT = 200;
const HISTORY_PAGE = 50;
const RECONNECT_MIN_MS = 500;
const RECONNECT_MAX_MS = 10000;

// Contract types, filled by loadContract.
const T = {};

async function loadContract() {
  const root = new protobuf.Root();
  root.resolvePath = (origin, target) =>
    target.startsWith("google/protobuf/") ? target : PROTO_ROOT + target;
  await root.load(["wrinkle/v1/frame.proto"], { keepCase: true });
  T.Frame = root.lookupType("wrinkle.v1.Frame");
  T.ErrorCode = root.lookupEnum("wrinkle.v1.ErrorCode");
}

// Decoded messages use strings for 64-bit integers, so ids survive.
const DECODE = { longs: String, enums: String, defaults: true, oneofs: true };

// ---- connection ---------------------------------------------------------

class Connection {
  constructor(url, handlers) {
    this.url = url;
    this.handlers = handlers;
    this.ws = null;
    this.nextId = 0;
    this.pending = new Map();
    this.closed = false;
    this.backoff = RECONNECT_MIN_MS;
  }

  open() {
    this.closed = false;
    this.handlers.state("connecting");
    const ws = new WebSocket(this.url);
    ws.binaryType = "arraybuffer";
    ws.onopen = () => {
      this.backoff = RECONNECT_MIN_MS;
      this.handlers.state("connected");
      this.handlers.open();
    };
    ws.onmessage = (e) => this.receive(new Uint8Array(e.data));
    ws.onclose = () => {
      this.ws = null;
      for (const p of this.pending.values()) p.reject(new Error("connection closed"));
      this.pending.clear();
      this.handlers.state("offline");
      if (!this.closed) {
        setTimeout(() => this.open(), this.backoff);
        this.backoff = Math.min(this.backoff * 2, RECONNECT_MAX_MS);
      }
    };
    ws.onerror = () => {};
    this.ws = ws;
  }

  close() {
    this.closed = true;
    if (this.ws) this.ws.close();
  }

  send(frame) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) throw new Error("not connected");
    this.ws.send(T.Frame.encode(T.Frame.fromObject(frame)).finish());
  }

  // call sends a request and resolves with the response body of that kind.
  call(body) {
    const id = ++this.nextId;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      try {
        this.send({ request: { id, ...body } });
      } catch (err) {
        this.pending.delete(id);
        reject(err);
      }
    });
  }

  receive(bytes) {
    const frame = T.Frame.toObject(T.Frame.decode(bytes), DECODE);
    switch (frame.kind) {
      case "response": {
        const r = frame.response;
        const p = this.pending.get(Number(r.request_id));
        if (!p) return;
        this.pending.delete(Number(r.request_id));
        if (r.body === "error") {
          const err = new Error(r.error.message);
          err.code = r.error.code;
          p.reject(err);
        } else {
          p.resolve(r[r.body]);
        }
        break;
      }
      case "event":
        this.handlers.event(frame.event);
        break;
      case "ping":
        this.send({ pong: {} });
        break;
      default:
        break;
    }
  }
}

// ---- state --------------------------------------------------------------

const state = {
  name: null,
  me: null,
  deviceId: "0",
  cursor: "0",
  conn: null,
  conversations: new Map(), // id -> Conversation
  messages: new Map(), // conversation id -> Map(seq -> Message)
  pending: new Map(), // conversation id -> [optimistic messages]
  current: null,
};

const store = {
  get: (key) => localStorage.getItem(`wrinkle.${state.name}.${key}`),
  set: (key, value) => localStorage.setItem(`wrinkle.${state.name}.${key}`, value),
};

// Cursors and sequence numbers are strings; compare them as integers.
const big = (s) => BigInt(s || 0);
const lt = (a, b) => big(a) < big(b);

function messagesOf(id) {
  if (!state.messages.has(id)) state.messages.set(id, new Map());
  return state.messages.get(id);
}

// ---- protocol flow ------------------------------------------------------

async function onOpen() {
  const resp = await state.conn.call({
    connect: { user_name: state.name, device_id: state.deviceId, device_name: navigator.userAgent.slice(0, 64) },
  });
  state.me = resp.user;
  state.deviceId = resp.device_id;
  store.set("device", resp.device_id);
  if (state.cursor === "0") {
    // A device without history starts at the head: the past comes from
    // the conversation list and history requests.
    state.cursor = resp.log_head;
    store.set("cursor", state.cursor);
  }
  ui.me.textContent = `${state.me.name} · device ${state.deviceId.slice(-4)}`;
  await refreshConversations();
  await syncFromCursor();
  if (state.current) await showConversation(state.current, true);
}

async function refreshConversations() {
  const resp = await state.conn.call({ list_conversations: {} });
  for (const c of resp.conversations || []) state.conversations.set(c.conversation_id, c);
  renderList();
}

async function syncFromCursor() {
  for (;;) {
    const resp = await state.conn.call({ sync: { cursor: state.cursor, limit: SYNC_LIMIT } });
    for (const ev of resp.events || []) applyEvent(ev);
    state.cursor = resp.cursor;
    store.set("cursor", state.cursor);
    if (!resp.has_more) break;
  }
  state.conn.send({ ack: { cursor: state.cursor } });
}

function onEvent(ev) {
  // Pushes and sync overlap; the cursor tells them apart.
  if (!lt(state.cursor, ev.cursor)) return;
  if (big(ev.cursor) !== big(state.cursor) + 1n) {
    // A gap: let sync fill it instead.
    syncFromCursor().catch(console.error);
    return;
  }
  applyEvent(ev);
  state.cursor = ev.cursor;
  store.set("cursor", state.cursor);
  state.conn.send({ ack: { cursor: state.cursor } });
}

function applyEvent(ev) {
  switch (ev.body) {
    case "message": {
      const m = ev.message.message;
      messagesOf(m.conversation_id).set(m.seq, m);
      dropPending(m);
      let c = state.conversations.get(m.conversation_id);
      if (!c) {
        // First message of a conversation the peer opened.
        refreshConversations().catch(console.error);
        return;
      }
      if (lt(c.last_seq, m.seq)) {
        c.last_seq = m.seq;
        c.last_message = m;
      }
      renderList();
      if (state.current === m.conversation_id) {
        renderMessages();
        markReadIfVisible();
      }
      break;
    }
    case "read": {
      const r = ev.read;
      const c = state.conversations.get(r.conversation_id);
      if (!c) return;
      if (r.user_id === state.me.user_id) {
        if (lt(c.read_seq, r.seq)) c.read_seq = r.seq;
      } else if (lt(c.peer_read_seq, r.seq)) {
        c.peer_read_seq = r.seq;
      }
      renderList();
      if (state.current === r.conversation_id) renderMessages();
      break;
    }
    default:
      break;
  }
}

async function openConversation(peerName) {
  const resp = await state.conn.call({ open_conversation: { peer_user_name: peerName } });
  const c = resp.conversation;
  state.conversations.set(c.conversation_id, c);
  renderList();
  await showConversation(c.conversation_id);
}

async function showConversation(id, reload = false) {
  state.current = id;
  const c = state.conversations.get(id);
  ui.peerName.textContent = c.peer.name;
  ui.conversation.hidden = false;
  ui.empty.hidden = true;
  renderList();
  const known = messagesOf(id);
  if (reload || known.size === 0) {
    const resp = await state.conn.call({ get_messages: { conversation_id: id, before_seq: "0", limit: HISTORY_PAGE } });
    for (const m of resp.messages || []) known.set(m.seq, m);
  }
  renderMessages();
  markReadIfVisible();
  ui.text.focus();
}

async function loadEarlier() {
  const id = state.current;
  const known = messagesOf(id);
  const oldest = [...known.keys()].sort((a, b) => Number(big(a) - big(b)))[0];
  if (!oldest) return;
  const resp = await state.conn.call({ get_messages: { conversation_id: id, before_seq: oldest, limit: HISTORY_PAGE } });
  for (const m of resp.messages || []) known.set(m.seq, m);
  renderMessages(true);
}

function markReadIfVisible() {
  const c = state.conversations.get(state.current);
  if (!c || !document.hasFocus() || !lt(c.read_seq, c.last_seq)) return;
  const seq = c.last_seq;
  c.read_seq = seq;
  renderList();
  state.conn.call({ mark_read: { conversation_id: c.conversation_id, seq } }).catch(console.error);
}

async function sendMessage(text) {
  const id = state.current;
  const optimistic = {
    conversation_id: id,
    client_message_id: crypto.randomUUID(),
    sender_user_id: state.me.user_id,
    text,
    pending: true,
  };
  if (!state.pending.has(id)) state.pending.set(id, []);
  state.pending.get(id).push(optimistic);
  renderMessages();
  try {
    const resp = await state.conn.call({
      send_message: { conversation_id: id, client_message_id: optimistic.client_message_id, text },
    });
    messagesOf(id).set(resp.message.seq, resp.message);
    dropPending(resp.message);
    const c = state.conversations.get(id);
    if (lt(c.last_seq, resp.message.seq)) {
      c.last_seq = resp.message.seq;
      c.last_message = resp.message;
      c.read_seq = resp.message.seq;
    }
    renderList();
    renderMessages();
  } catch (err) {
    optimistic.failed = true;
    renderMessages();
    throw err;
  }
}

function dropPending(m) {
  const list = state.pending.get(m.conversation_id);
  if (!list) return;
  const i = list.findIndex((p) => p.client_message_id === m.client_message_id);
  if (i >= 0) list.splice(i, 1);
}

// ---- rendering ----------------------------------------------------------

const ui = {};

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") e.className = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v);
  }
  e.append(...children);
  return e;
}

function renderList() {
  const items = [...state.conversations.values()].sort((a, b) => {
    const at = a.last_message ? a.last_message.sent_at : null;
    const bt = b.last_message ? b.last_message.sent_at : null;
    return (bt ? Number(big(bt.seconds)) : 0) - (at ? Number(big(at.seconds)) : 0);
  });
  ui.list.replaceChildren(
    ...items.map((c) => {
      const unread = big(c.last_seq) - big(c.read_seq);
      const last = c.last_message
        ? `${c.last_message.sender_user_id === state.me.user_id ? "You: " : ""}${c.last_message.text}`
        : "No messages yet";
      return el(
        "li",
        { class: c.conversation_id === state.current ? "current" : "", onclick: () => showConversation(c.conversation_id) },
        el("div", { class: "peer" }, c.peer.name, unread > 0n ? el("span", { class: "unread" }, String(unread)) : ""),
        el("div", { class: "last" }, last),
      );
    }),
  );
}

function renderMessages(keepScroll = false) {
  const c = state.conversations.get(state.current);
  const known = messagesOf(state.current);
  const ordered = [...known.values()].sort((a, b) => Number(big(a.seq) - big(b.seq)));
  const pending = state.pending.get(state.current) || [];
  const oldest = ordered.length ? ordered[0].seq : "0";
  ui.earlier.hidden = ordered.length === 0 || !lt("1", oldest);
  const atBottom = ui.messages.scrollTop + ui.messages.clientHeight >= ui.messages.scrollHeight - 20;
  const before = ui.messages.scrollHeight;
  ui.messages.replaceChildren(
    ...ordered.map((m) => {
      const mine = m.sender_user_id === state.me.user_id;
      const time = new Date(Number(big(m.sent_at.seconds)) * 1000).toLocaleTimeString([], { timeStyle: "short" });
      const read = mine && !lt(c.peer_read_seq, m.seq) ? " · read" : "";
      return el("li", { class: mine ? "mine" : "" }, m.text, el("span", { class: "meta" }, `${time}${read}`));
    }),
    ...pending.map((m) => el("li", { class: "mine pending" }, m.text, el("span", { class: "meta" }, m.failed ? "failed" : "sending…"))),
  );
  if (keepScroll) ui.messages.scrollTop = ui.messages.scrollHeight - before;
  else if (atBottom || pending.length) ui.messages.scrollTop = ui.messages.scrollHeight;
}

// ---- wiring -------------------------------------------------------------

function signIn(name, url) {
  if (state.conn) state.conn.close();
  state.conversations.clear();
  state.messages.clear();
  state.pending.clear();
  state.current = null;
  state.me = null;
  state.name = name;
  state.deviceId = store.get("device") || "0";
  state.cursor = store.get("cursor") || "0";
  localStorage.setItem("wrinkle.last", JSON.stringify({ name, url }));
  ui.signin.hidden = true;
  ui.app.hidden = false;
  ui.signout.hidden = false;
  state.conn = new Connection(url, {
    state: (s) => {
      ui.status.textContent = s;
      ui.status.dataset.state = s;
    },
    open: () => onOpen().catch((err) => alert(`connect failed: ${err.message}`)),
    event: onEvent,
  });
  state.conn.open();
}

window.addEventListener("DOMContentLoaded", async () => {
  for (const id of ["status", "me", "signin", "signout", "app", "list", "conversation", "empty", "earlier", "messages", "text", "peerName"]) {
    ui[id] = document.getElementById({ list: "conversations", peerName: "peer-name" }[id] || id);
  }
  try {
    await loadContract();
  } catch (err) {
    alert(`could not load the contract from ${PROTO_ROOT}: ${err.message}`);
    return;
  }
  const last = JSON.parse(localStorage.getItem("wrinkle.last") || "null");
  if (last) {
    document.getElementById("name").value = last.name;
    document.getElementById("url").value = last.url;
  }
  ui.signout.addEventListener("click", () => {
    localStorage.removeItem("wrinkle.last");
    location.reload();
  });
  document.getElementById("signin-form").addEventListener("submit", (e) => {
    e.preventDefault();
    signIn(document.getElementById("name").value.trim(), document.getElementById("url").value.trim());
  });
  document.getElementById("open-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const input = document.getElementById("peer");
    openConversation(input.value.trim())
      .then(() => (input.value = ""))
      .catch((err) => alert(err.message));
  });
  document.getElementById("send-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const text = ui.text.value.trim();
    if (!text) return;
    ui.text.value = "";
    sendMessage(text).catch((err) => alert(err.message));
  });
  ui.earlier.addEventListener("click", () => loadEarlier().catch(console.error));
  window.addEventListener("focus", markReadIfVisible);
  // Resume the last session; sign out to switch users.
  if (last) signIn(last.name, last.url);
});
