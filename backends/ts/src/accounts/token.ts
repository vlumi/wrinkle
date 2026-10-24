// A device token is "v1." and the unpadded base64url of the user id, the
// device id as 8 bytes big-endian and a 32-byte secret. The device row keeps
// only the SHA-256 of the secret.
import { createHash, randomBytes } from "node:crypto";
import type { UUID } from "../internal/ids.ts";

const tokenPrefix = "v1.";
const tokenSecretLen = 32;
const tokenLen = 16 + 8 + tokenSecretLen;

export interface DeviceToken {
  userId: UUID;
  deviceId: bigint;
  secret: Buffer;
}

/** A token for the device with a fresh secret. */
export const newToken = (userId: UUID, deviceId: bigint): DeviceToken => ({
  userId,
  deviceId,
  secret: randomBytes(tokenSecretLen),
});

export function tokenString(t: DeviceToken): string {
  const id = Buffer.alloc(8);
  id.writeBigUInt64BE(t.deviceId);
  return tokenPrefix + Buffer.concat([t.userId, id, t.secret]).toString("base64url");
}

/** What the device row stores. */
export const tokenHash = (t: DeviceToken): Buffer => createHash("sha256").update(t.secret).digest();

export function parseToken(s: string): DeviceToken | undefined {
  if (!s.startsWith(tokenPrefix)) return undefined;
  const encoded = s.slice(tokenPrefix.length);
  if (!/^[A-Za-z0-9_-]*$/.test(encoded)) return undefined;
  const raw = Buffer.from(encoded, "base64url");
  if (raw.length !== tokenLen || raw.toString("base64url") !== encoded) return undefined;
  return { userId: raw.subarray(0, 16), deviceId: raw.readBigUInt64BE(16), secret: raw.subarray(24) };
}
