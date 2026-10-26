// The contact service: each user's own side toward other users, a request
// or a block, changed only through the user's outbox, and the checks the
// other services make against it, against the user keyspace. The fan-out
// copies each side to the other user's contact_peer and ends the other's
// request when a side ends.
import type { Pool } from "mysql2/promise";
import type { AddContactRequest } from "../gen/wrinkle/v1/messaging_pb.js";
import type { Contact, User } from "../gen/wrinkle/v1/model_pb.js";
import { profile, profiles } from "../accounts/service.ts";
import { resolve } from "../accounts/names.ts";
import { validName } from "../accounts/validate.ts";
import { ErrorReason } from "../gen/wrinkle/v1/frame_pb.js";
import { failedPrecondition, invalidArgument, notFound, permissionDenied } from "../internal/apierror.ts";
import { placeholders, rows, timedRows, type DbTime } from "../internal/db.ts";
import { hex, parseUUID, sameId, type UUID } from "../internal/ids.ts";
import { Block, blocks, change, contactOf, Request, sides, type Side } from "./sides.ts";

// Bounds each state's part of the list.
const maxPerState = 1000;

function parseUserId(b: Uint8Array): UUID {
  const id = parseUUID(b);
  if (!id) throw invalidArgument("user_id must be 16 bytes");
  return id;
}

// The queries of the list, one per state, each on the user's shard: the
// user's own rows with the copies of the other side, and the copies of
// requests the user has no row for.
const ownColumns = `
  SELECT c.other_user_id, c.state AS own_state, c.updated_at AS own_at, p.state AS peer_state, p.updated_at AS peer_at
  FROM contact c LEFT JOIN contact_peer p ON p.user_id = c.user_id AND p.other_user_id = c.other_user_id`;
const listQueries = [
  ownColumns +
    ` WHERE c.user_id = ? AND c.state = 'request' AND p.state = 'request'
      ORDER BY GREATEST(c.updated_at, p.updated_at) DESC LIMIT ?`,
  ownColumns +
    ` WHERE c.user_id = ? AND c.state = 'request' AND (p.state IS NULL OR p.state <> 'request')
      ORDER BY GREATEST(c.updated_at, COALESCE(p.updated_at, c.updated_at)) DESC LIMIT ?`,
  ownColumns +
    ` WHERE c.user_id = ? AND c.state = 'block'
      ORDER BY GREATEST(c.updated_at, COALESCE(p.updated_at, c.updated_at)) DESC LIMIT ?`,
  `SELECT p.other_user_id, c.state AS own_state, c.updated_at AS own_at, p.state AS peer_state, p.updated_at AS peer_at
   FROM contact_peer p LEFT JOIN contact c ON c.user_id = p.user_id AND c.other_user_id = p.other_user_id
   WHERE p.user_id = ? AND p.state = 'request' AND c.user_id IS NULL
   ORDER BY p.updated_at DESC LIMIT ?`,
];

interface ListedRow {
  other_user_id: Buffer;
  own_state: string | null;
  own_at: DbTime | null;
  peer_state: string | null;
  peer_at: DbTime | null;
}

const side = (state: string | null, at: DbTime | null): Side => ({ state: state ?? "", updatedAt: at ?? undefined });

const micros = (c: Contact): bigint =>
  c.updatedAt ? c.updatedAt.seconds * 1_000_000n + BigInt(c.updatedAt.nanos / 1000) : 0n;

/** The users the user has a state with, newest change first, leaving out deleted accounts. */
export async function listContacts(db: Pool, userId: UUID): Promise<Contact[]> {
  const all: ListedRow[] = [];
  for (const query of listQueries) all.push(...(await timedRows<ListedRow>(db, query, [userId, maxPerState])));
  const users = await profiles(
    db,
    all.map((l) => l.other_user_id),
  );
  const list: Contact[] = [];
  for (const l of all) {
    const user = users.get(hex(l.other_user_id));
    if (user && user.name !== "" && !user.deleted)
      list.push(contactOf(user, side(l.own_state, l.own_at), side(l.peer_state, l.peer_at)));
  }
  return list.sort((a, b) => {
    const d = micros(b) - micros(a);
    return d > 0n ? 1 : d < 0n ? -1 : 0;
  });
}

