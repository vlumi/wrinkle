// Stickers made from a user's sticker uploads: public and permanent, the
// sticker's id its blob's.
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { StickerSchema, type Sticker } from "../gen/wrinkle/v1/model_pb.js";
import { codeOf, failedPrecondition, invalidArgument } from "../internal/apierror.ts";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb.js";
import { exec, isDuplicate, now, placeholders, row, rows, tx } from "../internal/db.ts";
import { hex, parseUUID, type UUID } from "../internal/ids.ts";
import type { Deps } from "../services/deps.ts";
import { save } from "./palette.ts";

const maxGetIds = 100;

export function parseId(b: Uint8Array, field: string): UUID {
  const id = parseUUID(b);
  if (!id) throw invalidArgument(`${field} must be 16 bytes`);
  return id;
}

const notStickerUpload = () => failedPrecondition("not a sticker upload of yours");

/**
 * Makes a sticker of the user's uploaded sticker blob and saves it to their
 * palette unless that is full. A sticker that exists already is returned as
 * it is.
 */
export async function createSticker(deps: Deps, userId: UUID, blob: Uint8Array): Promise<Sticker> {
  const blobId = parseId(blob, "blob_id");
  const upload = await row<{ media_type: string }>(
    deps.users,
    `SELECT media_type FROM upload WHERE user_id = ? AND blob_id = ? AND purpose = 'sticker'`,
    [userId, blobId],
  );
  if (!upload) throw notStickerUpload();
  if (!(await deps.blobs.exists(blobId))) throw failedPrecondition("not uploaded");
  const existing = await load(deps, [blobId]);
  if (existing[0]) return existing[0];
  // Used before the sticker row exists, so the sweeper keeps the blob even if what follows fails.
  await tx(deps.users, async (c) => {
    const locked = await row(
      c,
      `SELECT 1 FROM upload WHERE user_id = ? AND blob_id = ? AND purpose = 'sticker' FOR UPDATE`,
      [userId, blobId],
    );
    if (!locked) throw notStickerUpload();
    await exec(c, `UPDATE upload SET used = TRUE WHERE user_id = ? AND blob_id = ?`, [userId, blobId]);
  });
  const at = now();
  const sticker = create(StickerSchema, {
    stickerId: blobId,
    creatorUserId: userId,
    mediaType: upload.media_type,
    createdAt: timestampFromDate(at),
  });
  try {
    await exec(
      deps.users,
      `INSERT INTO sticker (sticker_id, creator_user_id, media_type, created_at) VALUES (?, ?, ?, ?)`,
      [blobId, userId, upload.media_type, at],
    );
  } catch (err) {
    if (!isDuplicate(err)) throw err;
    const winner = (await load(deps, [blobId]))[0];
    if (!winner) throw new Error("sticker missing after a duplicate key", { cause: err });
    return winner;
  }
  try {
    await save(deps, userId, blobId);
  } catch (err) {
    if (codeOf(err) !== ErrorCode.FAILED_PRECONDITION) throw err;
  }
  return sticker;
}

/** The known stickers of 1 to 100 ids, each once, in the order asked. */
export async function getStickers(deps: Deps, stickerIds: Uint8Array[]): Promise<Sticker[]> {
  if (stickerIds.length < 1 || stickerIds.length > maxGetIds) {
    throw invalidArgument(`sticker_ids must have 1 to ${maxGetIds} ids`);
  }
  const seen = new Set<string>();
  const list: UUID[] = [];
  for (const b of stickerIds) {
    const id = parseId(b, "sticker_ids");
    if (!seen.has(hex(id))) {
      seen.add(hex(id));
      list.push(id);
    }
  }
  return load(deps, list);
}

/** The stickers of the ids that exist, in the order given. */
export async function load(deps: Deps, stickerIds: UUID[]): Promise<Sticker[]> {
  if (stickerIds.length === 0) return [];
  const found = new Map<string, Sticker>();
  for (const r of await rows<{ sticker_id: Buffer; creator_user_id: Buffer; media_type: string; created_at: Date }>(
    deps.users,
    `SELECT sticker_id, creator_user_id, media_type, created_at FROM sticker WHERE sticker_id IN (${placeholders(stickerIds.length)})`,
    stickerIds,
  )) {
    found.set(
      hex(r.sticker_id),
      create(StickerSchema, {
        stickerId: r.sticker_id,
        creatorUserId: r.creator_user_id,
        mediaType: r.media_type,
        createdAt: timestampFromDate(r.created_at),
      }),
    );
  }
  return stickerIds.flatMap((id) => found.get(hex(id)) ?? []);
}
