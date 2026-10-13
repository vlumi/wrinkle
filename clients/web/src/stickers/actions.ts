// Stickers in the session: the user's palette, loaded on connect and kept
// as the user changes it, and sending one.

import { upload } from "../blobs/upload";
import { STICKER } from "../conversations/model";
import type { sendActions } from "../conversations/send";
import { BlobPurpose } from "../gen/wrinkle/v1/messaging_pb";
import { fromHex, hex } from "../ids";
import type { Api } from "../session/api";
import type { Session } from "../session/session";
import { moved, withoutSticker, withSaved } from "./model";

export function stickerActions({ set, get, link }: Api, send: ReturnType<typeof sendActions>) {
  async function refreshPalette() {
    if (!link.conn) return;
    const resp = await link.conn.call("getPalette", { case: "getPalette", value: {} });
    set({ palette: resp.stickers.map((st) => hex(st.stickerId)) });
  }

  const actions = {
    sendSticker(stickerId) {
      return send.sendBody({ case: "sticker", value: { stickerId: fromHex(stickerId) } }, STICKER, stickerId);
    },

    async createSticker(picture) {
      const blobId = await upload(link.conn, BlobPurpose.STICKER, picture);
      await link.call("createSticker", { case: "createSticker", value: { blobId } });
      // The palette event follows; showing it now does not wait for it.
      const palette = get().palette;
      if (palette) set({ palette: withSaved(palette, hex(blobId)) });
    },

    async saveSticker(stickerId) {
      await link.call("saveSticker", { case: "saveSticker", value: { stickerId: fromHex(stickerId) } });
      const palette = get().palette;
      if (palette) set({ palette: withSaved(palette, stickerId) });
    },

    async removeSticker(stickerId) {
      await link.call("removeSticker", { case: "removeSticker", value: { stickerId: fromHex(stickerId) } });
      const palette = get().palette;
      if (palette) set({ palette: withoutSticker(palette, stickerId) });
    },

    async moveSticker(stickerId, before) {
      await link.call("moveSticker", {
        case: "moveSticker",
        value: { stickerId: fromHex(stickerId), beforeStickerId: fromHex(before) },
      });
      const palette = get().palette;
      if (palette) set({ palette: moved(palette, stickerId, before) });
    },
  } satisfies Partial<Session>;

  return { actions, refreshPalette };
}
