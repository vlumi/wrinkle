import { create } from "@bufbuild/protobuf";
import { AttachmentsSchema } from "../gen/wrinkle/v1/content_pb";
import { EventSchema } from "../gen/wrinkle/v1/event_pb";
import {
  ConversationKind,
  ConversationSchema,
  MemberSchema,
  MessageSchema,
  NoticeSchema,
  SealedSchema,
} from "../gen/wrinkle/v1/model_pb";
import type { Message } from "../gen/wrinkle/v1/model_pb";
import { hex } from "../ids";
import {
  addHistory,
  applyEvent,
  attachmentsOf,
  byActivity,
  emptyData,
  inOrder,
  isDeleted,
  preview,
  STICKER,
  stickerOf,
  textKey,
  textOf,
  UNDECRYPTABLE,
  unread,
  upsertConversation,
  withAttachments,
  withDeleted,
  withMembers,
  withStickers,
  withTexts,
} from "./model";
import type { Data } from "./model";

const id = (n: number) => new Uint8Array(16).fill(n);
const me = id(1);
const peer = id(2);
const c7 = id(7);
const k7 = hex(c7);
// The model only tells sealed content apart; the texts come from opening it.
const sealed = { case: "sealed", value: create(SealedSchema, { epoch: 1 }) } as const;

/** The data with the texts of the conversation's messages, as this device opened them. */
function opened(data: Data, conversationId = c7): Data {
  const texts: Record<string, string> = {};
  for (const m of Object.values(data.messages[hex(conversationId)] ?? {}))
    texts[textKey(m)] = m.clientMessageId || `m${m.seq}`;
  return withTexts(data, texts);
}

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
          content: sealed,
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
  expect(textOf(c.lastMessage!, data.texts)).toBe("");
  expect(textOf(c.lastMessage!, opened(data).texts)).toBe("m1");
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

