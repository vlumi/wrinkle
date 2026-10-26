import { create } from "@bufbuild/protobuf";
import { expect, test, vi } from "vitest";
import type { Connection } from "../connection/connection";
import { ConversationSchema } from "../gen/wrinkle/v1/model_pb";
import { listConversations } from "./actions";

test("conversations are listed a page at a time until the token is empty", async () => {
  const conv = (n: number) => create(ConversationSchema, { conversationId: new Uint8Array([n]) });
  const pages = [
    { conversations: [conv(1), conv(2)], nextPageToken: new Uint8Array([7]) },
    { conversations: [conv(3)], nextPageToken: new Uint8Array([8]) },
    { conversations: [], nextPageToken: new Uint8Array() },
  ];
  const call = vi.fn((_: string, _body: { value: { pageToken: Uint8Array } }) => Promise.resolve(pages.shift()));
  const all = await listConversations(call as unknown as Connection["call"]);
  expect(all.map((c) => c.conversationId[0])).toEqual([1, 2, 3]);
  expect(call.mock.calls.map(([, b]) => [...b.value.pageToken])).toEqual([[], [7], [8]]);
});
