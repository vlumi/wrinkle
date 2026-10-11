// @vitest-environment node
// The key manager against a small in-memory stand-in for the backend's key
// requests: epochs made on first send, opened by the other side, shared
// with a new device, and replaced when the members or their devices change.

import { create } from "@bufbuild/protobuf";
import { BackendError } from "../connection/connection";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import type { Device, Message } from "../gen/wrinkle/v1/model_pb";
import { DeviceSchema, MessageSchema } from "../gen/wrinkle/v1/model_pb";
import { hex, sameId } from "../ids";
import type { DeviceRef } from "./crypto";
import { deviceId, Keys, needsEpoch } from "./keys";
import type { BodyInit, Call } from "./keys";
import { memoryStore } from "./store";

interface Wrap {
  epoch: number;
  recipient: string;
  senderUserId: Uint8Array;
  senderDeviceId: bigint;
  senderPublicKey: Uint8Array;
  wrapped: Uint8Array;
}

class Backend {
  keys = new Map<string, Uint8Array>();
  members = new Map<string, Uint8Array[]>();
  epochs = new Map<string, { epoch: number; rotate: boolean; wraps: Wrap[] }>();
  /** Replaces sender keys in GetConversationKeys, as a dishonest server might. */
  forge: Uint8Array | null = null;

  conversation(id: Uint8Array, members: Uint8Array[]) {
    this.members.set(hex(id), members);
    this.epochs.set(hex(id), { epoch: 0, rotate: false, wraps: [] });
  }

  setMembers(id: Uint8Array, members: Uint8Array[]) {
    this.members.set(hex(id), members);
    const c = this.epochs.get(hex(id))!;
    if (c.epoch > 0) c.rotate = true;
  }

  callFor(self: DeviceRef): Call {
    const fail = (code: ErrorCode) => {
      throw new BackendError(code, ErrorCode[code]);
    };
    const call = async (_: string, body: { case: string; value: Record<string, unknown> }) => {
      const v = body.value;
      switch (body.case) {
        case "setDeviceKey": {
          const id = deviceId(self.userId, self.deviceId);
          const old = this.keys.get(id);
          if (old && !sameId(old, v.publicKey as Uint8Array)) fail(ErrorCode.FAILED_PRECONDITION);
          this.keys.set(id, v.publicKey as Uint8Array);
          return {};
        }
        case "getDeviceKeys":
          return {
            users: (v.userIds as Uint8Array[]).map((u) => ({
              userId: u,
              devices: [...this.keys]
                .filter(([k]) => k.startsWith(hex(u) + ":"))
                .map(([k, publicKey]) => ({ deviceId: BigInt(k.split(":")[1]), publicKey })),
            })),
          };
        case "publishKeys": {
          const c = this.epochs.get(hex(v.conversationId as Uint8Array))!;
          const members = this.members.get(hex(v.conversationId as Uint8Array))!;
          if (!members.some((m) => sameId(m, self.userId))) fail(ErrorCode.PERMISSION_DENIED);
          const epoch = v.epoch as number;
          if (epoch === c.epoch + 1) {
            c.epoch = epoch;
            c.rotate = false;
          } else if (epoch < 1 || epoch > c.epoch) {
            fail(ErrorCode.FAILED_PRECONDITION);
          }
          const keys = v.keys as { recipientUserId: Uint8Array; recipientDeviceId: bigint; wrapped: Uint8Array }[];
          const me = deviceId(self.userId, self.deviceId);
          // Of two devices publishing the same epoch, the later fails.
          const mine = c.wraps.find((w) => w.epoch === epoch && w.recipient === me);
          const includesMe = keys.some((k) => deviceId(k.recipientUserId, k.recipientDeviceId) === me);
          if (includesMe && mine && !(sameId(mine.senderUserId, self.userId) && mine.senderDeviceId === self.deviceId))
            fail(ErrorCode.FAILED_PRECONDITION);
          for (const k of keys) {
            const recipient = deviceId(k.recipientUserId, k.recipientDeviceId);
            if (c.wraps.some((w) => w.epoch === epoch && w.recipient === recipient)) continue;
            c.wraps.push({
              epoch,
              recipient,
              senderUserId: self.userId,
              senderDeviceId: self.deviceId,
              senderPublicKey: this.keys.get(deviceId(self.userId, self.deviceId))!,
              wrapped: k.wrapped,
            });
          }
          return {};
        }
        case "getConversationKeys": {
          const c = this.epochs.get(hex(v.conversationId as Uint8Array))!;
          const me = deviceId(self.userId, self.deviceId);
          return {
            currentEpoch: c.epoch,
            rotate: c.rotate,
            currentRecipients: c.wraps
              .filter((w) => w.epoch === c.epoch)
              .map((w) => ({
                userId: Uint8Array.from(Buffer.from(w.recipient.split(":")[0], "hex")),
                deviceId: BigInt(w.recipient.split(":")[1]),
              })),
            keys: c.wraps
              .filter((w) => w.recipient === me && w.epoch > (v.afterEpoch as number))
              .map((w) => ({ ...w, senderPublicKey: this.forge ?? w.senderPublicKey })),
          };
        }
      }
      throw new Error("unexpected " + body.case);
    };
    return call as unknown as Call;
  }

