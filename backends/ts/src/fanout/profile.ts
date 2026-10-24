// Profile changes and deleted accounts: to the user and every direct
// conversation partner.
import { create } from "@bufbuild/protobuf";
import { PushSchema, SignedOutSchema } from "../gen/wrinkle/backend/v1/services_pb.js";
import { exec, nullBytes, rows } from "../internal/db.ts";
import { hex, type UUID } from "../internal/ids.ts";
import type { Delivery } from "./deliver.ts";
import { eachLimited } from "./limit.ts";
import type { UserOutboxRow } from "./outbox.ts";

/** How many conversation partners are read at a time. */
export const profilePage = 500;
/** How many partners of one user are delivered to at a time. */
export const profileConcurrency = 16;
/** How many rows one statement of a deleted user's purge removes. */
const purgePage = 1000;

const wrap = (userId: UUID, err: unknown) =>
  new Error(`deliver to user ${hex(userId)}: ${(err as Error).message}`, { cause: err });

/**
 * Appends a profile change to the user's own log, then to each direct
 * conversation partner's, with the partner's list entry updated in the
 * same transaction. A deleted user's log gets nothing.
 */
export async function deliverProfile(d: Delivery, o: UserOutboxRow): Promise<void> {
  const user = o.event.body.case === "profile" ? o.event.body.value.user : undefined;
  await d.append(o.userId, o.userId, o.outboxId, o.event).catch((err: unknown) => {
    throw wrap(o.userId, err);
  });
  let after: UUID = Buffer.alloc(16);
  for (;;) {
    const page = await partners(d, o.userId, after);
    await eachLimited(page, profileConcurrency, async (p) => {
      try {
        await d.append(p.peerId, o.userId, o.outboxId, o.event, async (c) => {
          await exec(
            c,
            `UPDATE user_conversation SET peer_name = ?, peer_icon = ?, peer_avatar = ?, peer_deleted = ?
             WHERE user_id = ? AND conversation_id = ?`,
            [
              user?.name ?? "",
              user?.icon ?? "",
              nullBytes(user?.avatar),
              user?.deleted ?? false,
              p.peerId,
              p.conversationId,
            ],
          );
        });
      } catch (err) {
        throw wrap(p.peerId, err);
      }
    });
    if (page.length < profilePage) return;
    after = page[page.length - 1]!.conversationId;
  }
}

interface Partner {
  conversationId: UUID;
  peerId: UUID;
}

/** A page of the user's direct conversations after the given one, in id order. */
async function partners(d: Delivery, userId: UUID, after: UUID): Promise<Partner[]> {
  const found = await rows<{ conversation_id: Buffer; peer_user_id: Buffer }>(
    d.db,
    `SELECT conversation_id, peer_user_id FROM user_conversation
     WHERE user_id = ? AND conversation_id > ? AND peer_user_id IS NOT NULL
     ORDER BY conversation_id LIMIT ?`,
    [userId, after, profilePage],
  );
  return found.map((r) => ({ conversationId: r.conversation_id, peerId: r.peer_user_id }));
}

/**
 * Tells the partners of a deleted user as of a profile change, closes the
 * user's connections and removes the user's log, conversation list,
 * devices, palette and contacts (other users' rows toward the user stay);
 * the uploads go back to unused, for the sweeper, except those made
 * stickers, which outlive the account. The user row stays as a tombstone.
 * Each step may run again on redelivery.
 */
export async function deliverDeletion(d: Delivery, o: UserOutboxRow): Promise<void> {
  await deliverProfile(d, o);
  await d.publish(
    o.userId,
    create(PushSchema, { kind: { case: "signedOut", value: create(SignedOutSchema, { allDevices: true }) } }),
  );
  for (const table of ["user_event", "user_conversation", "device", "palette_sticker", "contact", "contact_peer"]) {
    for (;;) {
      let affected: number;
      try {
        affected = (await exec(d.db, `DELETE FROM ${table} WHERE user_id = ? LIMIT ?`, [o.userId, purgePage]))
          .affectedRows;
      } catch (err) {
        throw new Error(`purge ${table} of user ${hex(o.userId)}: ${(err as Error).message}`, { cause: err });
      }
      if (affected < purgePage) break;
    }
  }
  try {
    await exec(d.db, "UPDATE upload SET used = FALSE WHERE user_id = ? AND purpose <> 'sticker'", [o.userId]);
  } catch (err) {
    throw new Error(`release uploads of user ${hex(o.userId)}: ${(err as Error).message}`, { cause: err });
  }
}
