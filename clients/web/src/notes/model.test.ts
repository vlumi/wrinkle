import { create } from "@bufbuild/protobuf";
import { ConversationKind, ConversationSchema, MessageSchema, SealedSchema } from "../gen/wrinkle/v1/model_pb";
import { emptyData, preview, textKey, upsertConversation } from "../conversations/model";
import { isNotes, notesOf } from "./model";

const id = (n: number) => new Uint8Array(16).fill(n);
const me = id(1);

const notes = create(ConversationSchema, {
  conversationId: id(8),
  kind: ConversationKind.NOTES,
  memberCount: 1,
  member: true,
});
const direct = create(ConversationSchema, {
  conversationId: id(7),
  kind: ConversationKind.DIRECT,
  peer: { userId: id(2), name: "bob" },
});

test("the notes are found among the conversations", () => {
  const data = upsertConversation(upsertConversation(emptyData(), direct), notes);
  expect(isNotes(notes)).toBe(true);
  expect(isNotes(direct)).toBe(false);
  expect(notesOf(data.conversations)?.conversationId).toEqual(notes.conversationId);
  expect(notesOf(upsertConversation(emptyData(), direct).conversations)).toBeUndefined();
});

test("the notes' preview is the latest note, without who wrote it", () => {
  const m = create(MessageSchema, {
    conversationId: notes.conversationId,
    seq: 1n,
    senderUserId: me,
    content: { case: "sealed", value: create(SealedSchema, { epoch: 1 }) },
  });
  const c = { ...notes, lastSeq: 1n, lastMessage: m };
  expect(preview(c, me, {}, { [textKey(m)]: "milk" })).toBe("milk");
  expect(
    preview(
      { ...direct, lastMessage: { ...m, conversationId: direct.conversationId } },
      me,
      {},
      {
        [textKey({ conversationId: direct.conversationId, seq: 1n })]: "hi",
      },
    ),
  ).toBe("You: hi");
});
