// The stickers feature: stickers and the user's palette.
import type { FeatureFactory } from "../services/features.ts";
import { move, palette, remove, save } from "./palette.ts";
import { createSticker, getStickers } from "./stickers.ts";

export const stickers: FeatureFactory = (deps) => ({
  handlers: {
    createSticker: async (caller, req) => ({
      case: "createSticker",
      value: {
        $typeName: "wrinkle.v1.CreateStickerResponse",
        sticker: await createSticker(deps, caller.userId, req.blobId),
      },
    }),
    getStickers: async (_caller, req) => ({
      case: "getStickers",
      value: { $typeName: "wrinkle.v1.GetStickersResponse", stickers: await getStickers(deps, req.stickerIds) },
    }),
    getPalette: async (caller) => ({
      case: "getPalette",
      value: { $typeName: "wrinkle.v1.GetPaletteResponse", stickers: await palette(deps, caller.userId) },
    }),
    saveSticker: async (caller, req) => {
      await save(deps, caller.userId, req.stickerId);
      return { case: "saveSticker", value: { $typeName: "wrinkle.v1.SaveStickerResponse" } };
    },
    removeSticker: async (caller, req) => {
      await remove(deps, caller.userId, req.stickerId);
      return { case: "removeSticker", value: { $typeName: "wrinkle.v1.RemoveStickerResponse" } };
    },
    moveSticker: async (caller, req) => {
      await move(deps, caller.userId, req.stickerId, req.beforeStickerId);
      return { case: "moveSticker", value: { $typeName: "wrinkle.v1.MoveStickerResponse" } };
    },
  },
});
