import { create } from "@bufbuild/protobuf";
import { EventSchema } from "../gen/wrinkle/v1/event_pb";
import {
  ConversationKind,
  ConversationSchema,
  MemberSchema,
  MessageSchema,
  NoticeSchema,
} from "../gen/wrinkle/v1/model_pb";
import type { Message } from "../gen/wrinkle/v1/model_pb";
import { hex } from "../ids";
import {
  applyEvent,
  byActivity,
  emptyData,
  inOrder,
  preview,
  textOf,
  unread,
  upsertConversation,
  withMembers,
} from "./model";
import type { Data } from "./model";

const id = (n: number) => new Uint8Array(16).fill(n);
const me = id(1);
const peer = id(2);
const c7 = id(7);
const k7 = hex(c7);

function oneConversation(): Data {
  const c = create(ConversationSchema, { conversationId: c7, peer: { userId: peer, name: "bob" } });
  return upsertConversation(emptyData(), c);
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
          content: { case: "text", value: `m${seq}` },
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
  expect(textOf(c.lastMessage!)).toBe("m1");
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
  const { unknownConversation } = applyEvent(emptyData(), messageEvent(1n, peer), me);
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

describe("groups", () => {
  const carol = id(3);
  const g = id(9);
  const kg = hex(g);
  const users = {
    [hex(peer)]: { userId: peer, name: "bob" },
    [hex(carol)]: { userId: carol, name: "carol" },
  } as Data["users"];

  function group(): Data {
    const c = create(ConversationSchema, {
      conversationId: g,
      kind: ConversationKind.GROUP,
      topic: "Trip",
      memberCount: 3,
      member: true,
      lastSeq: 1n,
    });
    const data = upsertConversation(emptyData(), c);
    const members = [{ userId: me, name: "me" }, users[hex(peer)], users[hex(carol)]].map((user) =>
      create(MemberSchema, { user, readSeq: 1n }),
    );
    return withMembers({ ...data, users }, g, members);
  }

  const groupMessage = (seq: bigint, sender: Uint8Array, content: Message["content"]) =>
    create(EventSchema, {
      cursor: seq,
      body: {
        case: "message",
        value: { message: create(MessageSchema, { conversationId: g, seq, senderUserId: sender, content }) },
      },
    });

  test("previews name the sender in groups and tell notices as sentences", () => {
    let data = applyEvent(group(), groupMessage(2n, peer, { case: "text", value: "hi" }), me).data;
    expect(preview(data.conversations[kg], me, data.users)).toBe("bob: hi");
    data = applyEvent(data, groupMessage(3n, me, { case: "text", value: "yo" }), me).data;
    expect(preview(data.conversations[kg], me, data.users)).toBe("You: yo");
    data = applyEvent(
      data,
      groupMessage(4n, carol, {
        case: "notice",
        value: create(NoticeSchema, { kind: { case: "topicChanged", value: { topic: "Trip 2" } } }),
      }),
      me,
    ).data;
    expect(preview(data.conversations[kg], me, data.users)).toBe("carol changed the topic to “Trip 2”");
    expect(data.conversations[kg].topic).toBe("Trip 2");
  });

  test("an unknown sender's text is shown alone", () => {
    const data = applyEvent(group(), groupMessage(2n, id(8), { case: "text", value: "hi" }), me).data;
    expect(preview(data.conversations[kg], me, data.users)).toBe("hi");
  });

  test("a removal notice ends membership once, and asks for the members", () => {
    const removed = {
      case: "notice",
      value: create(NoticeSchema, { kind: { case: "memberRemoved", value: { user: { userId: me, name: "me" } } } }),
    } as const;
    const applied = applyEvent(group(), groupMessage(2n, peer, removed), me);
    expect(applied.membersChanged).toBe(true);
    const c = applied.data.conversations[kg];
    expect([c.member, c.memberCount]).toEqual([false, 2]);
    expect(applied.data.members[kg]).toHaveLength(2);
    const again = applyEvent(applied.data, groupMessage(2n, peer, removed), me);
    expect(again.data.conversations[kg].memberCount).toBe(2);
    expect(again.membersChanged).toBe(false);
  });

  test("others' read events move their member read positions, not the peer's", () => {
    const read = create(EventSchema, { body: { case: "read", value: { conversationId: g, userId: carol, seq: 5n } } });
    const data = applyEvent(group(), read, me).data;
    expect(data.conversations[kg].peerReadSeq).toBe(0n);
    expect(data.members[kg].find((m) => hex(m.user!.userId) === hex(carol))?.readSeq).toBe(5n);
  });
});
