// Where blobs (avatars, stickers and attachments) are read and uploaded:
// the blob address, which follows from the gateway's URL.

import { hex } from "../ids";

/**
 * The blob address beside a gateway: https://<host>:<port> of a wss
 * endpoint, or http://<host>:9200 beside a plain ws one; null for anything else.
 */
export function blobAddress(gatewayURL: string): string | null {
  let u: URL;
  try {
    u = new URL(gatewayURL);
  } catch {
    return null;
  }
  if (u.protocol === "wss:") return `https://${u.host}`;
  if (u.protocol === "ws:") return `http://${u.hostname}:9200`;
  return null;
}

/** Where a blob is read. */
export function blobURL(address: string, blobId: Uint8Array): string {
  return `${address}/blobs/${hex(blobId)}`;
}

/**
 * Uploads a blob to a presigned address, telling the share sent so far.
 * XMLHttpRequest rather than fetch, which does not report upload progress.
 */
export function put(url: string, blob: Blob, onProgress: (share: number) => void): Promise<void> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("PUT", url);
    xhr.setRequestHeader("Content-Type", blob.type);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && e.total > 0) onProgress(e.loaded / e.total);
    };
    xhr.onload = () =>
      xhr.status >= 200 && xhr.status < 300 ? resolve() : reject(new Error(`The upload failed (HTTP ${xhr.status}).`));
    xhr.onerror = () => reject(new Error("The upload failed."));
    xhr.send(blob);
  });
}
