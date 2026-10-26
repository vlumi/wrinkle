// Reacting to a message in the current conversation: the user's reactions
// sealed together and set, shown before the event confirms them.

import { create as createMessage } from "@bufbuild/protobuf";
import { key, textKey } from "../conversations/model";
import { NO_CRYPTO } from "../e2ee/actions";
import { available } from "../e2ee/crypto";
import { staleKeys } from "../e2ee/keys";
import { timestampNow } from "@bufbuild/protobuf/wkt";
import { SealedSchema } from "../gen/wrinkle/v1/model_pb";
import type { Api } from "../session/api";
import type { Session } from "../session/session";
import { ownEmoji, toggled, withReaction } from "./model";

export function reactionActions({ set, get, link, current }: Api) {
  const actions = {
    async react(seq, emoji) {
      const c = current();
      const conversationId = c.conversationId;
      const id = key(conversationId);
      const messageKey = textKey({ conversationId, seq });
      const next = toggled(ownEmoji(get().reactions[messageKey], get().me?.userId), emoji);
      if (!next) return;
      for (let attempt = 1; ; attempt++) {
        const keys = link.keys;
        if (!keys?.ready) throw new Error(available() ? "Encryption is not ready yet." : NO_CRYPTO);
        const sealed = next.length > 0 ? await keys.sealReactions(conversationId, seq, next) : undefined;
        try {
          await link.call("setReaction", { case: "setReaction", value: { conversationId, seq, sealed } });
          // The event follows; showing it now does not wait for it.
          const me = get().me;
          if (me) {
            const s = sealed && createMessage(SealedSchema, sealed);
            set({ reactions: withReaction(get().reactions, messageKey, me.userId, s, timestampNow(), next) });
          }
          return;
        } catch (err) {
          // Another device made a newer epoch, or the members changed: look again, once.
          if (attempt > 1 || !sealed || !staleKeys(err)) throw err;
          keys.invalidate(id);
        }
      }
    },
  } satisfies Partial<Session>;

  return { actions };
}
