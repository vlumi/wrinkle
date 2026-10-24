// The account service: registration, sign-in with a password, device
// tokens, profiles and the name claims that keep names unique, against the
// user keyspace.
import { create } from "@bufbuild/protobuf";
import type { Pool } from "mysql2/promise";
import { UserSchema, type User } from "../gen/wrinkle/v1/model_pb.js";
import type { Blobs } from "../internal/blobs.ts";
import { hex, type UUID } from "../internal/ids.ts";
import { placeholders, row, rows } from "../internal/db.ts";
import type { Logger } from "../internal/log.ts";

export interface AccountsDeps {
  /** The user keyspace. */
  db: Pool;
  blobs: Blobs;
  log: Logger;
}

interface ProfileRow {
  user_id: Buffer;
  name: string;
  icon: string;
  avatar: Buffer | null;
  deleted_at: Date | null;
}

const toUser = (r: ProfileRow): User =>
  create(UserSchema, {
    userId: r.user_id,
    name: r.name,
    icon: r.icon,
    avatar: r.avatar ?? new Uint8Array(),
    deleted: r.deleted_at !== null,
  });

/** A user's profile, or undefined when there is no such user. */
export async function profile(db: Pool, userId: UUID): Promise<User | undefined> {
  const r = await row<ProfileRow>(db, `SELECT user_id, name, icon, avatar, deleted_at FROM user WHERE user_id = ?`, [
    userId,
  ]);
  return r && toUser(r);
}

/**
 * The profiles of several users in one query, by hex user id. A user that
 * does not exist has only its id.
 */
export async function profiles(db: Pool, userIds: UUID[]): Promise<Map<string, User>> {
  const result = new Map<string, User>();
  if (userIds.length === 0) return result;
  const list = await rows<ProfileRow>(
    db,
    `SELECT user_id, name, icon, avatar, deleted_at FROM user WHERE user_id IN (${placeholders(userIds.length)})`,
    userIds,
  );
  for (const r of list) result.set(hex(r.user_id), toUser(r));
  for (const id of userIds) if (!result.has(hex(id))) result.set(hex(id), create(UserSchema, { userId: id }));
  return result;
}
