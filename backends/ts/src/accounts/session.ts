// Connecting with a device token, and the password checks of a connected user.
import { timingSafeEqual } from "node:crypto";
import { create } from "@bufbuild/protobuf";
import type { Querier } from "../internal/db.ts";
import {
  ConnectResponseSchema,
  type ChangePasswordRequest,
  type ConnectRequest,
  type ConnectResponse,
} from "../gen/wrinkle/v1/messaging_pb.js";
import { UserSchema } from "../gen/wrinkle/v1/model_pb.js";
import { unauthenticated } from "../internal/apierror.ts";
import { exec, now, row, tx } from "../internal/db.ts";
import type { UUID } from "../internal/ids.ts";
import { signedInDevices, signOutDevice } from "./devices.ts";
import { lockOutbox } from "./outbox.ts";
import { hashPassword, verifyPassword } from "./password.ts";
import type { AccountsDeps } from "./service.ts";
import { dummyHash } from "./signin.ts";
import { parseToken, tokenHash } from "./token.ts";
import { checkPassword, validPassword } from "./validate.ts";

const invalidToken = () => unauthenticated("invalid device token");
export const wrongPassword = () => unauthenticated("wrong password");

/** Checks the device token and returns the user, the device's acked cursor and the log head. */
export async function connect(d: AccountsDeps, req: ConnectRequest): Promise<ConnectResponse> {
  const token = parseToken(req.deviceToken);
  if (!token) throw invalidToken();
  const device = await row<{ token_hash: Buffer | null; acked_position: bigint }>(
    d.db,
    `SELECT token_hash, acked_position FROM device WHERE user_id = ? AND device_id = ?`,
    [token.userId, token.deviceId],
  );
  if (!device?.token_hash || device.token_hash.length !== 32 || !timingSafeEqual(device.token_hash, tokenHash(token)))
    throw invalidToken();
  const user = await row<{
    name: string;
    icon: string;
    avatar: Buffer | null;
    deleted_at: Date | null;
    log_head: bigint;
  }>(d.db, `SELECT name, icon, avatar, deleted_at, log_head FROM user WHERE user_id = ?`, [token.userId]);
  if (!user || user.deleted_at !== null) throw invalidToken();
  await exec(d.db, `UPDATE device SET last_seen_at = ? WHERE user_id = ? AND device_id = ?`, [
    now(),
    token.userId,
    token.deviceId,
  ]);
  return create(ConnectResponseSchema, {
    user: create(UserSchema, {
      userId: token.userId,
      name: user.name,
      icon: user.icon,
      avatar: user.avatar ?? new Uint8Array(),
    }),
    deviceId: token.deviceId,
    deviceCursor: device.acked_position,
    logHead: user.log_head,
  });
}

/** The user's password hash in the row. */
export async function storedHash(q: Querier, userId: UUID): Promise<string> {
  const r = await row<{ password_hash: string }>(q, `SELECT password_hash FROM user WHERE user_id = ?`, [userId]);
  if (!r) throw new Error(`user ${userId.toString("hex")} missing`);
  return r.password_hash;
}

/** Checks the user's password, failing with UNAUTHENTICATED when it is wrong; returns the hash checked against. */
export async function verifyCurrent(d: AccountsDeps, userId: UUID, password: string): Promise<string> {
  const stored = await storedHash(d.db, userId);
  let hash = stored;
  let candidate = password;
  if (!validPassword(password) || stored === "") {
    // A deleted account has no hash; take as long as a wrong password.
    hash = await dummyHash();
    candidate = "";
  }
  const ok = await verifyPassword(candidate, hash);
  if (!ok || hash !== stored) throw wrongPassword();
  return stored;
}

/**
 * Replaces the password and signs out every other device of the user; their
 * connections are closed when fan-out delivers the sign-out.
 */
export async function changePassword(
  d: AccountsDeps,
  userId: UUID,
  deviceId: bigint,
  req: ChangePasswordRequest,
): Promise<void> {
  const verified = await verifyCurrent(d, userId, req.currentPassword);
  checkPassword(req.newPassword);
  const newHash = await hashPassword(req.newPassword);
  await tx(d.db, async (c) => {
    const o = await lockOutbox(c, userId, now());
    // Changed concurrently: what was checked is no longer the password.
    if ((await storedHash(c, userId)) !== verified) throw wrongPassword();
    await exec(c, `UPDATE user SET password_hash = ? WHERE user_id = ?`, [newHash, userId]);
    for (const other of await signedInDevices(c, userId)) {
      if (other !== deviceId) await signOutDevice(o, other);
    }
    await o.save();
  });
}
