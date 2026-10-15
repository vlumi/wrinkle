// Groups in the session: creating one, listing its members again when they
// change, and what the user does to the group on screen.

import { key, upsertConversation, withMembers } from "../conversations/model";
import { hex, sameId } from "../ids";
import { isNotes } from "../notes/model";
import type { Api } from "../session/api";
import type { Session } from "../session/session";
import { isGroup } from "./model";

export function groupActions({ set, get, link, fail, current }: Api) {
  /** Groups listing members right now, so each is asked once at a time. */
  const listing = new Set<string>();

  /** Lists a group's members, if the user is still in it. */
  async function refreshMembers(conversationId: string) {
    const c = get().conversations[conversationId];
    if (!link.conn || !c || !isGroup(c) || !c.member) return;
    listing.add(conversationId);
    let resp;
    try {
      resp = await link.conn.call("getMembers", { case: "getMembers", value: { conversationId: c.conversationId } });
    } finally {
      listing.delete(conversationId);
    }
    const data = withMembers(get(), c.conversationId, resp.members);
    set({ conversations: data.conversations, members: data.members, users: data.users });
  }

  /** Lists a group's members when its latest message is from someone not known yet, for the list's preview. */
  function learnSender(conversationId: string) {
    const { conversations, users, members, me } = get();
    const c = conversations[conversationId];
    const m = c?.lastMessage;
    if (!m || !isGroup(c) || sameId(m.senderUserId, me?.userId) || users[hex(m.senderUserId)]) return;
    if (members[conversationId] || listing.has(conversationId)) return;
    refreshMembers(conversationId).catch(fail);
  }

  /** The conversation's current members, for the key epochs: the user alone in the notes. */
  async function membersOf(conversationId: Uint8Array, fresh: boolean): Promise<Uint8Array[]> {
    const id = key(conversationId);
    const c = get().conversations[id];
    const me = get().me;
    if (!c || !me) throw new Error("unknown conversation");
    if (isNotes(c)) return [me.userId];
    if (!isGroup(c)) return c.peer ? [me.userId, c.peer.userId] : [me.userId];
    if (fresh || !get().members[id]) await refreshMembers(id);
    return (get().members[id] ?? []).flatMap((m) => (m.user ? [m.user.userId] : []));
  }

  const actions = {
    async createGroup(topic, memberNames, clientGroupId) {
      if (!link.conn) throw new Error("not connected");
      const resp = await link.conn.call("createGroup", {
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
      await link.call("addMembers", { case: "addMembers", value: { conversationId: c.conversationId, memberNames } });
      await refreshMembers(key(c.conversationId));
    },

    async removeMember(userId) {
      const c = current();
      await link.call("removeMember", { case: "removeMember", value: { conversationId: c.conversationId, userId } });
      await refreshMembers(key(c.conversationId));
    },

    async setAdmin(userId, admin) {
      const c = current();
      await link.call("setAdmin", { case: "setAdmin", value: { conversationId: c.conversationId, userId, admin } });
      await refreshMembers(key(c.conversationId));
    },

    async setTopic(topic) {
      const c = current();
      await link.call("setTopic", { case: "setTopic", value: { conversationId: c.conversationId, topic } });
    },

    async leaveGroup() {
      const c = current();
      await link.call("leaveGroup", { case: "leaveGroup", value: { conversationId: c.conversationId } });
    },
  } satisfies Partial<Session>;

  return { actions, refreshMembers, learnSender, membersOf };
}
