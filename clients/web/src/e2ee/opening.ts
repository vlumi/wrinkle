// Opening what arrives sealed into the model: messages into their texts,
// stickers and attachments, and reactions into their emoji.

import { isDeleted, textKey, withAttachments, withStickers, withTexts } from "../conversations/model";
import { isValid } from "../attachments/model";
import type { Message } from "../gen/wrinkle/v1/model_pb";
import { fromHex, hex } from "../ids";
import { cleaned, withOpened } from "../reactions/model";
import type { Api } from "../session/api";

export function opening({ set, get, link, onSignOut }: Api) {
  /** Messages being opened, by textKey. */
  const messages = new Set<string>();
  /** Reactions being opened, by textKey, user and nonce. */
  const reactions = new Set<string>();
  onSignOut(() => {
    messages.clear();
    reactions.clear();
  });

  /** Opens sealed messages not opened yet; again, those that could not be opened before. */
  function openMessages(list: (Message | undefined)[], again = false) {
    const k = link.keys;
    if (!k?.ready) return;
    for (const m of list) {
      if (!m || m.content.case !== "sealed" || isDeleted(m)) continue;
      const t = textKey(m);
      const known = get().texts[t];
      if (messages.has(t) || (known !== undefined && !(again && known === null))) continue;
      messages.add(t);
      k.open(m)
        .then((body) => {
          if (link.keys !== k || (body === null && get().texts[t] != null)) return;
          if (body?.case === "sticker") set(withStickers(get(), { [t]: hex(body.value.stickerId) }));
          else if (body?.case === "attachments" && isValid(body.value))
            set(withAttachments(get(), { [t]: body.value }));
          else set(withTexts(get(), { [t]: body?.case === "text" ? body.value : null }));
        })
        .catch(() => {
          // Not reachable now: tried again on the next look.
        })
        .finally(() => messages.delete(t));
    }
  }

  /** Opens the reactions of the conversation not opened yet; again, those that could not be opened before. */
  function openReactions(conversationId: string, again = false) {
    const k = link.keys;
    if (!k?.ready) return;
    const prefix = conversationId + "/";
    for (const [messageKey, users] of Object.entries(get().reactions)) {
      if (!messageKey.startsWith(prefix)) continue;
      const seq = BigInt(messageKey.slice(prefix.length));
      for (const [user, e] of Object.entries(users)) {
        if (e.emoji !== undefined && !(again && e.emoji === null)) continue;
        const sealed = e.sealed;
        const o = `${messageKey}/${user}/${hex(sealed.nonce)}`;
        if (reactions.has(o)) continue;
        reactions.add(o);
        k.openReactions(fromHex(conversationId), seq, fromHex(user), sealed)
          .then((emoji) => {
            if (link.keys !== k) return;
            const opened = emoji && cleaned(emoji);
            set({ reactions: withOpened(get().reactions, messageKey, user, sealed, opened) });
          })
          .catch(() => {
            // Not reachable now: tried again on the next look.
          })
          .finally(() => reactions.delete(o));
      }
    }
  }

  function openLatest() {
    openMessages(Object.values(get().conversations).map((c) => c.lastMessage));
  }

  return { openMessages, openReactions, openLatest };
}
