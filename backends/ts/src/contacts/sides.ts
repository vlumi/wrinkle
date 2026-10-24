// A user's side toward another, and the copy of the other's: how they are
// stored, how they read, and the one way the own side changes.
import { create, toBinary } from "@bufbuild/protobuf";
import type { Pool } from "mysql2/promise";
import { ContactEventSchema, EventSchema } from "../gen/wrinkle/v1/event_pb.js";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb.js";
import { ContactSchema, ContactState, UserSchema, type Contact, type User } from "../gen/wrinkle/v1/model_pb.js";
import { codeOf, unauthenticated } from "../internal/apierror.ts";
import { exec, microsOf, now, row, timedRow, timestampOf, tx, type DbTime, type Querier } from "../internal/db.ts";
import type { UUID } from "../internal/ids.ts";

// The states of a side as stored; contact_peer also keeps none once the
// other's side ended. Empty is no row.
export const Request = "request";
export const Block = "block";
export const None = "none";

/** One user's row toward another: its state, empty when there is no row, and when it last changed. */
export interface Side {
  state: string;
  updatedAt?: DbTime;
}

/** How a user sees another, from the own side and the copy of the other's. A block by the other is never revealed. */
export function view(own: string, peer: string): ContactState {
  if (own === Block) return ContactState.BLOCKED;
  if (own === Request && peer === Request) return ContactState.CONTACT;
  if (own === Request) return ContactState.OUTGOING;
  if (own === "" && peer === Request) return ContactState.INCOMING;
  return ContactState.UNSPECIFIED;
}

/** The other user as the user sees them, changed when either row last changed. */
export function contactOf(other: User, own: Side, peer: Side): Contact {
  const c = create(ContactSchema, { user: other, state: view(own.state, peer.state) });
  let at = own.updatedAt;
  if (peer.updatedAt && (!at || microsOf(peer.updatedAt) > microsOf(at))) at = peer.updatedAt;
  if (at) c.updatedAt = timestampOf(at);
  return c;
}

/** The side an outbox row's state names: the user's own side after the change. */
export function payloadSide(state: ContactState): string {
  if (state === ContactState.OUTGOING) return Request;
  if (state === ContactState.BLOCKED) return Block;
  return None;
}

export function payloadState(side: string): ContactState {
  if (side === Request) return ContactState.OUTGOING;
  if (side === Block) return ContactState.BLOCKED;
  return ContactState.UNSPECIFIED;
}

async function readSide(q: Querier, table: "contact" | "contact_peer", userId: UUID, otherId: UUID): Promise<Side> {
  const r = await timedRow<{ state: string; updated_at: DbTime }>(
    q,
    `SELECT state, updated_at FROM ${table} WHERE user_id = ? AND other_user_id = ?`,
    [userId, otherId],
  );
  return r ? { state: r.state, updatedAt: r.updated_at } : { state: "" };
}

/** The user's own side toward the other and the copy of the other's, both on the user's shard. */
export async function sides(q: Querier, userId: UUID, otherId: UUID): Promise<{ own: Side; peer: Side }> {
  const own = await readSide(q, "contact", userId, otherId);
  const peer = await readSide(q, "contact_peer", userId, otherId);
  return { own, peer };
}

/** Whether the user blocked the other, from the user's own row. */
export async function blocks(q: Querier, userId: UUID, otherId: UUID): Promise<boolean> {
  return (await readSide(q, "contact", userId, otherId)).state === Block;
}

/**
 * Stores the other user's side, as of their outbox row, in the user's
 * contact_peer, unless a later row of theirs is already there, and reports
 * whether the stored copy is this row's.
 */
