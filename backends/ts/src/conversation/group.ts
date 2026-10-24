// Groups: creating them, their membership, topic and admins.
import { create } from "@bufbuild/protobuf";
import type { Pool, PoolConnection } from "mysql2/promise";
import {
  AdminChangedSchema,
  ConversationSchema,
  GroupCreatedSchema,
  MemberLeftSchema,
  MemberRemovedSchema,
  MemberSchema,
  MembersAddedSchema,
  MessageSchema,
  NoticeSchema,
  TopicChangedSchema,
  type Conversation,
  type Member,
  type Notice,
  type User,
} from "../gen/wrinkle/v1/model_pb.js";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb.js";
import { profiles } from "../accounts/service.ts";
import { kindOf } from "../devicesync/conversations.ts";
import { codeOf, failedPrecondition, invalidArgument, notFound, permissionDenied } from "../internal/apierror.ts";
import {
  exec,
  isDuplicate,
  now,
  row,
  rows,
  timedRow,
  timestampOf,
  tx,
  type DbTime,
  type Querier,
} from "../internal/db.ts";
import { hex, newUUID, type UUID } from "../internal/ids.ts";
import { validLabel } from "../internal/label.ts";
import type { Deps } from "../services/deps.ts";
import {
  decodeBlobIds,
  decodeBody,
  encodeMembers,
  group,
  has,
  kindGroup,
  load,
  membershipChanged,
  selectState,
  selectStateForUpdate,
  sorted,
  storeNotice,
  without,
  type State,
} from "./state.ts";

const maxTopicBytes = 200;
const maxClientGroupIdBytes = 64;
/** The most members a group has. */
export const maxMembers = 256;

/** Validates a group topic. */
export function checkTopic(topic: string): void {
  if (!validLabel(topic, maxTopicBytes))
    throw invalidArgument(
      `topic must be 1 to ${maxTopicBytes} bytes of UTF-8, without control characters or white space at either end`,
    );
}

/** Validates the id a device chose for a new group. */
export function checkClientGroupId(id: string): void {
  const n = Buffer.byteLength(id, "utf8");
  if (n === 0 || n > maxClientGroupIdBytes || !id.isWellFormed())
    throw invalidArgument(`client_group_id must be 1 to ${maxClientGroupIdBytes} bytes of UTF-8`);
}

const notice = (kind: Notice["kind"]): Notice => create(NoticeSchema, { kind });

/**
 * Creates a group of the creator, its admin, and the invitees, with a
 * GroupCreated notice as its first message. The claim on the creator's
 * client id makes a retry return the group created the first time, and
 * completes one whose creation stopped half-way.
 */
export async function createGroup(
  deps: Deps,
  creatorId: UUID,
  topic: string,
  clientGroupId: string,
  invitees: UUID[],
): Promise<Conversation> {
  checkTopic(topic);
  checkClientGroupId(clientGroupId);
  if (invitees.length + 1 > maxMembers) throw failedPrecondition(`a group has at most ${maxMembers} members`);
  const db = deps.conversations;
  const conversationId = await claimGroup(db, creatorId, clientGroupId);
  const snapshots = await snapshotsOf(deps, invitees);
  await tx(db, async (c) => {
    try {
      await load(c, selectStateForUpdate, conversationId);
      // Created by an earlier attempt.
      return;
    } catch (err) {
      if (codeOf(err) !== ErrorCode.NOT_FOUND) throw err;
    }
    const at = now();
    const joining = [creatorId, ...invitees];
    const st: State = { kind: kindGroup, topic, members: sorted(joining), lastSeq: 1n, keyEpoch: 0, rotate: false };
    await exec(
      c,
      `INSERT INTO conversation (conversation_id, kind, topic, members, last_seq, created_at)
       VALUES (?, ?, ?, ?, 0, ?)`,
      [conversationId, kindGroup, topic, encodeMembers(st.members), at],
    );
    await exec(
      c,
      `INSERT INTO conversation_member (conversation_id, user_id, admin, joined_seq, left_seq, joined_at)
       VALUES ${Array(joining.length).fill("(?, ?, ?, 1, NULL, ?)").join(", ")}`,
      joining.flatMap((id) => [conversationId, id, id.equals(creatorId), at]),
    );
    const created = notice({
      case: "groupCreated",
      value: create(GroupCreatedSchema, { topic, members: snapshots }),
    });
    await storeNotice(c, conversationId, st, creatorId, created, st.members);
  });
  return describe(db, conversationId, creatorId);
}

