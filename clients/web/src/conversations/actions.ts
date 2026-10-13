// Conversations in the session: the list, the one on screen with its
// history, read positions, and deleting one's own messages.

import { timestampNow } from "@bufbuild/protobuf/wkt";
import type { opening } from "../e2ee/opening";
import type { groupActions } from "../groups/actions";
import { isFormer, noticeUsers } from "../groups/model";
import { fromHex } from "../ids";
import type { Api } from "../session/api";
import type { Session } from "../session/session";
import { addHistory, key, unread, upsertConversation, withDeleted, withUsers } from "./model";
import type { Data } from "./model";

const HISTORY_PAGE = 50;

export const isVisible = (): boolean => typeof document === "undefined" || document.visibilityState === "visible";

export function conversationActions(
  { set, get, link, fail, current, onSignOut }: Api,
  groups: ReturnType<typeof groupActions>,
  open: ReturnType<typeof opening>,
) {
  /** Conversations whose newest page of history was loaded since connecting; events alone may bring only the latest messages. */
  const loaded = new Set<string>();
  onSignOut(() => loaded.clear());

  async function refreshConversations() {
    if (!link.conn) return;
    const resp = await link.conn.call("listConversations", { case: "listConversations", value: {} });
    let data: Data = get();
    for (const c of resp.conversations) data = upsertConversation(data, c);
    set({ conversations: data.conversations, users: data.users });
    for (const c of resp.conversations) groups.learnSender(key(c.conversationId));
    open.openLatest();
  }

  async function loadHistory(conversationId: string, beforeSeq: bigint) {
    if (!link.conn) return;
    const id = fromHex(conversationId);
    const resp = await link.conn.call("getMessages", {
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
    open.openMessages(resp.messages);
    open.openReactions(conversationId);
  }

  const actions = {
    async openConversation(peerName) {
      if (!link.conn) return;
      const resp = await link.conn.call("openConversation", { case: "openConversation", value: { peerName } });
      if (!resp.conversation) return;
      set({ conversations: upsertConversation(get(), resp.conversation).conversations });
      open.openLatest();
      await get().show(key(resp.conversation.conversationId));
    },

    async show(conversationId) {
      set({ current: conversationId, settings: false });
      if (!conversationId) return;
      // Keys may have arrived since, as from another device of the user.
      link.keys?.retry(conversationId);
      const c = get().conversations[conversationId];
      open.openMessages([...Object.values(get().messages[conversationId] ?? {}), c?.lastMessage], true);
      open.openReactions(conversationId, true);
      if (!loaded.has(conversationId)) await loadHistory(conversationId, 0n);
      get().markReadIfVisible();
      await groups.refreshMembers(conversationId).catch(fail);
      groups
        .membersOf(fromHex(conversationId), false)
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

    async deleteMessage(seq) {
      const c = current();
      await link.call("deleteMessage", { case: "deleteMessage", value: { conversationId: c.conversationId, seq } });
      // The event follows; showing it now does not wait for it.
      const data = withDeleted(get(), c.conversationId, seq, timestampNow());
      set({ conversations: data.conversations, messages: data.messages, reactions: data.reactions });
    },

    markReadIfVisible() {
      const id = get().current;
      const c = id ? get().conversations[id] : undefined;
      if (!id || !c || !link.conn || unread(c) === 0n || !isVisible()) return;
      const seq = c.lastSeq;
      set({ conversations: { ...get().conversations, [id]: { ...c, readSeq: seq } } });
      // Former members cannot move their read position on the backend.
      if (isFormer(c)) return;
      link.conn.call("markRead", { case: "markRead", value: { conversationId: c.conversationId, seq } }).catch(fail);
    },
  } satisfies Partial<Session>;

  return { actions, refreshConversations, loadHistory };
}
