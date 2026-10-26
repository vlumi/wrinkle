// Settling a conversation row's attachment uploads before its delivery.
import type { Pool } from "mysql2/promise";
import { exec, placeholders } from "../internal/db.ts";
import { hex, parseUUID, type UUID } from "../internal/ids.ts";
import type { OutboxRow } from "./outbox.ts";

/**
 * Ends the wait of a delivered message's attachments, which the sweeper
 * would otherwise take for a failed send's, and releases a deleted
 * message's, for the sweeper to delete. Both are idempotent, so a record
 * delivered again changes nothing.
 */
export async function settleUploads(db: Pool, o: OutboxRow): Promise<void> {
  const msg = o.event.body.case === "message" ? o.event.body.value.message : undefined;
  if (msg && msg.blobIds.length > 0) {
    const sender = parseUUID(msg.senderUserId);
    if (!sender) return;
    const blobIds = msg.blobIds.map(parseUUID).filter((id) => id !== undefined);
    await updateUploads(db, `UPDATE upload SET pending_since = NULL`, sender, blobIds);
    return;
  }
  if (o.releasedBy && o.releasedUploads.length > 0) {
    await updateUploads(db, `UPDATE upload SET used = FALSE, pending_since = NULL`, o.releasedBy, o.releasedUploads);
  }
}

async function updateUploads(db: Pool, update: string, userId: UUID, blobIds: UUID[]): Promise<void> {
  if (blobIds.length === 0) return;
  try {
    await exec(
      db,
      `${update} WHERE user_id = ? AND purpose = 'attachment' AND blob_id IN (${placeholders(blobIds.length)})`,
      [userId, ...blobIds],
    );
  } catch (err) {
    throw new Error(`uploads of user ${hex(userId)}: ${(err as Error).message}`, { cause: err });
  }
}
