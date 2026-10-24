// Profile changes: name, icon and avatar.
import { create } from "@bufbuild/protobuf";
import type { UpdateProfileRequest } from "../gen/wrinkle/v1/messaging_pb.js";
import { ProfileEventSchema } from "../gen/wrinkle/v1/event_pb.js";
import { UserSchema, type User } from "../gen/wrinkle/v1/model_pb.js";
import { failedPrecondition, invalidArgument, unauthenticated } from "../internal/apierror.ts";
import { exec, now, nullBytes, row, tx, type Querier } from "../internal/db.ts";
import { parseUUID, sameId, type UUID } from "../internal/ids.ts";
import { claim, releaseClaim } from "./names.ts";
import { Outbox } from "./outbox.ts";
import { profile, type AccountsDeps } from "./service.ts";
import { checkName, validIcon } from "./validate.ts";

const deleted = () => unauthenticated("the account is deleted");

/**
 * Changes the user's name, icon, avatar or several. A new name is claimed
 * first; the change and its outbox row are one transaction on the user's
 * shard; the old claim is released last. An interruption leaves at worst a
 * stale claim, which the next claimant takes over.
 */
export async function updateProfile(d: AccountsDeps, userId: UUID, req: UpdateProfileRequest): Promise<User> {
  if (req.name === undefined && req.icon === undefined && req.avatar === undefined) return mustProfile(d, userId);
  if (req.name !== undefined) checkName(req.name);
  if (req.icon !== undefined && !validIcon(req.icon)) throw invalidArgument(`unknown icon ${JSON.stringify(req.icon)}`);
  if (req.avatar !== undefined) await checkAvatar(d, userId, req.avatar);
  const current = await mustProfile(d, userId);
  if (current.deleted) throw deleted();
  if (req.name !== undefined && req.name !== current.name) await claim(d.db, req.name, userId);
  const { oldName, user } = await storeProfile(d, userId, req);
  if (user.name !== oldName) {
    // The change is committed; a claim left behind is stale and is taken
    // over by the next claimant.
    await release(d, userId, oldName, user.name).catch((err: unknown) =>
      d.log.warn("releasing the old name failed", { user: userId, err }),
    );
  }
  return user;
}

async function mustProfile(d: AccountsDeps, userId: UUID): Promise<User> {
  const user = await profile(d.db, userId);
  if (!user) throw new Error(`user ${userId.toString("hex")} missing`);
  return user;
}

/** Accepts an empty avatar, for none, or a blob the user has uploaded as an avatar. */
async function checkAvatar(d: AccountsDeps, userId: UUID, avatar: Uint8Array): Promise<void> {
  if (avatar.length === 0) return;
  const blobId = parseUUID(avatar);
  if (!blobId) throw invalidArgument("avatar must be empty or 16 bytes");
  await avatarUpload(d.db, userId, blobId, false);
  if (!(await d.blobs.exists(blobId))) throw failedPrecondition("not uploaded");
}

/** Checks that the user made an avatar upload slot for the blob, optionally locking it against the sweeper. */
async function avatarUpload(q: Querier, userId: UUID, blobId: UUID, lock: boolean): Promise<void> {
  const r = await row(
    q,
    `SELECT 1 FROM upload WHERE user_id = ? AND blob_id = ? AND purpose = 'avatar'` + (lock ? ` FOR UPDATE` : ""),
    [userId, blobId],
  );
  if (!r) throw failedPrecondition("not an avatar upload of yours");
}

/** Applies the change and writes its outbox row in one transaction. */
async function storeProfile(
  d: AccountsDeps,
  userId: UUID,
  req: UpdateProfileRequest,
): Promise<{ oldName: string; user: User }> {
  return tx(d.db, async (c) => {
    const r = await row<{
      name: string;
      icon: string;
      avatar: Buffer | null;
      deleted_at: Date | null;
      last_outbox_id: bigint;
    }>(c, `SELECT name, icon, avatar, deleted_at, last_outbox_id FROM user WHERE user_id = ? FOR UPDATE`, [userId]);
    if (!r) throw new Error(`user ${userId.toString("hex")} missing`);
    if (r.deleted_at !== null) throw deleted();
    const o = new Outbox(c, userId, now(), r.last_outbox_id);
    const user = create(UserSchema, {
      userId,
      name: req.name ?? r.name,
      icon: req.icon ?? r.icon,
      avatar: r.avatar ?? new Uint8Array(),
    });
    if (req.avatar !== undefined) {
      await swapAvatar(c, userId, user.avatar, req.avatar);
      user.avatar = req.avatar;
    }
    await exec(c, `UPDATE user SET name = ?, icon = ?, avatar = ? WHERE user_id = ?`, [
      user.name,
      user.icon,
      nullBytes(user.avatar),
      userId,
    ]);
    await o.add("profile", { case: "profile", value: create(ProfileEventSchema, { user }) });
    await o.save();
    return { oldName: r.name, user };
  });
}

/** Marks the new avatar's upload used and the old one's unused, for the sweeper to delete. */
async function swapAvatar(q: Querier, userId: UUID, old: Uint8Array, avatar: Uint8Array): Promise<void> {
  if (avatar.length > 0) {
    const blobId = parseUUID(avatar)!;
    await avatarUpload(q, userId, blobId, true);
    await exec(q, `UPDATE upload SET used = TRUE WHERE user_id = ? AND blob_id = ?`, [userId, blobId]);
  }
  if (old.length > 0 && !sameId(old, avatar)) {
    await exec(q, `UPDATE upload SET used = FALSE WHERE user_id = ? AND blob_id = ?`, [userId, Buffer.from(old)]);
  }
}

/**
 * Gives up the claim on the old name after a rename. Names that differ only
 * in case share one claim, which stays as it is: claims compare ignoring
 * case, and Vitess cannot change a vindex column in place.
 */
async function release(d: AccountsDeps, userId: UUID, oldName: string, newName: string): Promise<void> {
  const r = await row<{ same: unknown }>(d.db, `SELECT ? = ? COLLATE utf8mb4_0900_as_ci AS same`, [oldName, newName]);
  if (Number(r?.same) === 1) return;
  await releaseClaim(d.db, oldName, userId);
}