  async device(userId: Uint8Array, id: bigint, members?: (c: Uint8Array) => Uint8Array[]) {
    const self = { userId, deviceId: id };
    const keys = new Keys(this.callFor(self), self, memoryStore(), {
      members: async (c) => members?.(c) ?? this.members.get(hex(c))!,
      directoryChanged: () => {},
    });
    expect(await keys.start()).toBe(true);
    return keys;
  }
}

const user = (n: number) => new Uint8Array(16).fill(n);
const alice = user(1),
  bob = user(2),
  carol = user(3);
const conversation = user(9);
let seq = 0n;

async function send(from: Keys, text: string, body: BodyInit = { case: "text", value: text }): Promise<Message> {
  const clientMessageId = `m${++seq}`;
  const sealed = await from.seal(conversation, clientMessageId, body);
  return create(MessageSchema, {
    conversationId: conversation,
    seq,
    senderUserId: from.self.userId,
    clientMessageId,
    content: { case: "sealed", value: sealed },
  });
}

/** The text of a message as the device opens it, or null. */
async function opened(k: Keys, m: Message): Promise<string | null> {
  const body = await k.open(m);
  return body?.case === "text" ? body.value : null;
}

test("the first send makes epoch 1, which the other side opens", async () => {
  const b = new Backend();
  b.conversation(conversation, [alice, bob]);
  const a1 = await b.device(alice, 11n);
  const b1 = await b.device(bob, 21n);
  const m = await send(a1, "hello");
  expect(m.content.case === "sealed" && m.content.value.epoch).toBe(1);
  expect(await opened(b1, m)).toBe("hello");
  expect(await opened(a1, m)).toBe("hello");
  // Replies use the same epoch while nothing changes.
  const r = await send(b1, "hi");
  expect(r.content.case === "sealed" && r.content.value.epoch).toBe(1);
  expect(await opened(a1, r)).toBe("hi");
});

test("a sticker is sealed content too", async () => {
  const b = new Backend();
  b.conversation(conversation, [alice, bob]);
  const a1 = await b.device(alice, 11n);
  const b1 = await b.device(bob, 21n);
  const stickerId = user(7);
  const m = await send(a1, "", { case: "sticker", value: { stickerId } });
  const body = await b1.open(m);
  expect(body?.case).toBe("sticker");
  expect(body?.case === "sticker" && hex(body.value.stickerId)).toBe(hex(stickerId));
});

test("content moved to another message does not open", async () => {
  const b = new Backend();
  b.conversation(conversation, [alice, bob]);
  const a1 = await b.device(alice, 11n);
  const b1 = await b.device(bob, 21n);
  const m = await send(a1, "hello");
  expect(await opened(b1, { ...m, clientMessageId: "other" })).toBeNull();
  expect(await opened(b1, { ...m, senderUserId: bob })).toBeNull();
});

