import { create } from "@bufbuild/protobuf";
import { EventSchema } from "../gen/wrinkle/v1/event_pb";
import { ConversationSchema, MessageSchema } from "../gen/wrinkle/v1/model_pb";
import { hex } from "../ids";
import { applyEvent, byActivity, inOrder, unread, upsertConversation } from "./model";
import type { Data } from "./model";

const id = (n: number) => new Uint8Array(16).fill(n);
const me = id(1);
const peer = id(2);
const c7 = id(7);
const k7 = hex(c7);

function oneConversation(): Data {
  const c = create(ConversationSchema, { conversationId: c7, peer: { userId: peer, name: "bob" } });
  return upsertConversation({ conversations: {}, messages: {} }, c);
}

function messageEvent(seq: bigint, sender: Uint8Array, seconds = 0n) {
  return create(EventSchema, {
    cursor: seq,
    body: {
      case: "message",
      value: {
        message: create(MessageSchema, {
          conversationId: c7,
          seq,
          senderUserId: sender,
          text: `m${seq}`,
          sentAt: { seconds },
        }),
      },
    },
  });
}

test("a peer's message becomes the latest and unread", () => {
  const { data, unknownConversation } = applyEvent(oneConversation(), messageEvent(1n, peer), me);
  expect(unknownConversation).toBe(false);
  const c = data.conversations[k7];
  expect(c.lastSeq).toBe(1n);
  expect(c.lastMessage?.text).toBe("m1");
  expect(unread(c)).toBe(1n);
});

test("my own message is not unread", () => {
  const { data } = applyEvent(oneConversation(), messageEvent(1n, me), me);
  expect(unread(data.conversations[k7])).toBe(0n);
});

test("applying the same message twice keeps one copy", () => {
  let data = applyEvent(oneConversation(), messageEvent(1n, peer), me).data;
  data = applyEvent(data, messageEvent(1n, peer), me).data;
  expect(inOrder(data.messages[k7])).toHaveLength(1);
});

test("read events tell the reader from the peer and never go backwards", () => {
  let data = oneConversation();
  for (const seq of [1n, 2n, 3n]) data = applyEvent(data, messageEvent(seq, peer), me).data;
  const read = (userId: Uint8Array, seq: bigint) =>
    create(EventSchema, { body: { case: "read", value: { conversationId: c7, userId, seq } } });
  data = applyEvent(data, read(me, 2n), me).data;
  data = applyEvent(data, read(peer, 3n), me).data;
  data = applyEvent(data, read(me, 1n), me).data;
  const c = data.conversations[k7];
  expect(c.readSeq).toBe(2n);
  expect(c.peerReadSeq).toBe(3n);
  expect(unread(c)).toBe(1n);
});

test("a message in an unknown conversation asks for the list", () => {
  const { unknownConversation } = applyEvent({ conversations: {}, messages: {} }, messageEvent(1n, peer), me);
  expect(unknownConversation).toBe(true);
});

test("conversations sort by their latest message, newest first", () => {
  let data = oneConversation();
  data = upsertConversation(
    data,
    create(ConversationSchema, { conversationId: id(8), peer: { userId: id(3), name: "carol" } }),
  );
  data = applyEvent(data, messageEvent(1n, peer, 100n), me).data;
  expect(byActivity(data.conversations).map((c) => hex(c.conversationId))).toEqual([k7, hex(id(8))]);
});

test("a profile event renames the peer in their conversations", () => {
  const profile = create(EventSchema, {
    body: { case: "profile", value: { user: { userId: peer, name: "robert", icon: "fox" } } },
  });
  const c = applyEvent(oneConversation(), profile, me).data.conversations[k7];
  expect(c.peer?.name).toBe("robert");
  expect(c.peer?.icon).toBe("fox");
});

test("another user's profile event changes nothing", () => {
  const data = oneConversation();
  const profile = create(EventSchema, {
    body: { case: "profile", value: { user: { userId: id(3), name: "carol" } } },
  });
  expect(applyEvent(data, profile, me).data).toBe(data);
});
