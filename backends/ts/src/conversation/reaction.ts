// Reactions: each member's sealed reactions to a message, replaced as a whole.
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import type { Pool } from "mysql2/promise";
import { ReactionEventSchema } from "../gen/wrinkle/v1/event_pb.js";
import { MessageSchema, ReactionSchema, SealedSchema, type Message, type Sealed } from "../gen/wrinkle/v1/model_pb.js";
import { failedPrecondition, invalidArgument, notFound } from "../internal/apierror.ts";
import { exec, now, row, timedRows, timestampOf, tx, type DbTime, type Querier } from "../internal/db.ts";
import { hex, randomId, type UUID } from "../internal/ids.ts";
import { checkBlocks, checkEpoch, checkSealed, copySealed, type Conversations } from "./service.ts";
import { decodeBody, event, insertOutbox, member } from "./state.ts";

const maxReactionCiphertext = 1 << 10;

/**
 * Replaces the user's sealed reactions to a message, or removes them when
 * sealed is undefined. Setting the same bytes again, or removing none,
 * changes nothing and tells no one.
 */
export async function setReaction(
  cs: Conversations,
  userId: UUID,
  conversationId: UUID,
  seq: bigint,
  sealed: Sealed | undefined,
): Promise<void> {
  if (seq === 0n) throw invalidArgument("seq must be positive");
  if (sealed) checkSealed(sealed, maxReactionCiphertext);
  await checkBlocks(cs, userId, conversationId);
  await tx(cs.deps.conversations, async (c) => {
    const st = await member(c, conversationId, userId);
    const m = await row<{ joined_seq: bigint }>(
      c,
      `SELECT joined_seq FROM conversation_member WHERE conversation_id = ? AND user_id = ?`,
      [conversationId, userId],
    );
    if (!m) throw new Error(`member row of ${hex(userId)} missing`);
    const missing = notFound(`message ${seq} of conversation ${hex(conversationId)} does not exist`);
    if (seq < m.joined_seq) throw missing;
    const stored = await row<{ body: Buffer }>(c, `SELECT body FROM message WHERE conversation_id = ? AND seq = ?`, [
      conversationId,
      seq,
    ]);
    if (!stored) throw missing;
    const msg = create(MessageSchema);
    decodeBody(stored.body, msg);
    if (msg.content.case === "notice") throw invalidArgument(`message ${seq} is a notice`);
    if (msg.deletedAt) throw failedPrecondition(`message ${seq} is deleted`);
    let newBody: Buffer | undefined;
    if (sealed) {
      checkEpoch(st, sealed.epoch);
      sealed = copySealed(sealed);
      newBody = Buffer.from(toBinary(SealedSchema, sealed));
    }
    const old = await row<{ body: Buffer }>(
      c,
      `SELECT body FROM reaction WHERE conversation_id = ? AND seq = ? AND user_id = ?`,
      [conversationId, seq, userId],
    );
    if ((!sealed && !old) || (newBody && old && old.body.equals(newBody))) return;
    // The column keeps microseconds; the event carries the stored time.
    const at = now();
    if (!newBody) {
      await exec(c, `DELETE FROM reaction WHERE conversation_id = ? AND seq = ? AND user_id = ?`, [
        conversationId,
        seq,
        userId,
      ]);
    } else {
      await exec(
        c,
        `INSERT INTO reaction (conversation_id, seq, user_id, body, updated_at) VALUES (?, ?, ?, ?, ?)
         ON DUPLICATE KEY UPDATE body = ?, updated_at = ?`,
        [conversationId, seq, userId, newBody, at, newBody, at],
      );
    }
    const updatedAt = timestampOf(at);
    await insertOutbox(c, {
      conversationId,
      outboxId: randomId(),
      recipients: st.members,
      st,
      kind: "reaction",
      event: event(updatedAt, {
        case: "reaction",
        value: create(ReactionEventSchema, { conversationId, seq, userId, sealed, updatedAt }),
      }),
      at,
    });
  });
}

/**
 * Fills in the reactions of the messages, which are of one conversation and
 * sorted by seq, newest first, with one query over their range.
 */
export async function addReactions(q: Querier | Pool, conversationId: UUID, messages: Message[]): Promise<void> {
  const bySeq = new Map<bigint, Message>();
  for (const msg of messages) if (!msg.deletedAt && msg.content.case !== "notice") bySeq.set(msg.seq, msg);
  if (bySeq.size === 0) return;
  const list = await timedRows<{ seq: bigint; user_id: Buffer; body: Buffer; updated_at: DbTime }>(
    q,
    `SELECT seq, user_id, body, updated_at FROM reaction
     WHERE conversation_id = ? AND seq >= ? AND seq <= ? ORDER BY seq, updated_at, user_id`,
    [conversationId, messages[messages.length - 1]!.seq, messages[0]!.seq],
  );
  for (const r of list) {
    const msg = bySeq.get(r.seq);
    if (!msg) continue;
    let sealed: Sealed;
    try {
      sealed = fromBinary(SealedSchema, r.body);
    } catch (err) {
      throw new Error(`unmarshal reaction: ${(err as Error).message}`, { cause: err });
    }
    msg.reactions.push(create(ReactionSchema, { userId: r.user_id, sealed, updatedAt: timestampOf(r.updated_at) }));
  }
}