async function findClaim(db: Querier, creatorId: UUID, clientGroupId: string): Promise<UUID | undefined> {
  const r = await row<{ conversation_id: Buffer }>(
    db,
    `SELECT conversation_id FROM group_claim WHERE creator_user_id = ? AND client_group_id = ?`,
    [creatorId, clientGroupId],
  );
  return r?.conversation_id;
}

/** Whether the creator's client group id already names a group, so that a retry returns it as created. */
export async function groupExists(db: Pool, creatorId: UUID, clientGroupId: string): Promise<boolean> {
  const id = await findClaim(db, creatorId, clientGroupId);
  if (!id) return false;
  try {
    await load(db, selectState, id);
    return true;
  } catch (err) {
    if (codeOf(err) === ErrorCode.NOT_FOUND) return false;
    throw err;
  }
}

/** The group id claimed for the creator's client id, claiming a new one when there is none. */
async function claimGroup(db: Pool, creatorId: UUID, clientGroupId: string): Promise<UUID> {
  const found = await findClaim(db, creatorId, clientGroupId);
  if (found) return found;
  const id = newUUID();
  try {
    await exec(
      db,
      `INSERT INTO group_claim (creator_user_id, client_group_id, conversation_id, created_at) VALUES (?, ?, ?, ?)`,
      [creatorId, clientGroupId, id, now()],
    );
    return id;
  } catch (err) {
    if (!isDuplicate(err)) throw err;
  }
  const again = await findClaim(db, creatorId, clientGroupId);
  if (!again) throw new Error("group claim missing after a duplicate key");
  return again;
}

/** Vets users to add to a group. */
export type CheckUsers = (users: UUID[]) => Promise<void>;

/**
 * Adds the users who are not members yet; any member may. They read the
 * group from the notice that adds them. check vets the users to add,
 * before the group is locked and again for any who left meanwhile.
 */
export async function addMembers(
  deps: Deps,
  userId: UUID,
  conversationId: UUID,
  users: UUID[],
  check: CheckUsers,
): Promise<void> {
  const db = deps.conversations;
  let st = await group(db, selectState, conversationId, userId);
  const checked = notMembers(st, users);
  await check(checked);
  const snapshots = await profiles(deps.users, users);
  await tx(db, async (c) => {
    st = await group(c, selectStateForUpdate, conversationId, userId);
    const added = notMembers(st, users);
    if (added.length === 0) return;
    const unchecked = added.filter((id) => !checked.some((x) => x.equals(id)));
    if (unchecked.length > 0) await check(unchecked);
    if (st.members.length + added.length > maxMembers)
      throw failedPrecondition(`a group has at most ${maxMembers} members`);
    st.lastSeq++;
    const at = now();
    for (const id of added) {
      await exec(
        c,
        `INSERT INTO conversation_member (conversation_id, user_id, admin, joined_seq, left_seq, joined_at)
         VALUES (?, ?, FALSE, ?, NULL, ?)
         ON DUPLICATE KEY UPDATE admin = FALSE, joined_seq = VALUES(joined_seq), left_seq = NULL, joined_at = VALUES(joined_at)`,
        [conversationId, id, st.lastSeq, at],
      );
    }
    st.members = sorted([...st.members, ...added]);
    membershipChanged(st);
    const n = notice({
      case: "membersAdded",
      value: create(MembersAddedSchema, { users: added.map((id) => snapshots.get(hex(id))!) }),
    });
    await storeNotice(c, conversationId, st, userId, n, st.members);
  });
}

/** The users not in the group, each once. */
export function notMembers(st: State, users: UUID[]): UUID[] {
  const list: UUID[] = [];
  for (const id of users) if (!has(st.members, id) && !list.some((x) => x.equals(id))) list.push(id);
  return list;
}

