// The user's conversation list: reading it, and the entries added when the
// user opens a conversation or creates a group. The fan-out keeps the
// entries current.
import { create, fromBinary } from "@bufbuild/protobuf";
import type { Pool } from "mysql2/promise";
import {
  ConversationKind,
  ConversationSchema,
  MessageSchema,
  UserSchema,
  type Conversation,
  type User,
} from "../gen/wrinkle/v1/model_pb.js";
import { profile } from "../accounts/service.ts";
import { resolve } from "../accounts/names.ts";
import { validName } from "../accounts/validate.ts";
import { checkMessaging } from "../contacts/contacts.ts";
import { invalidArgument, notFound } from "../internal/apierror.ts";
import { exec, now, nullBytes, timedRows, timestampOf, type DbTime } from "../internal/db.ts";
import { hex, participants, sameId, type UUID } from "../internal/ids.ts";

const maxConversationList = 200;

/** The contract's kind of a stored one. */
export function kindOf(kind: string): ConversationKind {
  if (kind === "direct") return ConversationKind.DIRECT;
  if (kind === "group") return ConversationKind.GROUP;
  if (kind === "notes") return ConversationKind.NOTES;
  return ConversationKind.UNSPECIFIED;
}

const conversationColumns = `
  SELECT conversation_id, kind, peer_user_id, peer_name, peer_icon, peer_avatar, peer_deleted,
    topic, member_count, member, admin, last_seq,
    last_sender_user_id, last_client_message_id, last_body, last_sent_at,
    read_seq, peer_read_seq`;

interface ConversationRow {
  conversation_id: Buffer;
  kind: string;
  peer_user_id: Buffer | null;
  peer_name: string;
  peer_icon: string;
  peer_avatar: Buffer | null;
  peer_deleted: number;
  topic: string;
  member_count: number;
  member: number;
  admin: number;
  last_seq: bigint;
  last_sender_user_id: Buffer | null;
  last_client_message_id: string | null;
  last_body: Buffer | null;
  last_sent_at: DbTime | null;
  read_seq: bigint;
  peer_read_seq: bigint;
}

function toConversation(r: ConversationRow): Conversation {
  const c = create(ConversationSchema, {
    conversationId: r.conversation_id,
    kind: kindOf(r.kind),
    topic: r.topic,
    memberCount: r.member_count,
    member: Boolean(r.member),
    admin: Boolean(r.admin),
    lastSeq: r.last_seq,
    readSeq: r.read_seq,
    peerReadSeq: r.peer_read_seq,
  });
  if (r.peer_user_id) {
    c.peer = create(UserSchema, {
      userId: r.peer_user_id,
      name: r.peer_name,
      icon: r.peer_icon,
      avatar: r.peer_avatar ?? new Uint8Array(),
      deleted: Boolean(r.peer_deleted),
    });
  }
  if (r.last_seq > 0n && r.last_sender_user_id) {
    let content;
    try {
      content = fromBinary(MessageSchema, r.last_body ?? new Uint8Array());
    } catch (err) {
      throw new Error(`last message of conversation ${hex(r.conversation_id)}: ${(err as Error).message}`, {
        cause: err,
      });
    }
    c.lastMessage = create(MessageSchema, {
      conversationId: r.conversation_id,
      seq: r.last_seq,
      senderUserId: r.last_sender_user_id,
      clientMessageId: r.last_client_message_id ?? "",
      sentAt: timestampOf(r.last_sent_at ?? new Date(0)),
      content: content.content,
      deletedAt: content.deletedAt,
      blobIds: content.blobIds,
    });
  }
  return c;
}

/** The user's conversations, most recently active first. */
export async function listConversations(db: Pool, userId: UUID): Promise<Conversation[]> {
  const list = await timedRows<ConversationRow>(
    db,
    conversationColumns + ` FROM user_conversation WHERE user_id = ? ORDER BY updated_at DESC LIMIT ?`,
    [userId, maxConversationList],
  );
  return list.map(toConversation);
}

