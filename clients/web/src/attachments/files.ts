// Attachments' files: downloaded from the blob address, checked against
// their digest, decrypted, and kept in memory by blob id while there is room.

import { blobURL } from "../blobs/blobs";
import { decryptBlob } from "../e2ee/crypto";
import type { Attachment } from "../gen/wrinkle/v1/content_pb";
import { hex } from "../ids";

/** How much decrypted data to keep, in bytes. */
const CACHE_BYTES = 200 * 1024 * 1024;

const files = new Map<string, Promise<Blob>>();
const sizes = new Map<string, number>();
let cached = 0;

function remember(id: string, blob: Blob) {
  sizes.set(id, blob.size);
  cached += blob.size;
  // Oldest first, as the map keeps insertion order.
  for (const [old, size] of sizes) {
    if (cached <= CACHE_BYTES || old === id) break;
    sizes.delete(old);
    files.delete(old);
    cached -= size;
  }
}

/** Keeps a file this device sent, so showing it needs no download. */
export function keep(blobId: Uint8Array, blob: Blob): void {
  const id = hex(blobId);
  if (files.has(id)) return;
  files.set(id, Promise.resolve(blob));
  remember(id, blob);
}

/** The attachment's file, from memory or downloaded; rejects if it cannot be had or does not match. */
export function fileOf(address: string, a: Attachment): Promise<Blob> {
  const id = hex(a.blobId);
  const known = files.get(id);
  if (known) {
    // Used again: the newest in the order.
    files.delete(id);
    files.set(id, known);
    return known;
  }
  const loading = (async () => {
    const resp = await fetch(blobURL(address, a.blobId));
    if (!resp.ok) throw new Error(`The download failed (HTTP ${resp.status}).`);
    const plain = await decryptBlob(a.key, a.digest, new Uint8Array(await resp.arrayBuffer()));
    return new Blob([plain], { type: a.mediaType || "application/octet-stream" });
  })();
  files.set(id, loading);
  loading.then(
    (blob) => {
      if (files.get(id) === loading) remember(id, blob);
    },
    () => {
      // Not kept: tried again next time.
      if (files.get(id) === loading) files.delete(id);
    },
  );
  return loading;
}

const thumbnails = new Map<string, string>();

/** An object URL of the attachment's thumbnail, made once per blob; empty if it has none. */
export function thumbnailURL(a: Attachment): string {
  if (a.thumbnail.length === 0) return "";
  const id = hex(a.blobId);
  let url = thumbnails.get(id);
  if (!url) {
    url = URL.createObjectURL(new Blob([new Uint8Array(a.thumbnail)], { type: "image/jpeg" }));
    thumbnails.set(id, url);
  }
  return url;
}

/** Saves the file under its name, through an object URL. */
export function save(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.style.display = "none";
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}
