// Conversation keys: the epoch keys devices wrap for each other, which the
// servers store and hand out but cannot unwrap.
import { create } from "@bufbuild/protobuf";
import type { PoolConnection } from "mysql2/promise";
import {
  GetConversationKeysResponseSchema,
  KeyRecipientSchema,
  ReceivedKeySchema,
  type GetConversationKeysResponse,
  type WrappedKey,
} from "../gen/wrinkle/v1/messaging_pb.js";
import { deviceKey } from "../accounts/keys.ts";
import { failedPrecondition, invalidArgument, permissionDenied } from "../internal/apierror.ts";
import { exec, now, row, rows, rowsOf, tx } from "../internal/db.ts";
import { hex, parseUUID, type UUID } from "../internal/ids.ts";
import type { Deps } from "../services/deps.ts";
import { has, load, member, notInConversation, selectState, type State } from "./state.ts";

const maxWrappedKeys = 500;
const wrappedBytes = 60;

interface Wrap {
  userId: UUID;
  deviceId: bigint;
  wrapped: Buffer;
}

/**
 * Stores keys of a conversation wrapped by the requesting device. Epoch
 * key_epoch + 1 starts a new epoch, for current members; the current epoch
 * takes more current members' devices; an older one only the user's own
 * devices. A request that wraps for the requesting device itself makes the
 * epoch: for an existing epoch it fails unless this device made it, so of
 * two devices publishing the same epoch, the later fails.
 */
export async function publishKeys(
  deps: Deps,
  userId: UUID,
  deviceId: bigint,
  conversationId: UUID,
  epoch: number,
  keys: WrappedKey[],
): Promise<void> {
  if (keys.length === 0 || keys.length > maxWrappedKeys)
    throw invalidArgument(`keys must have 1 to ${maxWrappedKeys} wrapped keys`);
  let self = false;
  const wraps: Wrap[] = keys.map((k) => {
    const id = parseUUID(k.recipientUserId);
    if (!id) throw invalidArgument("recipient_user_id must be 16 bytes");
    if (k.recipientDeviceId === 0n) throw invalidArgument("recipient_device_id must be set");
    if (k.wrapped.length !== wrappedBytes) throw invalidArgument(`wrapped must be ${wrappedBytes} bytes`);
    self ||= id.equals(userId) && k.recipientDeviceId === deviceId;
    return { userId: id, deviceId: k.recipientDeviceId, wrapped: Buffer.from(k.wrapped) };
  });
  const senderKey = await deviceKey(deps.users, userId, deviceId);

  await tx(deps.conversations, async (c) => {
    const st = await member(c, conversationId, userId);
    if (!senderKey) throw failedPrecondition("this device has no key; set one first");
    const notNext = failedPrecondition(`epoch ${epoch} is not next: the current one is ${st.keyEpoch}`);
    if (epoch === st.keyEpoch + 1) {
      recipientsAreMembers(st, wraps);
    } else if (epoch >= 1 && epoch <= st.keyEpoch) {
      if (epoch === st.keyEpoch) recipientsAreMembers(st, wraps);
      else if (wraps.some((w) => !w.userId.equals(userId)))
        throw permissionDenied("keys of an earlier epoch go only to the user's own devices");
      if (self && !(await madeBy(c, conversationId, epoch, userId, deviceId))) throw notNext;
    } else {
      throw notNext;
    }
    const at = now();
    await exec(
      c,
      `INSERT IGNORE INTO conversation_key
         (conversation_id, recipient_user_id, recipient_device_id, epoch, sender_user_id, sender_device_id, sender_public_key, wrapped, created_at)
       VALUES ${rowsOf(wraps.length, 9)}`,
      wraps.flatMap((w) => [conversationId, w.userId, w.deviceId, epoch, userId, deviceId, senderKey, w.wrapped, at]),
    );
    if (epoch > st.keyEpoch)
      await exec(c, `UPDATE conversation SET key_epoch = ?, rotate = FALSE WHERE conversation_id = ?`, [
        epoch,
        conversationId,
      ]);
  });
}

function recipientsAreMembers(st: State, wraps: Wrap[]): void {
  for (const w of wraps)
    if (!has(st.members, w.userId)) throw invalidArgument(`recipient ${hex(w.userId)} is not a member`);
}

/** Whether the device holds the epoch's key wrapped by itself, that is, it made the epoch. */
async function madeBy(
  c: PoolConnection,
  conversationId: UUID,
  epoch: number,
  userId: UUID,
  deviceId: bigint,
): Promise<boolean> {
  const r = await row<{ sender_user_id: Buffer; sender_device_id: bigint }>(
    c,
    `SELECT sender_user_id, sender_device_id FROM conversation_key
     WHERE conversation_id = ? AND recipient_user_id = ? AND recipient_device_id = ? AND epoch = ?`,
    [conversationId, userId, deviceId, epoch],
  );
  return !!r && r.sender_user_id.equals(userId) && r.sender_device_id === deviceId;
}

/**
 * The conversation's key state and the keys wrapped for the device in
 * epochs after afterEpoch, oldest first. Former members may ask: no wraps
 * reach them after they leave.
 */
export async function conversationKeys(
  deps: Deps,
  userId: UUID,
  deviceId: bigint,
  conversationId: UUID,
  afterEpoch: number,
): Promise<GetConversationKeysResponse> {
  const db = deps.conversations;
  const st = await load(db, selectState, conversationId);
  const m = await row(db, `SELECT 1 FROM conversation_member WHERE conversation_id = ? AND user_id = ?`, [
    conversationId,
    userId,
  ]);
  if (!m) throw notInConversation(userId, conversationId);
  const resp = create(GetConversationKeysResponseSchema, { currentEpoch: st.keyEpoch, rotate: st.rotate });
  if (st.keyEpoch > 0) {
    const recipients = await rows<{ recipient_user_id: Buffer; recipient_device_id: bigint }>(
      db,
      `SELECT recipient_user_id, recipient_device_id FROM conversation_key
       WHERE conversation_id = ? AND epoch = ? ORDER BY recipient_user_id, recipient_device_id`,
      [conversationId, st.keyEpoch],
    );
    resp.currentRecipients = recipients.map((r) =>
      create(KeyRecipientSchema, { userId: r.recipient_user_id, deviceId: r.recipient_device_id }),
    );
  }
  const received = await rows<{
    epoch: number;
    sender_user_id: Buffer;
    sender_device_id: bigint;
    sender_public_key: Buffer;
    wrapped: Buffer;
  }>(
    db,
    `SELECT epoch, sender_user_id, sender_device_id, sender_public_key, wrapped FROM conversation_key
     WHERE conversation_id = ? AND recipient_user_id = ? AND recipient_device_id = ? AND epoch > ?
     ORDER BY epoch`,
    [conversationId, userId, deviceId, afterEpoch],
  );
  resp.keys = received.map((r) =>
    create(ReceivedKeySchema, {
      epoch: r.epoch,
      senderUserId: r.sender_user_id,
      senderDeviceId: r.sender_device_id,
      senderPublicKey: r.sender_public_key,
      wrapped: r.wrapped,
    }),
  );
  return resp;
}
