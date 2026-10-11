// A chosen picture made into a sticker: the center square, at most 512
// pixels a side, as PNG or WebP, whichever is smaller, under the contract's
// 256 KiB limit; smaller sides until it fits.

import { encode, scaled, squareCanvas } from "../account/picture";

export const STICKER_SIDE = 512;
export const STICKER_MAX_BYTES = 256 * 1024;
/** Fractions of the largest side to try, until one fits. */
const SCALES = [1, 0.75, 0.5, 0.375, 0.25];

/** The sides to try for a square of the given side, largest first. */
export function stickerSides(side: number): number[] {
  const largest = Math.min(side, STICKER_SIDE);
  return [...new Set(SCALES.map((s) => Math.max(1, Math.round(largest * s))))];
}

/** The smallest of the encodings that came out as asked. */
export function smallest(blobs: (Blob | null)[], types: string[]): Blob | null {
  return blobs.reduce<Blob | null>((best, b) => {
    if (!b || !types.includes(b.type)) return best;
    return !best || b.size < best.size ? b : best;
  }, null);
}

export async function prepareSticker(file: Blob): Promise<Blob> {
  const largest = await squareCanvas(file, STICKER_SIDE);
  for (const size of stickerSides(largest.width)) {
    const canvas = size === largest.width ? largest : scaled(largest, 0, 0, largest.width, size);
    // A browser that cannot encode WebP gives PNG, which then counts once.
    const blob = smallest(
      [await encode(canvas, "image/png"), await encode(canvas, "image/webp", 0.9)],
      ["image/png", "image/webp"],
    );
    if (blob && blob.size <= STICKER_MAX_BYTES) return blob;
  }
  throw new Error("The picture could not be made small enough for a sticker.");
}
