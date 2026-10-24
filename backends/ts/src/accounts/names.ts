// A name claim in user_name is valid while its owner's row still has that
// name. Names are compared by MySQL, under the claim table's collation,
// never here.
import type { Pool } from "mysql2/promise";
import { alreadyExists, unavailable } from "../internal/apierror.ts";
import { exec, isDuplicate, now, row } from "../internal/db.ts";
import { sameId, type UUID } from "../internal/ids.ts";

const claimAttempts = 3;

/** Whether the user's row has the name, ignoring case. */
async function owns(db: Pool, userId: UUID, name: string): Promise<boolean> {
  const r = await row(db, `SELECT 1 FROM user WHERE user_id = ? AND name = ? COLLATE utf8mb4_0900_as_ci`, [
    userId,
    name,
  ]);
  return r !== undefined;
}

/** The user holding the claim on the name, if any. */
export async function claimOwner(db: Pool, name: string): Promise<UUID | undefined> {
  const r = await row<{ user_id: Buffer }>(db, `SELECT user_id FROM user_name WHERE name = ?`, [name]);
  return r?.user_id;
}

/**
 * Takes the name for the user. A claim the user already holds is fine; a
 * valid claim of another user fails with ALREADY_EXISTS; a stale one is
 * taken over.
 */
export async function claim(db: Pool, name: string, userId: UUID): Promise<void> {
  for (let i = 0; i < claimAttempts; i++) {
    try {
      await exec(db, `INSERT INTO user_name (name, user_id, created_at) VALUES (?, ?, ?)`, [name, userId, now()]);
      return;
    } catch (err) {
      if (!isDuplicate(err)) throw err;
    }
    const owner = await claimOwner(db, name);
    if (!owner) continue;
    if (sameId(owner, userId)) return;
    if (await owns(db, owner, name)) throw alreadyExists("the name is taken");
    await exec(db, `DELETE FROM user_name WHERE name = ? AND user_id = ?`, [name, owner]);
  }
  throw unavailable("the name is contended; try again");
}

/** The user with the name, ignoring case, or undefined when no user has it. */
export async function resolve(db: Pool, name: string): Promise<UUID | undefined> {
  const owner = await claimOwner(db, name);
  if (!owner) return undefined;
  return (await owns(db, owner, name)) ? owner : undefined;
}

/** Gives up a claim, as after a rename or a deletion. */
export async function releaseClaim(db: Pool, name: string, userId: UUID): Promise<void> {
  await exec(db, `DELETE FROM user_name WHERE name = ? AND user_id = ?`, [name, userId]);
}
