// Registration and sign-in with a password, each creating a device.
import { randomBytes } from "node:crypto";
import { create } from "@bufbuild/protobuf";
import type { Pool } from "mysql2/promise";
import {
  DeviceCredentialsSchema,
  type DeviceCredentials,
  type RegisterRequest,
  type SignInRequest,
} from "../gen/wrinkle/v1/messaging_pb.js";
import { unauthenticated } from "../internal/apierror.ts";
import { exec, now, row, tx } from "../internal/db.ts";
import { newUUID, randomId, type UUID } from "../internal/ids.ts";
import { claim, claimOwner } from "./names.ts";
import { lockOutbox } from "./outbox.ts";
import { argonMemory, argonThreads, argonTime, encodeHash, hashPassword, verifyPassword } from "./password.ts";
import type { AccountsDeps } from "./service.ts";
import { newToken, tokenHash, tokenString } from "./token.ts";
import { checkDeviceName, checkName, checkPassword, validName, validPassword } from "./validate.ts";

const wrongCredentials = "wrong name or password";

let dummy: Promise<string> | undefined;

/** A hash verified against when there is no account to check, so an unknown name takes as long as a wrong password. */
export const dummyHash = (): Promise<string> =>
  (dummy ??= encodeHash(randomBytes(16).toString("base64"), randomBytes(16), argonMemory, argonTime, argonThreads));

/**
 * Creates an account and a device for it. The user row comes before the
 * name claim, so an interruption leaves at worst a user nobody can reach,
 * never a claim without its user.
 */
export async function register(d: AccountsDeps, req: RegisterRequest): Promise<DeviceCredentials> {
  checkName(req.name);
  checkPassword(req.password);
  checkDeviceName(req.deviceName);
  const hash = await hashPassword(req.password);
  const userId = newUUID();
  await exec(
    d.db,
    `INSERT INTO user (user_id, name, icon, password_hash, log_head, last_outbox_id, created_at)
     VALUES (?, ?, '', ?, 0, 0, ?)`,
    [userId, req.name, hash, now()],
  );
  try {
    await claim(d.db, req.name, userId);
  } catch (err) {
    await exec(d.db, `DELETE FROM user WHERE user_id = ?`, [userId]).catch((derr: unknown) =>
      d.log.warn("removing the unclaimed user failed", { user: userId, err: derr }),
    );
    throw err;
  }
  return newDevice(d.db, userId, req.deviceName);
}

/** Checks the password and creates a device. A wrong name and a wrong password fail alike, in about the same time. */
export async function signIn(d: AccountsDeps, req: SignInRequest): Promise<DeviceCredentials> {
  checkDeviceName(req.deviceName);
  const found = await passwordOf(d.db, req.name);
  let hash = found?.hash ?? "";
  let password = req.password;
  if (hash === "" || !validPassword(password)) {
    hash = await dummyHash();
    password = "";
  }
  const ok = await verifyPassword(password, hash);
  if (!ok || !found || !validPassword(req.password)) throw unauthenticated(wrongCredentials);
  return newDevice(d.db, found.userId, req.deviceName);
}

/** The user holding a valid claim on the name and their password hash. */
async function passwordOf(db: Pool, name: string): Promise<{ userId: UUID; hash: string } | undefined> {
  if (!validName(name)) return undefined;
  const owner = await claimOwner(db, name);
  if (!owner) return undefined;
  const r = await row<{ password_hash: string }>(
    db,
    `SELECT password_hash FROM user WHERE user_id = ? AND name = ? COLLATE utf8mb4_0900_as_ci`,
    [owner, name],
  );
  return r && { userId: owner, hash: r.password_hash };
}

/** Signs a device in, with its DeviceEvent for the user's other devices. */
async function newDevice(db: Pool, userId: UUID, name: string): Promise<DeviceCredentials> {
  const token = newToken(userId, randomId());
  await tx(db, async (c) => {
    const o = await lockOutbox(c, userId, now());
    await exec(
      c,
      `INSERT INTO device (user_id, device_id, name, token_hash, acked_position, created_at, last_seen_at)
       VALUES (?, ?, ?, ?, 0, ?, ?)`,
      [userId, token.deviceId, name, tokenHash(token), o.now, o.now],
    );
    await o.addDevice(token.deviceId, false);
    await o.save();
  });
  return create(DeviceCredentialsSchema, { userId, deviceId: token.deviceId, deviceToken: tokenString(token) });
}