export async function copyPeer(
  db: Pool,
  userId: UUID,
  otherId: UUID,
  side: string,
  outboxId: bigint,
  at: Date,
): Promise<boolean> {
  await exec(
    db,
    `INSERT INTO contact_peer (user_id, other_user_id, state, source_outbox_id, updated_at) VALUES (?, ?, ?, ?, ?)
     ON DUPLICATE KEY UPDATE
       state = IF(VALUES(source_outbox_id) > source_outbox_id, VALUES(state), state),
       updated_at = IF(VALUES(source_outbox_id) > source_outbox_id, VALUES(updated_at), updated_at),
       source_outbox_id = GREATEST(source_outbox_id, VALUES(source_outbox_id))`,
    [userId, otherId, side, outboxId, at],
  );
  const r = await row<{ source_outbox_id: bigint }>(
    db,
    `SELECT source_outbox_id FROM contact_peer WHERE user_id = ? AND other_user_id = ?`,
    [userId, otherId],
  );
  return r?.source_outbox_id === outboxId;
}

/**
 * Ends the user's request toward the other, as RemoveContact by the user
 * would, when there is one: the fan-out calls it once the other's side
 * ended or became a block. A deleted user is left as they are.
 */
export async function endRequest(db: Pool, userId: UUID, otherId: UUID): Promise<void> {
  if ((await readSide(db, "contact", userId, otherId)).state !== Request) return;
  try {
    await change(db, userId, otherId, (own) => ["", own === Request]);
  } catch (err) {
    if (codeOf(err) !== ErrorCode.UNAUTHENTICATED) throw err;
  }
}

/**
 * Runs an edit of the user's side toward the other in one transaction under
 * the lock on the user row, which every change of the user's sides takes.
 * The edit gets the side as it is and returns the side after (empty for
 * none) and whether to write it, with the change in the user's outbox.
 */
export async function change(
  db: Pool,
  userId: UUID,
  otherId: UUID,
  edit: (own: string) => [after: string, write: boolean],
): Promise<void> {
  await tx(db, async (c) => {
    const user = await row<{ last_outbox_id: bigint; deleted_at: Date | null }>(
      c,
      `SELECT last_outbox_id, deleted_at FROM user WHERE user_id = ? FOR UPDATE`,
      [userId],
    );
    if (!user || user.deleted_at !== null) throw unauthenticated("the account is deleted");
    const r = await row<{ state: string }>(
      c,
      `SELECT state FROM contact WHERE user_id = ? AND other_user_id = ? FOR UPDATE`,
      [userId, otherId],
    );
    const own = r?.state ?? "";
    const [after, write] = edit(own);
    if (!write) return;
    const at = now();
    if (after === "") {
      await exec(c, `DELETE FROM contact WHERE user_id = ? AND other_user_id = ?`, [userId, otherId]);
    } else if (after !== own) {
      await exec(
        c,
        `INSERT INTO contact (user_id, other_user_id, state, updated_at) VALUES (?, ?, ?, ?)
         ON DUPLICATE KEY UPDATE state = VALUES(state), updated_at = VALUES(updated_at)`,
        [userId, otherId, after, at],
      );
    }
    const payload = toBinary(
      EventSchema,
      create(EventSchema, {
        occurredAt: timestampOf(at),
        body: {
          case: "contact",
          value: create(ContactEventSchema, {
            contact: create(ContactSchema, {
              user: create(UserSchema, { userId: otherId }),
              state: payloadState(after),
            }),
          }),
        },
      }),
    );
    const lastId = user.last_outbox_id + 1n;
    await exec(
      c,
      `INSERT INTO user_outbox (user_id, outbox_id, kind, payload, created_at) VALUES (?, ?, 'contact', ?, ?)`,
      [userId, lastId, Buffer.from(payload), at],
    );
    // As with the other outboxes: the binlog keeps the insert.
    await exec(c, `DELETE FROM user_outbox WHERE user_id = ? AND outbox_id = ?`, [userId, lastId]);
    await exec(c, `UPDATE user SET last_outbox_id = ? WHERE user_id = ?`, [lastId, userId]);
  });
}
