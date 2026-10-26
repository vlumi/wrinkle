// What the user keyspace does for groups: resolving the names a user
// invites, the contact check on them, and leaving every group of a deleted
// account.
import type { Pool } from "mysql2/promise";
import { ErrorCode, ErrorReason } from "../gen/wrinkle/v1/frame_pb.js";
import { resolve } from "../accounts/names.ts";
import { validName } from "../accounts/validate.ts";
import { contactsAmong } from "../contacts/contacts.ts";
import { codeOf, failedPrecondition, invalidArgument, notFound } from "../internal/apierror.ts";
import { rows } from "../internal/db.ts";
import { hex, type UUID } from "../internal/ids.ts";

/** The most members a group may have. */
export const maxMembers = 256;
// Bounds the names one request may invite.
const maxMemberNames = maxMembers - 1;
// How many of a deleted user's groups are read at a time.
const leavePage = 100;

export interface Invitees {
  /** The named users, without the requesting user and repeats, in order. */
  users: UUID[];
  /** The name each was first given by, by hex user id. */
  names: Map<string, string>;
}

/** Finds the named users, leaving out the requesting user and repeats, with the name each was first given by. */
export async function resolveMembers(db: Pool, userId: UUID, names: string[]): Promise<Invitees> {
  if (names.length > maxMemberNames) throw invalidArgument(`at most ${maxMemberNames} member names`);
  for (const name of names)
    if (!validName(name)) throw invalidArgument(`member name ${JSON.stringify(name)} is not a valid name`);
  const given = new Map<string, string>([[hex(userId), ""]]);
  const users: UUID[] = [];
  for (const name of names) {
    const id = await resolve(db, name);
    if (!id) throw notFound(`no user named ${JSON.stringify(name)}`);
    if (!given.has(hex(id))) {
      given.set(hex(id), name);
      users.push(id);
    }
  }
  return { users, names: given };
}

/** A check that every user is the requesting user's contact; the first who is not fails, by the name given. */
export function checkContacts(db: Pool, userId: UUID, names: Map<string, string>): (users: UUID[]) => Promise<void> {
  return async (users) => {
    const contacts = await contactsAmong(db, userId, users);
    for (const id of users)
      if (!contacts.has(hex(id)))
        throw failedPrecondition(ErrorReason.NOT_A_CONTACT, `not a contact: ${names.get(hex(id)) ?? ""}`);
  };
}

/**
 * Takes the user out of every group the list says they are in, for a
 * deleted account; leave is the conversation service's LeaveGroup. A group
 * left already, the list not showing it yet, is fine.
 */
export async function leaveGroups(
  db: Pool,
  userId: UUID,
  leave: (userId: UUID, conversationId: UUID) => Promise<void>,
): Promise<void> {
  let after: Buffer = Buffer.alloc(16);
  for (;;) {
    const page = await rows<{ conversation_id: Buffer }>(
      db,
      `SELECT conversation_id FROM user_conversation
       WHERE user_id = ? AND kind = 'group' AND member AND conversation_id > ?
       ORDER BY conversation_id LIMIT ?`,
      [userId, after, leavePage],
    );
    for (const { conversation_id: id } of page) {
      try {
        await leave(userId, id);
      } catch (err) {
        const code = codeOf(err);
        if (code !== ErrorCode.PERMISSION_DENIED && code !== ErrorCode.NOT_FOUND) throw err;
      }
    }
    if (page.length < leavePage) return;
    after = page[page.length - 1]!.conversation_id;
  }
}
