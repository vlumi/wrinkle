// A user's changes go to user_outbox in the transaction that makes them,
// under the lock on the user row, which holds the id counter.
import { create, toBinary } from "@bufbuild/protobuf";
import { DeviceEventSchema, EventSchema, type Event } from "../gen/wrinkle/v1/event_pb.js";
import type { Device } from "../gen/wrinkle/v1/model_pb.js";
import { exec, row, timestampOf, type Querier } from "../internal/db.ts";
import type { UUID } from "../internal/ids.ts";
import { loadDevice } from "./devices.ts";

export class Outbox {
  readonly q: Querier;
  readonly userId: UUID;
  readonly now: Date;
  lastId: bigint;

  constructor(q: Querier, userId: UUID, now: Date, lastId: bigint) {
    this.q = q;
    this.userId = userId;
    this.now = now;
    this.lastId = lastId;
  }

  /** Writes one row; kind names the event body. */
  async add(kind: string, body: Event["body"]): Promise<void> {
    const payload = toBinary(EventSchema, create(EventSchema, { occurredAt: timestampOf(this.now), body }));
    this.lastId++;
    await exec(
      this.q,
      `INSERT INTO user_outbox (user_id, outbox_id, kind, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
      [this.userId, this.lastId, kind, Buffer.from(payload), this.now],
    );
    // As with the conversation outbox: the binlog keeps the insert.
    await exec(this.q, `DELETE FROM user_outbox WHERE user_id = ? AND outbox_id = ?`, [this.userId, this.lastId]);
  }

  /** Stores the counter. */
  async save(): Promise<void> {
    await exec(this.q, `UPDATE user SET last_outbox_id = ? WHERE user_id = ?`, [this.lastId, this.userId]);
  }

  /** Writes a DeviceEvent with the device as it is now in the transaction. */
  addDevice(deviceId: bigint, signedOut: boolean): Promise<Device> {
    return this.addDeviceKind("device", deviceId, signedOut);
  }

  /** Writes a DeviceEvent for a change only the user's own devices need to know of: a new name. */
  addDeviceName(deviceId: bigint): Promise<Device> {
    return this.addDeviceKind("device_name", deviceId, false);
  }

  private async addDeviceKind(kind: string, deviceId: bigint, signedOut: boolean): Promise<Device> {
    const device = await loadDevice(this.q, this.userId, deviceId);
    await this.add(kind, {
      case: "device",
      value: create(DeviceEventSchema, { device, signedOut, userId: this.userId }),
    });
    return device;
  }
}

/** Locks the user row and reads the counter. */
export async function lockOutbox(q: Querier, userId: UUID, now: Date): Promise<Outbox> {
  const r = await row<{ last_outbox_id: bigint }>(q, `SELECT last_outbox_id FROM user WHERE user_id = ? FOR UPDATE`, [
    userId,
  ]);
  if (!r) throw new Error(`user ${userId.toString("hex")} missing`);
  return new Outbox(q, userId, now, r.last_outbox_id);
}
