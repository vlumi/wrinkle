// Attachments: the limits, what a message with them reads as, and how the
// thumbnails share the sealed content's room. Pure, so tested without a page.

import type { Attachment, Attachments } from "../gen/wrinkle/v1/content_pb";

export const MAX_ATTACHMENTS = 10;
/** The blob's limit is 25 MiB; encryption adds a nonce and a tag. */
export const MAX_FILE_BYTES = 25 * 1024 * 1024 - 28;
export const MAX_CAPTION = 4000;
export const MAX_THUMBNAIL_BYTES = 16 * 1024;
/** Images are sent at most this many pixels on the longest side. */
export const IMAGE_SIDE = 2048;
export const IMAGE_QUALITY = 0.85;
export const THUMBNAIL_SIDE = 320;
/** The room for the serialized content, under the sealed ciphertext's 48 KiB. */
export const CONTENT_BUDGET = 46 * 1024;
/** What an attachment takes besides its name, media type and thumbnail: ids, key, digest, sizes, tags. */
const ITEM_OVERHEAD = 128;
/** Longer names are cut to this many characters. */
const MAX_NAME = 200;

const encoder = new TextEncoder();
const utf8 = (s: string) => encoder.encode(s).length;

/** Images that are resized and sent as JPEG; other images, such as GIFs, go as they are. */
export const isResizable = (mediaType: string): boolean =>
  ["image/jpeg", "image/png", "image/webp", "image/heic", "image/heif", "image/avif", "image/bmp"].includes(mediaType);

/** Whether an attachment shows as an image: an image type with a size. */
export const isImage = (a: Pick<Attachment, "mediaType" | "width" | "height">): boolean =>
  a.mediaType.startsWith("image/") && a.width > 0 && a.height > 0;

/** The size of a width × height image scaled to at most max on its longest side, never up. */
export function fitted(width: number, height: number, max: number): { width: number; height: number } {
  const scale = Math.min(1, max / Math.max(width, height));
  return { width: Math.max(1, Math.round(width * scale)), height: Math.max(1, Math.round(height * scale)) };
}

/** The bytes each image's thumbnail may take, so the whole content fits; 0 leaves them out. */
export function thumbnailBudget(caption: string, items: { name: string; mediaType: string }[], images: number): number {
  if (images === 0) return 0;
  const fixed = utf8(caption) + items.reduce((n, i) => n + ITEM_OVERHEAD + utf8(i.name) + utf8(i.mediaType), 0);
  const each = Math.floor((CONTENT_BUDGET - fixed) / images);
  return Math.max(0, Math.min(MAX_THUMBNAIL_BYTES, each));
}

/** A file's name as sent: without control characters, cut to MAX_NAME characters. */
export function cleanName(name: string): string {
  // eslint-disable-next-line no-control-regex
  const clean = name.replace(/[\u0000-\u001f\u007f]/g, "").trim();
  return Array.from(clean).slice(0, MAX_NAME).join("") || "file";
}

/** What a message with attachments reads as in the list: its caption, or what it holds. */
export function attachmentsText(a: Pick<Attachments, "caption" | "items">): string {
  if (a.caption) return a.caption;
  const images = a.items.filter(isImage).length;
  if (images === a.items.length) return images === 1 ? "Photo" : `${images} photos`;
  if (a.items.length === 1) return `File: ${a.items[0].name}`;
  return `${a.items.length} attachments`;
}

/** A size in bytes for people. */
export function formatSize(bytes: number | bigint): string {
  const n = Number(bytes);
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`;
  return `${(n / (1024 * 1024)).toFixed(n < 10 * 1024 * 1024 ? 1 : 0)} MB`;
}

/** Whether a received Attachments is one this client can show: 1 to 10 items, each with a key and digest. */
export function isValid(a: Attachments): boolean {
  return (
    a.items.length >= 1 &&
    a.items.length <= MAX_ATTACHMENTS &&
    a.items.every((i) => i.blobId.length === 16 && i.key.length === 32 && i.digest.length === 32)
  );
}
