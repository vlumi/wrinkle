// A device's acks and push registration.
import type { Pool } from "mysql2/promise";
import { PushService } from "../gen/wrinkle/v1/messaging_pb.js";
import { invalidArgument } from "../internal/apierror.ts";
import { exec, now } from "../internal/db.ts";
import type { UUID } from "../internal/ids.ts";

const maxPushToken = 200;

/** Records how far a device has processed the log. */
export async function ack(db: Pool, userId: UUID, deviceId: bigint, cursor: bigint): Promise<void> {
  await exec(
    db,
    `UPDATE device SET acked_position = GREATEST(acked_position, ?), last_seen_at = ?
     WHERE user_id = ? AND device_id = ?`,
    [cursor, now(), userId, deviceId],
  );
}

/** The contract's name of the service, as push_service stores it. */
const serviceName = (s: PushService): string => {
  const name = PushService[s] as string | undefined;
  return name === undefined ? String(s) : `PUSH_SERVICE_${name}`;
};

/** Records where to push for the device while it is not connected; an empty token removes the registration. */
export async function registerPushToken(
  db: Pool,
  userId: UUID,
  deviceId: bigint,
  service: PushService,
  token: string,
): Promise<void> {
  if (token === "") {
    await exec(db, `UPDATE device SET push_service = NULL, push_token = NULL WHERE user_id = ? AND device_id = ?`, [
      userId,
      deviceId,
    ]);
    return;
  }
  if (service !== PushService.APNS && service !== PushService.APNS_SANDBOX)
    throw invalidArgument(`unknown push service ${serviceName(service)}`);
  if (Buffer.byteLength(token, "utf8") > maxPushToken)
    throw invalidArgument(`push token is longer than ${maxPushToken} bytes`);
  // APNs tokens are hex, and go into the request path as they are.
  if (!/^[0-9a-fA-F]*$/.test(token)) throw invalidArgument("push token must be hex");
  await exec(db, `UPDATE device SET push_service = ?, push_token = ? WHERE user_id = ? AND device_id = ?`, [
    serviceName(service),
    token,
    userId,
    deviceId,
  ]);
}
