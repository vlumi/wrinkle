// A user's palette of up to 200 saved stickers, in their order. Every change
// runs under the lock on the user row and writes the whole palette to the
// user's outbox, so the user's other devices follow.
import { create, toBinary } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { PoolConnection } from "mysql2/promise";
import { EventSchema } from "../gen/wrinkle/v1/event_pb.js";
import type { Sticker } from "../gen/wrinkle/v1/model_pb.js";
import { failedPrecondition, notFound, unauthenticated } from "../internal/apierror.ts";
import { exec, now, row, rows, tx } from "../internal/db.ts";
import { sameId, type UUID } from "../internal/ids.ts";
import type { Deps } from "../services/deps.ts";
import { load, parseId } from "./stickers.ts";

const maxPalette = 200;

interface Palette {
  c: PoolConnection;
  now: Date;
  order: UUID[];
  positions: number[];
}

export async function palette(deps: Deps, userId: UUID): Promise<Sticker[]> {
  const order = await rows<{ sticker_id: Buffer }>(
    deps.users,
    `SELECT sticker_id FROM palette_sticker WHERE user_id = ? ORDER BY position`,
    [userId],
  );
  return load(
    deps,
    order.map((r) => r.sticker_id),
  );
}

export async function save(deps: Deps, userId: UUID, sticker: Uint8Array): Promise<void> {
  const stickerId = parseId(sticker, "sticker_id");
  if ((await load(deps, [stickerId])).length === 0) throw notFound("no such sticker");
  await change(deps, userId, async (p) => {
    if (p.order.some((id) => sameId(id, stickerId))) return false;
    if (p.order.length >= maxPalette) throw failedPrecondition(`the palette is full, at ${maxPalette} stickers`);
    const position = p.order.length > 0 ? p.positions[p.positions.length - 1]! + 1 : 0;
    await exec(p.c, `INSERT INTO palette_sticker (user_id, sticker_id, position, saved_at) VALUES (?, ?, ?, ?)`, [
      userId,
      stickerId,
      position,
      p.now,
    ]);
    p.order.push(stickerId);
    return true;
  });
}

export async function remove(deps: Deps, userId: UUID, sticker: Uint8Array): Promise<void> {
  const stickerId = parseId(sticker, "sticker_id");
  await change(deps, userId, async (p) => {
    const i = p.order.findIndex((id) => sameId(id, stickerId));
    if (i < 0) return false;
    await exec(p.c, `DELETE FROM palette_sticker WHERE user_id = ? AND sticker_id = ?`, [userId, stickerId]);
    p.order.splice(i, 1);
    return true;
  });
}

/** Moves a saved sticker before another, or to the end; renumbers the positions that change. */
export async function move(deps: Deps, userId: UUID, sticker: Uint8Array, before: Uint8Array): Promise<void> {
  const stickerId = parseId(sticker, "sticker_id");
  const beforeId = before.length > 0 ? parseId(before, "before_sticker_id") : undefined;
  await change(deps, userId, async (p) => {
    const i = p.order.findIndex((id) => sameId(id, stickerId));
    if (i < 0) throw notFound("the sticker is not in the palette");
    if (beforeId && !p.order.some((id) => sameId(id, beforeId))) {
      throw notFound("the sticker to move before is not in the palette");
    }
    if (beforeId && sameId(beforeId, stickerId)) return false;
    const order = p.order.filter((_, k) => k !== i);
    const j = beforeId ? order.findIndex((id) => sameId(id, beforeId)) : order.length;
    order.splice(j, 0, stickerId);
    if (order.every((id, k) => sameId(id, p.order[k]))) return false;
    for (const [k, id] of order.entries()) {
      if (p.positions[p.order.findIndex((o) => sameId(o, id))] === k) continue;
      await exec(p.c, `UPDATE palette_sticker SET position = ? WHERE user_id = ? AND sticker_id = ?`, [k, userId, id]);
    }
    p.order = order;
    return true;
  });
}

async function change(deps: Deps, userId: UUID, edit: (p: Palette) => Promise<boolean>): Promise<void> {
  await tx(deps.users, async (c) => {
    const user = await row<{ last_outbox_id: bigint; deleted_at: Date | null }>(
      c,
      `SELECT last_outbox_id, deleted_at FROM user WHERE user_id = ? FOR UPDATE`,
      [userId],
    );
    if (!user) throw new Error("no such user");
    if (user.deleted_at) throw unauthenticated("the account is deleted");
    const saved = await rows<{ sticker_id: Buffer; position: number }>(
      c,
      `SELECT sticker_id, position FROM palette_sticker WHERE user_id = ? ORDER BY position`,
      [userId],
    );
    const p: Palette = {
      c,
      now: now(),
      order: saved.map((s) => s.sticker_id),
      positions: saved.map((s) => s.position),
    };
    if (!(await edit(p))) return;
    const payload = toBinary(
      EventSchema,
      create(EventSchema, {
        occurredAt: timestampFromDate(p.now),
        body: { case: "palette", value: { stickerIds: p.order } },
      }),
    );
    const outboxId = user.last_outbox_id + 1n;
    await exec(
      c,
      `INSERT INTO user_outbox (user_id, outbox_id, kind, payload, created_at) VALUES (?, ?, 'palette', ?, ?)`,
      [userId, outboxId, Buffer.from(payload), p.now],
    );
    await exec(c, `DELETE FROM user_outbox WHERE user_id = ? AND outbox_id = ?`, [userId, outboxId]);
    await exec(c, `UPDATE user SET last_outbox_id = ? WHERE user_id = ?`, [outboxId, userId]);
  });
}
