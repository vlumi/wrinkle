// Reading the user's event log.
import { create, fromBinary } from "@bufbuild/protobuf";
import type { Pool } from "mysql2/promise";
import { EventSchema } from "../gen/wrinkle/v1/event_pb.js";
import { SyncResponseSchema, type SyncResponse } from "../gen/wrinkle/v1/messaging_pb.js";
import { rows } from "../internal/db.ts";
import type { UUID } from "../internal/ids.ts";

const defaultSyncLimit = 100;
const maxSyncLimit = 1000;

/** Events after the cursor, oldest first. */
export async function sync(db: Pool, userId: UUID, cursor: bigint, limit: number): Promise<SyncResponse> {
  const n = Math.min(limit === 0 ? defaultSyncLimit : limit, maxSyncLimit);
  const list = await rows<{ payload: Buffer }>(
    db,
    `SELECT payload FROM user_event WHERE user_id = ? AND position > ? ORDER BY position LIMIT ?`,
    [userId, cursor, n + 1],
  );
  const resp = create(SyncResponseSchema, { cursor, hasMore: list.length > n });
  for (const r of list.slice(0, n)) {
    const event = fromBinary(EventSchema, r.payload);
    resp.events.push(event);
    resp.cursor = event.cursor;
  }
  return resp;
}
