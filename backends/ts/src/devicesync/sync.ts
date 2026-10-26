// Reading the user's event log.
import { create, fromBinary } from "@bufbuild/protobuf";
import type { Pool } from "mysql2/promise";
import { EventSchema } from "../gen/wrinkle/v1/event_pb.js";
import { SyncResponseSchema, type SyncResponse } from "../gen/wrinkle/v1/messaging_pb.js";
import { cursorExpired } from "../internal/apierror.ts";
import { row, rows } from "../internal/db.ts";
import type { UUID } from "../internal/ids.ts";
import { maxPageBytes } from "../internal/page.ts";

const defaultSyncLimit = 100;
const maxSyncLimit = 1000;

/** Events after the cursor, oldest first, or fails when the retention dropped some of them. */
export async function sync(db: Pool, userId: UUID, cursor: bigint, limit: number): Promise<SyncResponse> {
  const n = Math.min(limit === 0 ? defaultSyncLimit : limit, maxSyncLimit);
  const list = await rows<{ payload: Buffer }>(
    db,
    `SELECT payload FROM user_event WHERE user_id = ? AND position > ? ORDER BY position LIMIT ?`,
    [userId, cursor, n + 1],
  );
  const resp = create(SyncResponseSchema, { cursor, hasMore: list.length > n });
  let size = 0;
  for (const r of list.slice(0, n)) {
    if (resp.events.length > 0 && size + r.payload.length > maxPageBytes) {
      resp.hasMore = true;
      break;
    }
    size += r.payload.length;
    const event = fromBinary(EventSchema, r.payload);
    resp.events.push(event);
    resp.cursor = event.cursor;
  }
  // After the events: the trimmer raises the tail in the transaction that
  // drops them, so events read before the drop are all there, and a drop
  // that came first shows here.
  const tail =
    (await row<{ log_tail: bigint }>(db, `SELECT log_tail FROM user WHERE user_id = ?`, [userId]))?.log_tail ?? 0n;
  if (cursor < tail) throw cursorExpired(`cursor ${cursor} is before the log's oldest kept event, after ${tail}`);
  return resp;
}