/** The user's list entry of the conversation. */
export async function listed(db: Pool, userId: UUID, conversationId: UUID): Promise<Conversation> {
  const list = await timedRows<ConversationRow>(
    db,
    conversationColumns + ` FROM user_conversation WHERE user_id = ? AND conversation_id = ?`,
    [userId, conversationId],
  );
  if (!list[0]) throw new Error(`conversation ${hex(conversationId)} missing after insert`);
  return toConversation(list[0]);
}

/** Adds a direct conversation to the user's list, unless it is there already. */
export async function insertDirectEntry(db: Pool, userId: UUID, conversationId: UUID, peer: User): Promise<void> {
  await exec(
    db,
    `INSERT IGNORE INTO user_conversation
       (user_id, conversation_id, kind, peer_user_id, peer_name, peer_icon, peer_avatar, peer_deleted,
        member, last_seq, read_seq, peer_read_seq, updated_at)
     VALUES (?, ?, 'direct', ?, ?, ?, ?, ?, TRUE, 0, 0, 0, ?)`,
    [
      userId,
      conversationId,
      Buffer.from(peer.userId),
      peer.name,
      peer.icon,
      nullBytes(peer.avatar),
      peer.deleted,
      now(),
    ],
  );
}

/** Adds the user's notes to their list, unless they are there already. */
export async function insertNotesEntry(db: Pool, userId: UUID, conversationId: UUID): Promise<void> {
  await exec(
    db,
    `INSERT IGNORE INTO user_conversation
       (user_id, conversation_id, kind, member_count, member, last_seq, read_seq, peer_read_seq, updated_at)
     VALUES (?, ?, 'notes', 1, TRUE, 0, 0, 0, ?)`,
    [userId, conversationId, now()],
  );
}

/** Adds a group the user created to their list, unless it is there already; the members' lists get it from the fan-out. */
export async function insertGroupEntry(
  db: Pool,
  userId: UUID,
  conversationId: UUID,
  topic: string,
  memberCount: number,
): Promise<void> {
  await exec(
    db,
    `INSERT IGNORE INTO user_conversation
       (user_id, conversation_id, kind, topic, member_count, member, admin, last_seq, read_seq, peer_read_seq, updated_at)
     VALUES (?, ?, 'group', ?, ?, TRUE, TRUE, 0, 0, 0, ?)`,
    [userId, conversationId, topic, memberCount, now()],
  );
}

/**
 * Finds or creates the conversation with the named peer: open finds or
 * creates it in the conversation keyspace for the pair, smaller id first.
 */
export async function openDirect(
  db: Pool,
  userId: UUID,
  peerName: string,
  open: (userA: UUID, userB: UUID) => Promise<UUID>,
): Promise<Conversation> {
  if (!validName(peerName)) throw invalidArgument("peer_name is not a valid name");
  const peerId = await resolve(db, peerName);
  if (!peerId) throw notFound(`no user named ${JSON.stringify(peerName)}`);
  if (sameId(peerId, userId)) throw invalidArgument("your own notes are opened with OpenNotes");
  await checkMessaging(db, userId, peerId);
  const peer = await profile(db, peerId);
  if (!peer) throw notFound(`no user named ${JSON.stringify(peerName)}`);
  const [userA, userB] = participants(userId, peerId);
  const conversationId = await open(userA, userB);
  await insertDirectEntry(db, userId, conversationId, peer);
  return listed(db, userId, conversationId);
}

/** Finds or creates the user's notes: open finds or creates them in the conversation keyspace. */
export async function openNotes(db: Pool, userId: UUID, open: () => Promise<UUID>): Promise<Conversation> {
  const conversationId = await open();
  await insertNotesEntry(db, userId, conversationId);
  return listed(db, userId, conversationId);
}
