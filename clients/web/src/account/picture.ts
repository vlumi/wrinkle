// A chosen picture made into an avatar: the center square, at most 512
// pixels a side, encoded under the contract's 512 KiB limit.

export const AVATAR_SIDE = 512;
export const AVATAR_MAX_BYTES = 512 * 1024;
/** Encoding qualities to try, best first, until one fits. */
export const QUALITIES = [0.9, 0.8, 0.7, 0.6, 0.5, 0.4];

export interface Crop {
  /** The source square's top left corner and side. */
  sx: number;
  sy: number;
  side: number;
  /** The output's side. */
  size: number;
}

/** The centered square of a width × height image, scaled down to at most max a side. */
export function squareCrop(width: number, height: number, max = AVATAR_SIDE): Crop {
  const side = Math.min(width, height);
  return {
    sx: Math.floor((width - side) / 2),
    sy: Math.floor((height - side) / 2),
    side,
    size: Math.min(side, max),
  };
}

function encode(canvas: HTMLCanvasElement, type: string, quality: number): Promise<Blob | null> {
  return new Promise((resolve) => canvas.toBlob(resolve, type, quality));
}

/** The picture as an avatar: WebP, or JPEG where the browser cannot encode WebP. */
export async function preparePicture(file: Blob): Promise<Blob> {
  let image: ImageBitmap;
  try {
    image = await createImageBitmap(file, { imageOrientation: "from-image" });
  } catch {
    throw new Error("That file is not a picture this browser can read.");
  }
  const { sx, sy, side, size } = squareCrop(image.width, image.height);
  if (side === 0) throw new Error("The picture is empty.");
  const canvas = document.createElement("canvas");
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("Pictures cannot be drawn in this browser.");
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(image, sx, sy, side, side, 0, 0, size, size);
  image.close();
  for (const type of ["image/webp", "image/jpeg"]) {
    for (const quality of QUALITIES) {
      const blob = await encode(canvas, type, quality);
      // A browser that cannot encode the type falls back to PNG.
      if (!blob || blob.type !== type) break;
      if (blob.size <= AVATAR_MAX_BYTES) return blob;
    }
  }
  throw new Error("The picture could not be made small enough.");
}
