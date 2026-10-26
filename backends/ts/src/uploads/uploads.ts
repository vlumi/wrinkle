// Upload slots for blobs: a presigned PUT for exactly the declared type and
// size, and the attachments a message takes into use and releases.
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  BlobPurpose,
  CreateUploadResponseSchema,
  type CreateUploadRequest,
  type CreateUploadResponse,
} from "../gen/wrinkle/v1/messaging_pb.js";
import { failedPrecondition, invalidArgument } from "../internal/apierror.ts";
import { exec, now, placeholders, rows, tx, type Querier } from "../internal/db.ts";
import { newUUID, type UUID } from "../internal/ids.ts";
import type { Deps } from "../services/deps.ts";

const slotLifetime = 900; // seconds
const maxMediaTypeBytes = 100;

interface Limit {
  purpose: string;
  mediaTypes?: string[]; // undefined allows any
  maxSize: bigint;
}

const limits = new Map<BlobPurpose, Limit>([
  [
    BlobPurpose.AVATAR,
    { purpose: "avatar", mediaTypes: ["image/jpeg", "image/png", "image/webp"], maxSize: 512n * 1024n },
  ],
  [
    BlobPurpose.STICKER,
    { purpose: "sticker", mediaTypes: ["image/png", "image/webp", "image/gif"], maxSize: 256n * 1024n },
  ],
  [BlobPurpose.ATTACHMENT, { purpose: "attachment", maxSize: 25n * 1024n * 1024n }],
]);

export async function createUpload(deps: Deps, userId: UUID, req: CreateUploadRequest): Promise<CreateUploadResponse> {
  const l = limits.get(req.purpose);
  if (!l) throw invalidArgument(`unknown purpose BLOB_PURPOSE_${BlobPurpose[req.purpose]}`);
  if (l.mediaTypes && !l.mediaTypes.includes(req.mediaType)) {
    throw invalidArgument(`${l.purpose} must be one of ${l.mediaTypes.join(", ")}`);
  }
  if (!validMediaType(req.mediaType)) throw invalidArgument("media_type must be type/subtype");
  if (req.size < 1n || req.size > l.maxSize) {
    throw invalidArgument(`size must be 1 to ${l.maxSize} bytes for ${l.purpose}`);
  }
  const blobId = newUUID();
  const at = now();
  await exec(
    deps.users,
    `INSERT INTO upload (user_id, blob_id, purpose, media_type, size, used, created_at)
     VALUES (?, ?, ?, ?, ?, FALSE, ?)`,
    [userId, blobId, l.purpose, req.mediaType, req.size, at],
  );
  return create(CreateUploadResponseSchema, {
    blobId,
    uploadPath: deps.blobs.presignPut(blobId, req.mediaType, req.size, at, slotLifetime),
    expiresAt: timestampFromDate(new Date(at.getTime() + slotLifetime * 1000)),
  });
}

/** A media type as type/subtype, printable ASCII without spaces. */
export function validMediaType(s: string): boolean {
  if (Buffer.byteLength(s) > maxMediaTypeBytes) return false;
  for (const c of Buffer.from(s)) if (c <= 0x20 || c > 0x7e) return false;
  const i = s.indexOf("/");
  if (i <= 0) return false;
  const sub = s.slice(i + 1);
  return sub !== "" && !sub.includes("/");
}

const notAttachmentUpload = () => failedPrecondition("not an attachment upload of yours");

/**
 * Takes a message's attachments into use: each must be an attachment upload
 * of the sender's, uploaded, and not attached to another message. Checked
 * again under row locks, so the sweeper cannot take them meanwhile. They
 * stay pending until the fan-out delivers the message, so a send that fails
 * without releasing them loses them only to the sweeper.
 */
export async function useAttachments(deps: Deps, userId: UUID, blobIds: UUID[]): Promise<void> {
  if (blobIds.length === 0) return;
  const query = `SELECT used FROM upload WHERE user_id = ? AND purpose = 'attachment' AND blob_id IN (${placeholders(blobIds.length)})`;
  const args = [userId, ...blobIds];
  await attachmentUploads(deps.users, query, args, blobIds.length);
  for (const id of blobIds) {
    if (!(await deps.blobs.exists(id))) throw failedPrecondition("not uploaded");
  }
  await tx(deps.users, async (c) => {
    if (await attachmentUploads(c, query + " FOR UPDATE", args, blobIds.length)) {
      throw failedPrecondition("already attached");
    }
    await exec(
      c,
      `UPDATE upload SET used = TRUE, pending_since = ? WHERE user_id = ? AND blob_id IN (${placeholders(blobIds.length)})`,
      [now(), ...args],
    );
  });
}

/** Whether any of the uploads is used; throws unless all are the user's attachment uploads. */
async function attachmentUploads(q: Querier, query: string, args: unknown[], want: number): Promise<boolean> {
  const found = await rows<{ used: number }>(q, query, args);
  if (found.length !== want) throw notAttachmentUpload();
  return found.some((r) => r.used !== 0);
}

/** Gives a failed send's attachments back to the sweeper. */
export async function releaseAttachments(deps: Deps, userId: UUID, blobIds: UUID[]): Promise<void> {
  if (blobIds.length === 0) return;
  await exec(
    deps.users,
    `UPDATE upload SET used = FALSE, pending_since = NULL WHERE user_id = ? AND purpose = 'attachment' AND blob_id IN (${placeholders(blobIds.length)})`,
    [userId, ...blobIds],
  );
}