describe("deleted messages", () => {
  const deleted = (seq: bigint, conversationId = c7) =>
    create(EventSchema, {
      body: { case: "messageDeleted", value: { conversationId, seq, deletedAt: { seconds: 50n } } },
    });

  test("a deletion tombstones the message and the latest preview", () => {
    let data = oneConversation();
    for (const seq of [1n, 2n]) data = applyEvent(data, messageEvent(seq, me), me).data;
    const { data: applied, unknownConversation } = applyEvent(opened(data), deleted(2n), me);
    const next = applied;
    expect(unknownConversation).toBe(false);
    const m = next.messages[k7]["2"];
    expect(isDeleted(m)).toBe(true);
    expect(m.deletedAt?.seconds).toBe(50n);
    expect(textOf(m, next.texts)).toBe("");
    expect(textOf(next.messages[k7]["1"], next.texts)).toBe("m1");
    expect(preview(next.conversations[k7], me, next.users, next.texts)).toBe("Message removed");
  });

  test("deleting an earlier message leaves the preview", () => {
    let data = oneConversation();
    for (const seq of [1n, 2n]) data = applyEvent(data, messageEvent(seq, peer), me).data;
    data = applyEvent(data, deleted(1n), me).data;
    expect(isDeleted(data.messages[k7]["1"])).toBe(true);
    expect(preview(data.conversations[k7], me, data.users, opened(data, c7).texts)).toBe("m2");
  });

  test("a later message replaces the removed preview", () => {
    let data = applyEvent(oneConversation(), messageEvent(1n, peer), me).data;
    data = applyEvent(data, deleted(1n), me).data;
    data = applyEvent(data, messageEvent(2n, peer), me).data;
    expect(preview(data.conversations[k7], me, data.users, opened(data, c7).texts)).toBe("m2");
  });

  test("an older copy of a deleted message does not bring its text back", () => {
    let data = applyEvent(oneConversation(), messageEvent(1n, peer), me).data;
    data = applyEvent(data, deleted(1n), me).data;
    data = applyEvent(data, messageEvent(1n, peer), me).data;
    expect(isDeleted(data.messages[k7]["1"])).toBe(true);
    expect(preview(data.conversations[k7], me, data.users, opened(data, c7).texts)).toBe("Message removed");
  });

  test("a deletion in an unknown conversation asks for the list", () => {
    expect(applyEvent(emptyData(), deleted(1n), me).unknownConversation).toBe(true);
  });

  test("a message synced with deleted_at reads as removed", () => {
    const tomb = create(EventSchema, {
      cursor: 1n,
      body: {
        case: "message",
        value: {
          message: create(MessageSchema, {
            conversationId: c7,
            seq: 1n,
            senderUserId: peer,
            deletedAt: { seconds: 5n },
          }),
        },
      },
    });
    const data = applyEvent(oneConversation(), tomb, me).data;
    expect(preview(data.conversations[k7], me, data.users, opened(data, c7).texts)).toBe("Message removed");
  });
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
        value: {
          message: create(MessageSchema, {
            conversationId: g,
            seq,
            senderUserId: sender,
            content,
            clientMessageId: seq === 2n ? "hi" : "yo",
          }),
        },
      },
    });

  test("previews name the sender in groups and tell notices as sentences", () => {
    let data = applyEvent(
      group(),
      groupMessage(2n, peer, { case: "sealed", value: create(SealedSchema, { epoch: 1 }) }),
      me,
    ).data;
    expect(preview(data.conversations[kg], me, data.users, opened(data, g).texts)).toBe("bob: hi");
    data = applyEvent(
      data,
      groupMessage(3n, me, { case: "sealed", value: create(SealedSchema, { epoch: 1 }) }),
      me,
    ).data;
    expect(preview(data.conversations[kg], me, data.users, opened(data, g).texts)).toBe("You: yo");
    data = applyEvent(
      data,
      groupMessage(4n, carol, {
        case: "notice",
        value: create(NoticeSchema, { kind: { case: "topicChanged", value: { topic: "Trip 2" } } }),
      }),
      me,
    ).data;
    expect(preview(data.conversations[kg], me, data.users, opened(data, g).texts)).toBe(
      "carol changed the topic to “Trip 2”",
    );
    expect(data.conversations[kg].topic).toBe("Trip 2");
  });

  test("a blocked sender's latest message is hidden in a group's preview, not in a direct one", () => {
    const blocked = new Set([hex(peer)]);
    let data = applyEvent(group(), groupMessage(2n, peer, sealed), me).data;
    expect(preview(data.conversations[kg], me, data.users, opened(data, g).texts, blocked)).toBe(
      "Message from a blocked user",
    );
    data = applyEvent(data, groupMessage(3n, me, sealed), me).data;
    expect(preview(data.conversations[kg], me, data.users, opened(data, g).texts, blocked)).toBe("You: yo");
    const direct = applyEvent(oneConversation(), messageEvent(1n, peer), me).data;
    expect(preview(direct.conversations[k7], me, direct.users, opened(direct).texts, blocked)).toBe("m1");
  });

  test("an unknown sender's text is shown alone", () => {
    const data = applyEvent(
      group(),
      groupMessage(2n, id(8), { case: "sealed", value: create(SealedSchema, { epoch: 1 }) }),
      me,
    ).data;
    expect(preview(data.conversations[kg], me, data.users, opened(data, g).texts)).toBe("hi");
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

test("a sealed message this device cannot open says so", () => {
  const data = applyEvent(oneConversation(), messageEvent(1n, peer), me).data;
  const m = data.messages[k7]["1"];
  const next = withTexts(data, { [textKey(m)]: null });
  expect(textOf(m, next.texts)).toBe(UNDECRYPTABLE);
  expect(preview(next.conversations[k7], me, next.users, next.texts)).toBe(UNDECRYPTABLE);
});

test("a sticker message shows its sticker, and reads as a sticker in the list", () => {
  const data = applyEvent(oneConversation(), messageEvent(1n, peer), me).data;
  const m = data.messages[k7]["1"];
  expect(stickerOf(m, data.stickers)).toBeUndefined();
  const next = withStickers(data, { [textKey(m)]: "ab12" });
  expect(stickerOf(m, next.stickers)).toBe("ab12");
  expect(textOf(m, next.texts)).toBe(STICKER);
  expect(preview(next.conversations[k7], me, next.users, next.texts)).toBe(STICKER);
  const deleted = withDeleted(next, c7, 1n);
  expect(stickerOf(deleted.messages[k7]["1"], deleted.stickers)).toBeUndefined();
});

test("a message with attachments shows them, and reads as its caption or what it holds", () => {
  const data = applyEvent(oneConversation(), messageEvent(1n, peer), me).data;
  const m = data.messages[k7]["1"];
  const photo = { blobId: id(9), mediaType: "image/jpeg", width: 4, height: 3, name: "a.jpg" };
  const a = create(AttachmentsSchema, { items: [photo, photo] });
  const next = withAttachments(data, { [textKey(m)]: a });
  expect(attachmentsOf(m, next.attachments)).toBe(a);
  expect(textOf(m, next.texts)).toBe("2 photos");
  expect(preview(next.conversations[k7], me, next.users, next.texts)).toBe("2 photos");
  const captioned = withAttachments(data, {
    [textKey(m)]: create(AttachmentsSchema, { items: [photo], caption: "hi" }),
  });
  expect(textOf(m, captioned.texts)).toBe("hi");
  const deleted = withDeleted(next, c7, 1n);
  expect(attachmentsOf(deleted.messages[k7]["1"], deleted.attachments)).toBeUndefined();
});

test("reaction events, history and a deletion", () => {
  const reaction = (cursor: bigint, sealedSet?: { epoch: number }) =>
    create(EventSchema, {
      cursor,
      body: { case: "reaction", value: { conversationId: c7, seq: 1n, userId: peer, sealed: sealedSet } },
    });
  let data = applyEvent(oneConversation(), messageEvent(1n, me), me).data;
  data = applyEvent(data, reaction(2n, { epoch: 1 }), me).data;
  const k = `${k7}/1`;
  expect(Object.keys(data.reactions[k])).toEqual([hex(peer)]);
  data = applyEvent(data, reaction(3n), me).data;
  expect(data.reactions[k]).toBeUndefined();
  // History brings the reactions with the messages.
  const listed = create(MessageSchema, {
    conversationId: c7,
    seq: 1n,
    senderUserId: me,
    content: sealed,
    reactions: [{ userId: peer, sealed: { epoch: 1 } }],
  });
  data = addHistory(data, c7, [listed]);
  expect(Object.keys(data.reactions[k])).toEqual([hex(peer)]);
  // A deleted message's reactions go with it.
  data = applyEvent(
    data,
    create(EventSchema, { cursor: 4n, body: { case: "messageDeleted", value: { conversationId: c7, seq: 1n } } }),
    me,
  ).data;
  expect(data.reactions[k]).toBeUndefined();
  expect(addHistory(data, c7, [listed]).reactions[k]).toBeUndefined();
});
