// The flow of events into the model: synced from the cursor on connect and
// after a gap, pushed over the connection in between.

import { BackendError } from "../connection/connection";
import type { contactActions } from "../contacts/actions";
import type { conversationActions } from "../conversations/actions";
import { applyEvent, emptyData, key } from "../conversations/model";
import { confirmPending } from "../conversations/pending";
import { withDevice } from "../devices/model";
import type { opening } from "../e2ee/opening";
import type { sharingActions } from "../e2ee/sharing";
import type { Event } from "../gen/wrinkle/v1/event_pb";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import type { groupActions } from "../groups/actions";
import { hex, sameId } from "../ids";
import type { Api } from "./api";

const SYNC_LIMIT = 200;

export function sync(
  { set, get, link, fail }: Api,
  conversations: ReturnType<typeof conversationActions>,
  groups: ReturnType<typeof groupActions>,
  open: ReturnType<typeof opening>,
  sharing: ReturnType<typeof sharingActions>,
  contacts: ReturnType<typeof contactActions>,
) {
  async function syncFromCursor() {
    if (!link.conn) return;
    for (;;) {
      let resp;
      try {
        resp = await link.conn.call("sync", { case: "sync", value: { cursor: link.cursor, limit: SYNC_LIMIT } });
      } catch (err) {
        if (err instanceof BackendError && err.code === ErrorCode.CURSOR_EXPIRED) {
          startOver();
          return;
        }
        throw err;
      }
      for (const ev of resp.events) apply(ev);
      link.saveCursor(resp.cursor);
      if (!resp.hasMore) break;
    }
    link.conn.ack(link.cursor);
    get().markReadIfVisible();
  }

  // The log no longer reaches back to the cursor: everything held may be
  // stale, so the device starts again as one without history, from the
  // head on the next connect, which lists and loads it all again.
  function startOver() {
    link.saveCursor(0n);
    set({ ...emptyData(), complete: {} });
    link.conn?.restart();
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
    if (event.body.case === "contact") {
      if (event.body.value.contact) contacts.apply(event.body.value.contact);
      return;
    }
    if (event.body.case === "profile" && event.body.value.user) contacts.profileChanged(event.body.value.user);
    if (event.body.case === "device") {
      const { device, signedOut } = event.body.value;
      // Others' devices reach the user too, with only their keys.
      const owner = event.body.value.userId.length > 0 ? event.body.value.userId : me.userId;
      const devices = get().devices;
      if (device && devices && sameId(owner, me.userId)) set({ devices: withDevice(devices, device, signedOut) });
      if (device) sharing.deviceChanged(owner, device, signedOut);
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
    if (event.body.case === "reaction") open.openReactions(key(event.body.value.conversationId));
    if (unknownConversation) conversations.refreshConversations().catch(fail);
    if (m) {
      const id = key(m.conversationId);
      // A membership change needs a new epoch before the next send.
      if (membersChanged) link.keys?.invalidate(id);
      if (membersChanged && (get().members[id] || get().current === id)) groups.refreshMembers(id).catch(fail);
      else groups.learnSender(id);
      open.openMessages([m]);
    }
  }

  function onEvent(event: Event) {
    // Pushes and sync overlap; the cursor tells them apart. A gap means
    // something was missed: sync fills it.
    if (event.cursor <= link.cursor) return;
    if (event.cursor !== link.cursor + 1n) {
      syncFromCursor().catch(fail);
      return;
    }
    apply(event);
    link.saveCursor(event.cursor);
    link.conn?.ack(link.cursor);
    get().markReadIfVisible();
  }

  return { syncFromCursor, onEvent };
}