/** Removes another member; only admins may. */
export async function removeMember(deps: Deps, userId: UUID, conversationId: UUID, targetId: UUID): Promise<void> {
  const snapshots = await profiles(deps.users, [targetId]);
  await tx(deps.conversations, async (c) => {
    const st = await group(c, selectStateForUpdate, conversationId, userId);
    await requireAdmin(c, conversationId, userId);
    if (targetId.equals(userId)) throw invalidArgument("removing oneself is leaving the group");
    if (!has(st.members, targetId)) throw notFound(`user ${hex(targetId)} is not in group ${hex(conversationId)}`);
    const before = st.members;
    st.lastSeq++;
    await leftAt(c, conversationId, targetId, st.lastSeq);
    st.members = without(st.members, targetId);
    membershipChanged(st);
    const n = notice({
      case: "memberRemoved",
      value: create(MemberRemovedSchema, { user: snapshots.get(hex(targetId)) }),
    });
    await storeNotice(c, conversationId, st, userId, n, before);
  });
}

/** Takes the user out of a group. When the last admin leaves, the member who joined first becomes admin. */
export async function leave(deps: Deps, userId: UUID, conversationId: UUID): Promise<void> {
  await tx(deps.conversations, async (c) => {
    const st = await group(c, selectStateForUpdate, conversationId, userId);
    const admin = await isAdmin(c, conversationId, userId);
    const before = st.members;
    st.lastSeq++;
    await leftAt(c, conversationId, userId, st.lastSeq);
    st.members = without(st.members, userId);
    membershipChanged(st);
    const left = create(MemberLeftSchema);
    if (admin && st.members.length > 0) left.promoted = await promote(deps, c, conversationId);
    await storeNotice(c, conversationId, st, userId, notice({ case: "memberLeft", value: left }), before);
  });
}

/** Makes the member who joined first an admin when no current member is one, and returns their snapshot. */
async function promote(deps: Deps, c: PoolConnection, conversationId: UUID): Promise<User | undefined> {
  if ((await countAdmins(c, conversationId)) > 0) return undefined;
  const first = await row<{ user_id: Buffer }>(
    c,
    `SELECT user_id FROM conversation_member WHERE conversation_id = ? AND left_seq IS NULL
     ORDER BY joined_seq, joined_at, user_id LIMIT 1`,
    [conversationId],
  );
  if (!first) throw new Error(`no current member of group ${hex(conversationId)} to promote`);
  await exec(c, `UPDATE conversation_member SET admin = TRUE WHERE conversation_id = ? AND user_id = ?`, [
    conversationId,
    first.user_id,
  ]);
  return (await profiles(deps.users, [first.user_id])).get(hex(first.user_id));
}

async function countAdmins(c: PoolConnection, conversationId: UUID): Promise<number> {
  const r = await row<{ n: bigint }>(
    c,
    `SELECT COUNT(*) AS n FROM conversation_member WHERE conversation_id = ? AND left_seq IS NULL AND admin`,
    [conversationId],
  );
  return Number(r?.n ?? 0n);
}

/** Changes a group's topic; any member may. */
export async function setTopic(deps: Deps, userId: UUID, conversationId: UUID, topic: string): Promise<void> {
  checkTopic(topic);
  await tx(deps.conversations, async (c) => {
    const st = await group(c, selectStateForUpdate, conversationId, userId);
    if (st.topic === topic) return;
    st.topic = topic;
    st.lastSeq++;
    const n = notice({ case: "topicChanged", value: create(TopicChangedSchema, { topic }) });
    await storeNotice(c, conversationId, st, userId, n, st.members);
  });
}

/** Makes a member an admin or no longer one; only admins may. A group keeps at least one admin. */
export async function setAdmin(
  deps: Deps,
  userId: UUID,
  conversationId: UUID,
  targetId: UUID,
  admin: boolean,
): Promise<void> {
  const snapshots = await profiles(deps.users, [targetId]);
  await tx(deps.conversations, async (c) => {
    const st = await group(c, selectStateForUpdate, conversationId, userId);
    await requireAdmin(c, conversationId, userId);
    if (!has(st.members, targetId)) throw notFound(`user ${hex(targetId)} is not in group ${hex(conversationId)}`);
    if ((await isAdmin(c, conversationId, targetId)) === admin) return;
    if (!admin && (await countAdmins(c, conversationId)) <= 1)
      throw failedPrecondition("a group keeps at least one admin");
    await exec(c, `UPDATE conversation_member SET admin = ? WHERE conversation_id = ? AND user_id = ?`, [
      admin,
      conversationId,
      targetId,
    ]);
    st.lastSeq++;
    const n = notice({
      case: "adminChanged",
      value: create(AdminChangedSchema, { user: snapshots.get(hex(targetId)), admin }),
    });
    await storeNotice(c, conversationId, st, userId, n, st.members);
  });
}

