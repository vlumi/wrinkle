// Device changes: to the user's own devices, and a device's key to everyone
// who encrypts for it.
import { clone, create } from "@bufbuild/protobuf";
import { PushSchema, SignedOutSchema } from "../gen/wrinkle/backend/v1/services_pb.js";
import { DeviceEventSchema, EventSchema } from "../gen/wrinkle/v1/event_pb.js";
import { DeviceSchema } from "../gen/wrinkle/v1/model_pb.js";
import { placeholders, rows } from "../internal/db.ts";
import { hex, isZero, parseUUID, type UUID } from "../internal/ids.ts";
import type { Delivery } from "./deliver.ts";
import { eachLimited } from "./limit.ts";
import type { UserOutboxRow } from "./outbox.ts";
import { profileConcurrency, profilePage } from "./profile.ts";

const wrap = (userId: UUID, err: unknown) =>
  new Error(`deliver to user ${hex(userId)}: ${(err as Error).message}`, { cause: err });

/**
 * Appends a device change to the user's own log; a device signed out also
 * has its connections closed, even when the log had the event already. A
 * device with a key also goes, with only its id and key, to everyone who
 * encrypts for it: the user's direct partners and the members of the
 * user's groups; a rename, which changes nothing they use, does not.
 */
export async function deliverDevice(d: Delivery, o: UserOutboxRow): Promise<void> {
  if (o.event.body.case !== "device") return;
  let event = o.event;
  let de = o.event.body.value;
  if (de.userId.length === 0) {
    de = clone(DeviceEventSchema, de);
    de.userId = o.userId;
    event = clone(EventSchema, o.event);
    event.body = { case: "device", value: de };
  }
  const device = de.device;
  await d.append(o.userId, o.userId, o.outboxId, event).catch((err: unknown) => {
    throw wrap(o.userId, err);
  });
  if (de.signedOut) {
    await d.publish(
      o.userId,
      create(PushSchema, {
        kind: { case: "signedOut", value: create(SignedOutSchema, { deviceIds: [device?.deviceId ?? 0n] }) },
      }),
    );
  }
  if (!device || device.publicKey.length === 0 || o.kind === "device_name") return;
  const keyOnly = create(EventSchema, {
    occurredAt: event.occurredAt,
    body: {
      case: "device",
      value: create(DeviceEventSchema, {
        userId: o.userId,
        device: create(DeviceSchema, { deviceId: device.deviceId, publicKey: device.publicKey }),
        signedOut: de.signedOut,
      }),
    },
  });
  const seen = new Set<string>([hex(o.userId)]);
  let after: UUID = Buffer.alloc(16);
  for (;;) {
    const { users, last } = await contacts(d, o.userId, after);
    const fresh = users.filter((id) => {
      const key = hex(id);
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
    await eachLimited(fresh, profileConcurrency, async (id) => {
      try {
        await d.append(id, o.userId, o.outboxId, keyOnly);
      } catch (err) {
        throw wrap(id, err);
      }
    });
    if (!last) return;
    after = last;
  }
}

/**
 * The users of a page of the user's conversations after the given one, in
 * id order: direct partners, and the current members of groups the user is
 * in. last is the page's last conversation, undefined after the final page.
 */
async function contacts(d: Delivery, userId: UUID, after: UUID): Promise<{ users: UUID[]; last: UUID | undefined }> {
  const entries = await rows<{ conversation_id: Buffer; peer_user_id: Buffer | null }>(
    d.db,
    `SELECT conversation_id, peer_user_id FROM user_conversation
     WHERE user_id = ? AND conversation_id > ? AND (peer_user_id IS NOT NULL OR (kind = 'group' AND member))
     ORDER BY conversation_id LIMIT ?`,
    [userId, after, profilePage],
  );
  const users: UUID[] = [];
  const groups: Buffer[] = [];
  let last: UUID | undefined;
  for (const e of entries) {
    last = e.conversation_id;
    const peer = parseUUID(e.peer_user_id);
    if (peer) users.push(peer);
    else groups.push(e.conversation_id);
  }
  if (entries.length < profilePage) last = undefined;
  if (last && isZero(last)) last = undefined;
  if (groups.length === 0) return { users, last };
  const found = await rows<{ members: Buffer | null }>(
    d.conversations,
    `SELECT members FROM conversation WHERE conversation_id IN (${placeholders(groups.length)})`,
    groups,
  );
  for (const r of found) {
    const members = r.members ?? Buffer.alloc(0);
    for (let i = 0; i + 16 <= members.length; i += 16) users.push(Buffer.from(members.subarray(i, i + 16)));
  }
  return { users, last };
}
