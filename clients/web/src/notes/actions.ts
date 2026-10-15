// Notes in the session: opening them, which makes them on the backend the
// first time.

import { key, upsertConversation } from "../conversations/model";
import type { opening } from "../e2ee/opening";
import type { Api } from "../session/api";
import type { Session } from "../session/session";

export function notesActions({ set, get, link }: Api, open: ReturnType<typeof opening>) {
  const actions = {
    async openNotes() {
      const resp = await link.call("openNotes", { case: "openNotes", value: {} });
      if (!resp.conversation) return;
      set({ conversations: upsertConversation(get(), resp.conversation).conversations });
      open.openLatest();
      await get().show(key(resp.conversation.conversationId));
    },
  } satisfies Partial<Session>;

  return { actions };
}
