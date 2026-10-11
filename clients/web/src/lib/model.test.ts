import { create } from "@bufbuild/protobuf";
import { EventSchema } from "../gen/wrinkle/v1/event_pb";
import { ConversationSchema, MessageSchema } from "../gen/wrinkle/v1/model_pb";
import { applyEvent, byActivity, inOrder, unread, upsertConversation } from "./model";
import type { Data } from "./model";

const me = 1n;
const peer = 2n;

function withConversation(): Data {
  const c = create(ConversationSchema, { conversationId: 7n, peer: { userId: peer, name: "bob" } });
  return upsertConversation({ conversations: {}, messages: {} }, c);
}

function messageEvent(seq: bigint, sender: bigint, seconds = 0n) {
  return create(EventSchema, {
    cursor: seq,
    body: {
      case: "message",
      value: {
        message: create(MessageSchema, {
          conversationId: 7n,
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
  const { data, unknownConversation } = applyEvent(withConversation(), messageEvent(1n, peer), me);
  expect(unknownConversation).toBe(false);
  const c = data.conversations["7"];
  expect(c.lastSeq).toBe(1n);
  expect(c.lastMessage?.text).toBe("m1");
  expect(unread(c)).toBe(1n);
});

test("my own message is not unread", () => {
  const { data } = applyEvent(withConversation(), messageEvent(1n, me), me);
  expect(unread(data.conversations["7"])).toBe(0n);
});

test("applying the same message twice keeps one copy", () => {
  let data = applyEvent(withConversation(), messageEvent(1n, peer), me).data;
  data = applyEvent(data, messageEvent(1n, peer), me).data;
  expect(inOrder(data.messages["7"])).toHaveLength(1);
});

test("read events tell the reader from the peer and never go backwards", () => {
  let data = withConversation();
  for (const seq of [1n, 2n, 3n]) data = applyEvent(data, messageEvent(seq, peer), me).data;
  const read = (userId: bigint, seq: bigint) =>
    create(EventSchema, { body: { case: "read", value: { conversationId: 7n, userId, seq } } });
  data = applyEvent(data, read(me, 2n), me).data;
  data = applyEvent(data, read(peer, 3n), me).data;
  data = applyEvent(data, read(me, 1n), me).data;
  const c = data.conversations["7"];
  expect(c.readSeq).toBe(2n);
  expect(c.peerReadSeq).toBe(3n);
  expect(unread(c)).toBe(1n);
});

test("a message in an unknown conversation asks for the list", () => {
  const { unknownConversation } = applyEvent({ conversations: {}, messages: {} }, messageEvent(1n, peer), me);
  expect(unknownConversation).toBe(true);
});

test("conversations sort by their latest message, newest first", () => {
  let data = withConversation();
  data = upsertConversation(
    data,
    create(ConversationSchema, { conversationId: 8n, peer: { userId: 3n, name: "carol" } }),
  );
  data = applyEvent(data, messageEvent(1n, peer, 100n), me).data;
  expect(byActivity(data.conversations).map((c) => c.conversationId)).toEqual([7n, 8n]);
});
