// Sending to the current conversation: a message sealed and delivered with
// the blobs it refers to, shown pending until confirmed, and tried again
// after a failure.

import { BackendError } from "../connection/connection";
import { available } from "../e2ee/crypto";
import { NO_CRYPTO } from "../e2ee/actions";
import { staleKeys } from "../e2ee/keys";
import type { BodyInit } from "../e2ee/keys";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import type { Message } from "../gen/wrinkle/v1/model_pb";
import { isGroup } from "../groups/model";
import { fromHex } from "../ids";
import type { Api } from "../session/api";
import type { Session } from "../session/session";
import { addMessages, key, noteLatest, textKey, withStickers, withTexts } from "./model";
import type { Data } from "./model";
import { addPending, confirmPending, failPending, newMessageId, retryPending } from "./pending";

/** What a failed message shows when the peer blocked the user. */
export const REFUSED = "Can't message this user";

export function sendActions({ set, get, link, onSignOut }: Api) {
  /** Sends that failed, to try again, by client message id. */
  const retries = new Map<string, { conversationId: string; run: () => Promise<void> }>();
  onSignOut(() => retries.clear());

  /** Seals a message body and sends it, with the blobs it refers to; resolves with the backend's copy. */
  async function deliver(
    conversationId: Uint8Array,
    clientMessageId: string,
    body: BodyInit,
    blobIds: Uint8Array[] = [],
  ): Promise<Message | undefined> {
    for (let attempt = 1; ; attempt++) {
      const keys = link.keys;
      if (!keys?.ready) throw new Error(available() ? "Encryption is not ready yet." : NO_CRYPTO);
      const sealed = await keys.seal(conversationId, clientMessageId, body);
      try {
        const resp = await link.call("sendMessage", {
          case: "sendMessage",
          value: { conversationId, clientMessageId, sealed, blobIds },
        });
        return resp.message;
      } catch (err) {
        // Another device made a newer epoch, or the members changed: look again, once.
        if (attempt > 1 || !staleKeys(err)) throw err;
        keys.invalidate(key(conversationId));
      }
    }
  }

  /** Records a sent message as confirmed, with what it says. */
  function sent(m: Message, opened: (data: Data, t: string) => Data) {
    const data = opened(noteLatest(addMessages(get(), m.conversationId, [m]), m, true), textKey(m));
    set({ ...data, pending: confirmPending(get().pending, m.conversationId, m.clientMessageId) });
  }

  /** Runs a send shown pending; a failure leaves it marked failed, to try again. */
  async function attempt(conversationId: string, clientMessageId: string, run: () => Promise<void>) {
    retries.delete(clientMessageId);
    try {
      await run();
    } catch (err) {
      retries.set(clientMessageId, { conversationId, run });
      const refused =
        err instanceof BackendError &&
        err.code === ErrorCode.PERMISSION_DENIED &&
        !isGroup(get().conversations[conversationId]);
      set({ pending: failPending(get().pending, conversationId, clientMessageId, refused ? REFUSED : undefined) });
      throw err;
    }
  }

  /** Seals and sends a message to the current conversation, shown pending until confirmed. */
  async function sendBody(body: BodyInit, text: string, sticker?: string) {
    const id = get().current;
    if (!id || !link.conn) return;
    const clientMessageId = newMessageId();
    set({ pending: addPending(get().pending, id, { clientMessageId, text, sticker }) });
    await attempt(id, clientMessageId, async () => {
      const m = await deliver(fromHex(id), clientMessageId, body);
      if (m) sent(m, (data, t) => (sticker ? withStickers(data, { [t]: sticker }) : withTexts(data, { [t]: text })));
    });
  }

  const actions = {
    send(text) {
      return sendBody({ case: "text", value: text }, text);
    },

    async retrySend(clientMessageId) {
      const r = retries.get(clientMessageId);
      if (!r) return;
      set({ pending: retryPending(get().pending, r.conversationId, clientMessageId) });
      await attempt(r.conversationId, clientMessageId, r.run);
    },
  } satisfies Partial<Session>;

  return { actions, deliver, sent, attempt, sendBody };
}
