// A user's devices: listing, renaming and signing out.
import { create } from "@bufbuild/protobuf";
import { DeviceSchema, type Device } from "../gen/wrinkle/v1/model_pb.js";
import type { RenameDeviceRequest } from "../gen/wrinkle/v1/messaging_pb.js";
import { codeOf, invalidArgument, notFound } from "../internal/apierror.ts";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb.js";
import {
  exec,
  now,
  row,
  rows,
  timedRow,
  timedRows,
  timestampOf,
  tx,
  type DbTime,
  type Querier,
} from "../internal/db.ts";
import type { UUID } from "../internal/ids.ts";
import { lockOutbox, type Outbox } from "./outbox.ts";
import type { AccountsDeps } from "./service.ts";
import { checkDeviceName } from "./validate.ts";

const deviceColumns = `SELECT device_id, name, created_at, last_seen_at, push_token IS NOT NULL AS push, public_key FROM device`;

interface DeviceRow {
  device_id: bigint;
  name: string;
  created_at: DbTime;
  last_seen_at: DbTime;
  push: unknown;
  public_key: Buffer | null;
}

const toDevice = (r: DeviceRow): Device =>
  create(DeviceSchema, {
    deviceId: r.device_id,
    name: r.name,
    createdAt: timestampOf(r.created_at),
    lastSeenAt: timestampOf(r.last_seen_at),
    push: Number(r.push) !== 0,
    publicKey: r.public_key ?? new Uint8Array(),
  });

export async function loadDevice(q: Querier, userId: UUID, deviceId: bigint): Promise<Device> {
  const r = await timedRow<DeviceRow>(q, deviceColumns + ` WHERE user_id = ? AND device_id = ?`, [userId, deviceId]);
  if (!r) throw new Error(`device ${deviceId} of user ${userId.toString("hex")} missing`);
  return toDevice(r);
}

/** The user's signed-in devices, oldest first. */
export async function listDevices(d: AccountsDeps, userId: UUID): Promise<Device[]> {
  const list = await timedRows<DeviceRow>(
    d.db,
    deviceColumns + ` WHERE user_id = ? AND token_hash IS NOT NULL ORDER BY created_at, device_id`,
    [userId],
  );
  return list.map(toDevice);
}

const noSuchDevice = () => notFound("no such signed-in device");

/** Locks the user row for the outbox, then checks that the device is the user's and signed in. */
export async function lockSignedIn(q: Querier, userId: UUID, deviceId: bigint): Promise<Outbox> {
  const o = await lockOutbox(q, userId, now());
  const r = await row(
    q,
    `SELECT 1 FROM device WHERE user_id = ? AND device_id = ? AND token_hash IS NOT NULL FOR UPDATE`,
    [userId, deviceId],
  );
  if (!r) throw noSuchDevice();
  return o;
}

/** Renames one of the user's signed-in devices. */
export async function renameDevice(d: AccountsDeps, userId: UUID, req: RenameDeviceRequest): Promise<Device> {
  checkDeviceName(req.name);
  return tx(d.db, async (c) => {
    const o = await lockSignedIn(c, userId, req.deviceId);
    await exec(c, `UPDATE device SET name = ? WHERE user_id = ? AND device_id = ?`, [req.name, userId, req.deviceId]);
    const device = await o.addDeviceName(req.deviceId);
    await o.save();
    return device;
  });
}

/** Signs out another of the user's devices. */
export async function signOutOther(d: AccountsDeps, userId: UUID, deviceId: bigint, target: bigint): Promise<void> {
  if (target === deviceId) throw invalidArgument("this is the requesting device; use SignOut");
  await signOut(d, userId, target);
}

/** Invalidates the device's token and removes its push registration. */
export async function signOutSelf(d: AccountsDeps, userId: UUID, deviceId: bigint): Promise<void> {
  try {
    await signOut(d, userId, deviceId);
  } catch (err) {
    // Signed out already, by another device.
    if (codeOf(err) !== ErrorCode.NOT_FOUND) throw err;
  }
}

async function signOut(d: AccountsDeps, userId: UUID, deviceId: bigint): Promise<void> {
  await tx(d.db, async (c) => {
    const o = await lockSignedIn(c, userId, deviceId);
    await signOutDevice(o, deviceId);
    await o.save();
  });
}

/** Clears the device's token and push registration and writes its DeviceEvent. */
export async function signOutDevice(o: Outbox, deviceId: bigint): Promise<void> {
  await exec(
    o.q,
    `UPDATE device SET token_hash = NULL, push_service = NULL, push_token = NULL WHERE user_id = ? AND device_id = ?`,
    [o.userId, deviceId],
  );
  await o.addDevice(deviceId, true);
}

/** The user's devices with a token, locked. */
export async function signedInDevices(q: Querier, userId: UUID): Promise<bigint[]> {
  const list = await rows<{ device_id: bigint }>(
    q,
    `SELECT device_id FROM device WHERE user_id = ? AND token_hash IS NOT NULL FOR UPDATE`,
    [userId],
  );
  return list.map((r) => r.device_id);
}
