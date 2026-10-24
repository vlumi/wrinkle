// Devices' public keys.
import { create } from "@bufbuild/protobuf";
import type { Pool } from "mysql2/promise";
import { UserDevicesSchema, type UserDevices } from "../gen/wrinkle/v1/messaging_pb.js";
import { DeviceSchema } from "../gen/wrinkle/v1/model_pb.js";
import { failedPrecondition, invalidArgument } from "../internal/apierror.ts";
import { exec, placeholders, row, rows, tx } from "../internal/db.ts";
import { hex, parseUUID, sameId, type UUID } from "../internal/ids.ts";
import { lockSignedIn } from "./devices.ts";
import type { AccountsDeps } from "./service.ts";

const publicKeyBytes = 32;
const maxKeyUsers = 256;

/**
 * Stores the device's public key, once; the same key again does nothing.
 * Partners and group co-members learn it from the DeviceEvent.
 */
export async function setDeviceKey(d: AccountsDeps, userId: UUID, deviceId: bigint, key: Uint8Array): Promise<void> {
  if (key.length !== publicKeyBytes) throw invalidArgument(`public_key must be ${publicKeyBytes} bytes`);
  await tx(d.db, async (c) => {
    const o = await lockSignedIn(c, userId, deviceId);
    const r = await row<{ public_key: Buffer | null }>(
      c,
      `SELECT public_key FROM device WHERE user_id = ? AND device_id = ?`,
      [userId, deviceId],
    );
    const current = r?.public_key ?? null;
    if (current !== null && sameId(current, key)) return;
    if (current !== null) throw failedPrecondition("the device has a different key; sign in again as a new device");
    await exec(c, `UPDATE device SET public_key = ? WHERE user_id = ? AND device_id = ?`, [
      Buffer.from(key),
      userId,
      deviceId,
    ]);
    await o.addDevice(deviceId, false);
    await o.save();
  });
}

/** The public key of one of the user's devices, undefined when it has none. */
export async function deviceKey(db: Pool, userId: UUID, deviceId: bigint): Promise<Buffer | undefined> {
  const r = await row<{ public_key: Buffer | null }>(
    db,
    `SELECT public_key FROM device WHERE user_id = ? AND device_id = ?`,
    [userId, deviceId],
  );
  return r?.public_key ?? undefined;
}

/**
 * The signed-in devices with keys of the users, in the order asked, each
 * device with only its id and key, by device id. Unknown and deleted users
 * are left out.
 */
export async function deviceKeys(db: Pool, userIds: Uint8Array[]): Promise<UserDevices[]> {
  if (userIds.length === 0 || userIds.length > maxKeyUsers)
    throw invalidArgument(`user_ids must have 1 to ${maxKeyUsers} ids`);
  const order: UUID[] = [];
  const seen = new Set<string>();
  for (const b of userIds) {
    const id = parseUUID(b);
    if (!id) throw invalidArgument("user ids must be 16 bytes");
    if (!seen.has(hex(id))) {
      seen.add(hex(id));
      order.push(id);
    }
  }
  const inList = `(${placeholders(order.length)})`;
  const liveIds = await rows<{ user_id: Buffer }>(
    db,
    `SELECT user_id FROM user WHERE user_id IN ${inList} AND deleted_at IS NULL`,
    order,
  );
  const live = new Map<string, UserDevices>(
    liveIds.map((r) => [hex(r.user_id), create(UserDevicesSchema, { userId: r.user_id })]),
  );
  const devices = await rows<{ user_id: Buffer; device_id: bigint; public_key: Buffer }>(
    db,
    `SELECT user_id, device_id, public_key FROM device
     WHERE user_id IN ${inList} AND token_hash IS NOT NULL AND public_key IS NOT NULL`,
    order,
  );
  for (const r of devices)
    live.get(hex(r.user_id))?.devices.push(create(DeviceSchema, { deviceId: r.device_id, publicKey: r.public_key }));
  const users: UserDevices[] = [];
  for (const id of order) {
    const u = live.get(hex(id));
    if (!u) continue;
    u.devices.sort((a, b) => (a.deviceId < b.deviceId ? -1 : a.deviceId > b.deviceId ? 1 : 0));
    users.push(u);
  }
  return users;
}
