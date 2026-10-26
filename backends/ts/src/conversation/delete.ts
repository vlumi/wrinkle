// Deleting a message for everyone.
import { create } from "@bufbuild/protobuf";
import { MessageDeletedEventSchema } from "../gen/wrinkle/v1/event_pb.js";
import { MessageSchema } from "../gen/wrinkle/v1/model_pb.js";
import { invalidArgument, notFound, permissionDenied } from "../internal/apierror.ts";
import { exec, now, row, timestampOf } from "../internal/db.ts";
import { hex, randomId, type UUID } from "../internal/ids.ts";
import type { Deps } from "../services/deps.ts";
import { coldTx } from "./cold.ts";
import {
  decodeBlobIds,
  decodeBody,
  encodeBody,
  event,
  has,
  insertOutbox,
  load,
  notInConversation,
  selectStateForUpdate,
} from "./state.ts";

/**
 * Deletes one of the user's own messages for everyone: its body keeps only
 * the deletion time, and the event tells the current members, and the user
 * when no longer one, so that their logs and lists drop the content. A
 * former member may delete what they sent before leaving. Deleting a
 * deleted message again does nothing. The outbox row names the
 * attachments' uploads, which the fan-out releases for the sweeper. A
 * message in the cold tier is changed there, under the conversation's row
 * lock as in MySQL.
 */
export async function deleteMessage(deps: Deps, userId: UUID, conversationId: UUID, seq: bigint): Promise<void> {
  if (seq === 0n) throw invalidArgument("seq must be positive");
  await coldTx(deps.conversations, deps.cold, async (c, change) => {
    const st = await load(c, selectStateForUpdate, conversationId);
    const m = await row<{ joined_seq: bigint; left_seq: bigint | null }>(
      c,
      `SELECT joined_seq, left_seq FROM conversation_member WHERE conversation_id = ? AND user_id = ?`,
      [conversationId, userId],
    );
    if (!m) throw notInConversation(userId, conversationId);
    const missing = notFound(`message ${seq} of conversation ${hex(conversationId)} does not exist`);
    if (seq < m.joined_seq || (m.left_seq !== null && seq > m.left_seq)) throw missing;
    const inCold = seq <= st.coldSeq;
    const cold = inCold ? await deps.cold.get(conversationId, seq) : undefined;
    const stored = inCold
      ? cold && { sender_user_id: cold.sender, body: cold.body, blob_ids: cold.blobIds, outbox_id: cold.outboxId }
      : await row<{ sender_user_id: Buffer; body: Buffer; blob_ids: Buffer | null; outbox_id: bigint }>(
          c,
          `SELECT sender_user_id, body, blob_ids, outbox_id FROM message WHERE conversation_id = ? AND seq = ? FOR UPDATE`,
          [conversationId, seq],
        );
    if (!stored) throw missing;
    const msg = create(MessageSchema);
    decodeBody(stored.body, msg);
    if (msg.content.case === "notice") throw invalidArgument(`message ${seq} is a notice`);
    if (!stored.sender_user_id.equals(userId)) throw permissionDenied(`message ${seq} is not the user's own`);
    if (msg.deletedAt) return;
    const at = now();
    const deletedAt = timestampOf(at);
    const body = Buffer.from(encodeBody({ content: { case: undefined }, deletedAt }));
    if (cold) {
      const deleted = { ...cold, body, blobIds: Buffer.alloc(0), reactions: Buffer.alloc(0) };
      await change(conversationId, cold, deleted);
    } else {
      await exec(c, `UPDATE message SET body = ?, blob_ids = '' WHERE conversation_id = ? AND seq = ?`, [
        body,
        conversationId,
        seq,
      ]);
      await exec(c, `DELETE FROM reaction WHERE conversation_id = ? AND seq = ?`, [conversationId, seq]);
    }
    const blobs = decodeBlobIds(stored.blob_ids);
    const recipients = has(st.members, userId) ? st.members : [...st.members, userId];
    await insertOutbox(c, {
      conversationId,
      outboxId: randomId(),
      recipients,
      st,
      kind: "message_deleted",
      target: stored.outbox_id,
      releasedUploads: blobs.length > 0 ? Buffer.concat([userId, ...blobs]) : undefined,
      event: event(deletedAt, {
        case: "messageDeleted",
        value: create(MessageDeletedEventSchema, { conversationId, seq, deletedAt }),
      }),
      at,
    });
  });
}