/** A group's current members, those who joined first first. */
export async function members(deps: Deps, userId: UUID, conversationId: UUID): Promise<Member[]> {
  const db = deps.conversations;
  await group(db, selectState, conversationId, userId);
  const list = await rows<{ user_id: Buffer; admin: number; joined_seq: bigint }>(
    db,
    `SELECT user_id, admin, joined_seq FROM conversation_member
     WHERE conversation_id = ? AND left_seq IS NULL ORDER BY joined_seq, joined_at, user_id`,
    [conversationId],
  );
  const reads = new Map<string, bigint>();
  for (const r of await rows<{ user_id: Buffer; seq: bigint }>(
    db,
    `SELECT user_id, seq FROM read_mark WHERE conversation_id = ?`,
    [conversationId],
  ))
    reads.set(hex(r.user_id), r.seq);
  const users = await profiles(
    deps.users,
    list.map((r) => r.user_id),
  );
  return list.map((r) =>
    create(MemberSchema, {
      user: users.get(hex(r.user_id)),
      admin: Boolean(r.admin),
      joinedSeq: r.joined_seq,
      readSeq: reads.get(hex(r.user_id)) ?? 0n,
    }),
  );
}

/** The conversation as the user sees it now, from the conversation keyspace. */
export async function describe(db: Pool, conversationId: UUID, userId: UUID): Promise<Conversation> {
  const st = await load(db, selectState, conversationId);
  const c = create(ConversationSchema, {
    conversationId,
    kind: kindOf(st.kind),
    topic: st.topic,
    memberCount: st.members.length,
    lastSeq: st.lastSeq,
  });
  const m = await row<{ admin: number; left_seq: bigint | null }>(
    db,
    `SELECT admin, left_seq FROM conversation_member WHERE conversation_id = ? AND user_id = ?`,
    [conversationId, userId],
  );
  c.admin = Boolean(m?.admin);
  c.member = !!m && m.left_seq === null;
  if (st.lastSeq > 0n) {
    const last = await timedRow<{
      sender_user_id: Buffer;
      client_message_id: string;
      body: Buffer;
      blob_ids: Buffer | null;
      sent_at: DbTime;
    }>(
      db,
      `SELECT sender_user_id, client_message_id, body, blob_ids, sent_at FROM message WHERE conversation_id = ? AND seq = ?`,
      [conversationId, st.lastSeq],
    );
    if (last) {
      const msg = create(MessageSchema, {
        conversationId,
        seq: st.lastSeq,
        senderUserId: last.sender_user_id,
        clientMessageId: last.client_message_id,
        sentAt: timestampOf(last.sent_at),
        blobIds: decodeBlobIds(last.blob_ids),
      });
      decodeBody(last.body, msg);
      c.lastMessage = msg;
    }
  }
  const read = await row<{ seq: bigint }>(db, `SELECT seq FROM read_mark WHERE conversation_id = ? AND user_id = ?`, [
    conversationId,
    userId,
  ]);
  c.readSeq = read?.seq ?? 0n;
  return c;
}

/** Fails unless the current member is an admin. */
async function requireAdmin(c: PoolConnection, conversationId: UUID, userId: UUID): Promise<void> {
  if (!(await isAdmin(c, conversationId, userId)))
    throw permissionDenied(`user ${hex(userId)} is not an admin of group ${hex(conversationId)}`);
}

async function isAdmin(c: PoolConnection, conversationId: UUID, userId: UUID): Promise<boolean> {
  const r = await row<{ admin: number }>(
    c,
    `SELECT admin FROM conversation_member WHERE conversation_id = ? AND user_id = ? AND left_seq IS NULL`,
    [conversationId, userId],
  );
  return Boolean(r?.admin);
}

/** Ends the user's membership at the notice with seq. */
async function leftAt(c: PoolConnection, conversationId: UUID, userId: UUID, seq: bigint): Promise<void> {
  await exec(
    c,
    `UPDATE conversation_member SET left_seq = ?, admin = FALSE WHERE conversation_id = ? AND user_id = ?`,
    [seq, conversationId, userId],
  );
}

/** The users' profiles in the order given. */
async function snapshotsOf(deps: Deps, userIds: UUID[]): Promise<User[]> {
  const found = await profiles(deps.users, userIds);
  return userIds.map((id) => found.get(hex(id))!);
}
