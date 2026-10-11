// Chosen files made ready to send: images scaled to at most 2048 pixels a
// side as JPEG, with a thumbnail in the room the content leaves; other
// files, GIFs among them, go as they are.

import { encode } from "../account/picture";
import type { Bytes } from "../e2ee/crypto";
import { fitted, IMAGE_QUALITY, IMAGE_SIDE, isResizable, THUMBNAIL_SIDE } from "./model";

export interface Prepared {
  /** The bytes to encrypt and send. */
  bytes: Bytes;
  mediaType: string;
  /** Images only; 0 otherwise. */
  width: number;
  height: number;
  /** A JPEG preview, empty when there is none or no room for one. */
  thumbnail: Uint8Array;
}

/** Thumbnail sides and qualities to try, best first, until one fits. */
const THUMBNAIL_SCALES = [1, 0.75, 0.5, 0.35, 0.25];
const THUMBNAIL_QUALITIES = [0.7, 0.5, 0.35];

function draw(source: CanvasImageSource, width: number, height: number): HTMLCanvasElement {
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("Pictures cannot be drawn in this browser.");
  // JPEG has no transparency: show it on white.
  ctx.fillStyle = "#fff";
  ctx.fillRect(0, 0, width, height);
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(source, 0, 0, width, height);
  return canvas;
}

const bytesOf = async (blob: Blob): Promise<Bytes> => new Uint8Array(await blob.arrayBuffer());

/** The largest JPEG preview of the image within the budget; empty if none fits. */
async function thumbnail(source: CanvasImageSource, width: number, height: number, budget: number) {
  if (budget <= 0) return new Uint8Array();
  for (const scale of THUMBNAIL_SCALES) {
    const size = fitted(width, height, Math.round(THUMBNAIL_SIDE * scale));
    const canvas = draw(source, size.width, size.height);
    for (const quality of THUMBNAIL_QUALITIES) {
      const blob = await encode(canvas, "image/jpeg", quality);
      if (blob && blob.size <= budget) return bytesOf(blob);
    }
  }
  return new Uint8Array();
}

/** A chosen file as sent, with thumbnails of at most budget bytes. */
export async function prepare(file: File, budget: number): Promise<Prepared> {
  const mediaType = file.type || "application/octet-stream";
  if (!mediaType.startsWith("image/")) {
    return { bytes: await bytesOf(file), mediaType, width: 0, height: 0, thumbnail: new Uint8Array() };
  }
  let image: ImageBitmap;
  try {
    image = await createImageBitmap(file, { imageOrientation: "from-image" });
  } catch {
    // An image this browser cannot read goes as a file.
    return { bytes: await bytesOf(file), mediaType, width: 0, height: 0, thumbnail: new Uint8Array() };
  }
  try {
    if (!isResizable(mediaType)) {
      return {
        bytes: await bytesOf(file),
        mediaType,
        width: image.width,
        height: image.height,
        thumbnail: await thumbnail(image, image.width, image.height, budget),
      };
    }
    const size = fitted(image.width, image.height, IMAGE_SIDE);
    const canvas = draw(image, size.width, size.height);
    const jpeg = await encode(canvas, "image/jpeg", IMAGE_QUALITY);
    if (!jpeg) throw new Error("The picture could not be encoded.");
    return {
      bytes: await bytesOf(jpeg),
      mediaType: "image/jpeg",
      ...size,
      thumbnail: await thumbnail(canvas, size.width, size.height, budget),
    };
  } finally {
    image.close();
  }
}

/** The name a scaled image goes by: its own, ending in .jpg. */
export function jpegName(name: string): string {
  return name.replace(/\.[^./]*$/, "") + ".jpg";
}
