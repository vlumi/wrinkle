// Uploading a blob: an address asked of the gateway, then the bytes put there.

import type { Connection } from "../connection/connection";
import { BlobPurpose } from "../gen/wrinkle/v1/messaging_pb";
import { blobAddress, put } from "./blobs";

/** Uploads a blob for the purpose, telling the share sent; resolves with its id. */
export async function upload(
  conn: Connection | null,
  purpose: BlobPurpose,
  blob: Blob,
  onProgress?: (share: number) => void,
): Promise<Uint8Array> {
  const address = conn && blobAddress(conn.url);
  if (!conn || !address) throw new Error("not connected");
  const u = await conn.call("createUpload", {
    case: "createUpload",
    value: { purpose, mediaType: blob.type, size: BigInt(blob.size) },
  });
  if (onProgress) {
    await put(address + u.uploadPath, blob, onProgress);
    return u.blobId;
  }
  // The browser sets Content-Length from the body, which is the size asked for.
  const resp = await fetch(address + u.uploadPath, {
    method: "PUT",
    headers: { "Content-Type": blob.type },
    body: blob,
  });
  if (!resp.ok) throw new Error(`The upload failed (HTTP ${resp.status}).`);
  return u.blobId;
}