const unblockFirst = () => failedPrecondition(ErrorReason.BLOCKED_BY_YOU, "unblock this user first");

/** The profile of a user who exists and is not deleted. */
async function existing(db: Pool, userId: UUID): Promise<User> {
  const user = await profile(db, userId);
  if (!user || user.deleted) throw notFound("no such user");
  return user;
}

/** Asks a user to be a contact, or accepts their request. */
export async function addContact(db: Pool, userId: UUID, req: AddContactRequest): Promise<Contact> {
  let otherId: UUID | undefined;
  switch (req.user.case) {
    case "name":
      if (validName(req.user.value)) otherId = await resolve(db, req.user.value);
      if (!otherId) throw notFound("no such user");
      break;
    case "userId":
      otherId = parseUserId(req.user.value);
      break;
    default:
      throw invalidArgument("name or user_id is required");
  }
  if (sameId(otherId, userId)) throw invalidArgument("you can't add yourself");
  const other = await existing(db, otherId);
  const { own } = await sides(db, userId, otherId);
  if (own.state === Block) throw unblockFirst();
  if (await blocks(db, otherId, userId)) throw permissionDenied("can't add this user");
  if (own.state !== Request) {
    let refused = false as boolean;
    await change(db, userId, otherId, (o) => {
      refused = o === Block;
      return [Request, o === ""];
    });
    if (refused) throw unblockFirst();
  }
  const after = await sides(db, userId, otherId);
  return contactOf(other, after.own, after.peer);
}

/**
 * Ends any contact or request with the user, for both: the user's own
 * request goes, and the outbox row is written even without one, so that the
 * fan-out ends the other's request. A block stays. An unknown user, or
 * oneself, changes nothing.
 */
export async function removeContact(db: Pool, userId: UUID, other: Uint8Array): Promise<void> {
  const otherId = parseUserId(other);
  if (sameId(otherId, userId)) return;
  if (!(await profile(db, otherId))) return;
  await change(db, userId, otherId, (own) => [own === Request ? "" : own, true]);
}

/** Makes the user's side a block; the fan-out ends both requests. */
export async function blockUser(db: Pool, userId: UUID, other: Uint8Array): Promise<void> {
  const otherId = parseUserId(other);
  if (sameId(otherId, userId)) throw invalidArgument("you can't block yourself");
  await existing(db, otherId);
  await change(db, userId, otherId, (own) => [Block, own !== Block]);
}

/** Lifts the user's block; no request comes back. */
export async function unblockUser(db: Pool, userId: UUID, other: Uint8Array): Promise<void> {
  const otherId = parseUserId(other);
  await change(db, userId, otherId, (own) => ["", own === Block]);
}

/**
 * Refuses a direct conversation, or a message or reaction in one, between
 * users when either blocked the other: the user's own block first, and the
 * other's without saying more.
 */
export async function checkMessaging(db: Pool, userId: UUID, peerId: UUID): Promise<void> {
  if (await blocks(db, userId, peerId)) throw failedPrecondition(ErrorReason.BLOCKED_BY_YOU, "you blocked this user");
  if (await blocks(db, peerId, userId)) throw permissionDenied("can't message this user");
}

/** Which of the others are the user's contacts, by hex user id: both sides a request, each read from its owner's row. */
export async function contactsAmong(db: Pool, userId: UUID, others: UUID[]): Promise<Set<string>> {
  const result = new Set<string>();
  if (others.length === 0) return result;
  const asked = await rows<{ other_user_id: Buffer }>(
    db,
    `SELECT other_user_id FROM contact
     WHERE user_id = ? AND state = 'request' AND other_user_id IN (${placeholders(others.length)})`,
    [userId, ...others],
  );
  if (asked.length === 0) return result;
  const both = await rows<{ user_id: Buffer }>(
    db,
    `SELECT user_id FROM contact
     WHERE user_id IN (${placeholders(asked.length)}) AND other_user_id = ? AND state = 'request'`,
    [...asked.map((r) => r.other_user_id), userId],
  );
  for (const r of both) result.add(hex(r.user_id));
  return result;
}
