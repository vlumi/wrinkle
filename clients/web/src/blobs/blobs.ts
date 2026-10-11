// Where blobs (avatars and, later, stickers and attachments) are read and
// uploaded: the blob address, which follows from the gateway's URL.

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