test("a new device opens nothing until another device of its user shares the history", async () => {
  const b = new Backend();
  b.conversation(conversation, [alice, bob]);
  const a1 = await b.device(alice, 11n);
  const b1 = await b.device(bob, 21n);
  const m = await send(a1, "before");
  expect(await opened(b1, m)).toBe("before");
  const b2 = await b.device(bob, 22n);
  expect(await opened(b2, m)).toBeNull();
  const event = create(DeviceSchema, { deviceId: 22n, publicKey: b.keys.get(deviceId(bob, 22n)) }) as Device;
  expect(b1.deviceChanged(bob, event, false)).toBe(true);
  expect(await b1.share({ deviceId: 22n, publicKey: event.publicKey })).toBe(1);
  await b1.markKnown(22n);
  expect(b1.deviceChanged(bob, event, false)).toBe(false);
  b2.retry(hex(conversation));
  expect(await opened(b2, m)).toBe("before");
  // The shared current epoch already covers the new device: no new epoch.
  const next = await send(a1, "after");
  expect(next.content.case === "sealed" && next.content.value.epoch).toBe(1);
  expect(await opened(b2, next)).toBe("after");
});

test("a new device of a member, or a membership change, makes a new epoch", async () => {
  const b = new Backend();
  b.conversation(conversation, [alice, bob, carol]);
  const a1 = await b.device(alice, 11n);
  const b1 = await b.device(bob, 21n);
  const c1 = await b.device(carol, 31n);
  await send(a1, "one");
  await b.device(carol, 32n);
  // The directory learns of it from the device event.
  const added = create(DeviceSchema, { deviceId: 32n, publicKey: b.keys.get(deviceId(carol, 32n)) }) as Device;
  expect(a1.deviceChanged(carol, added, false)).toBe(false);
  const two = await send(a1, "two");
  expect(two.content.case === "sealed" && two.content.value.epoch).toBe(2);
  b.setMembers(conversation, [alice, bob]);
  a1.invalidate(hex(conversation));
  const three = await send(a1, "three");
  expect(three.content.case === "sealed" && three.content.value.epoch).toBe(3);
  expect(await opened(b1, three)).toBe("three");
  expect(await opened(c1, three)).toBeNull();
  expect(await opened(c1, two)).toBe("two");
});

test("two devices racing for the next epoch both end up sealing with the winner's", async () => {
  const b = new Backend();
  b.conversation(conversation, [alice, bob]);
  const a1 = await b.device(alice, 11n);
  const b1 = await b.device(bob, 21n);
  // Both see no epoch yet; one publishes first, the other retries.
  const [x, y] = await Promise.all([send(a1, "x"), send(b1, "y")]);
  const epochs = [x, y].map((m) => (m.content.case === "sealed" ? m.content.value.epoch : 0));
  expect(epochs).toEqual([1, 1]);
  expect(await opened(a1, y)).toBe("y");
  expect(await opened(b1, x)).toBe("x");
});

test("a key wrapped under a sender key the directory does not know is refused", async () => {
  const b = new Backend();
  b.conversation(conversation, [alice, bob]);
  const a1 = await b.device(alice, 11n);
  const b1 = await b.device(bob, 21n);
  const m = await send(a1, "hello");
  b.forge = new Uint8Array(32).fill(7);
  expect(await opened(b1, m)).toBeNull();
});

test("an epoch is needed with none yet, on rotate, without the key, or when the devices differ", () => {
  const state = { currentEpoch: 1, rotate: false, recipients: new Set(["a:1", "b:2"]) };
  expect(needsEpoch(state, new Set(["b:2", "a:1"]), true)).toBe(false);
  expect(needsEpoch({ ...state, currentEpoch: 0 }, new Set(["a:1", "b:2"]), true)).toBe(true);
  expect(needsEpoch({ ...state, rotate: true }, new Set(["a:1", "b:2"]), true)).toBe(true);
  expect(needsEpoch(state, new Set(["a:1", "b:2"]), false)).toBe(true);
  expect(needsEpoch(state, new Set(["a:1"]), true)).toBe(true);
  expect(needsEpoch(state, new Set(["a:1", "b:2", "b:3"]), true)).toBe(true);
});
