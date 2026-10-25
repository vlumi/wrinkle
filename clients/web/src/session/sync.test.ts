import { describe, expect, it, vi } from "vitest";
import { BackendError, type Connection } from "../connection/connection";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import type { Api } from "./api";
import { Link } from "./link";
import type { Session } from "./session";
import { sync } from "./sync";

describe("sync", () => {
  it("starts over from the head when the cursor expired", async () => {
    const link = new Link();
    link.cursor = 42n;
    const restart = vi.fn();
    const call = vi.fn().mockRejectedValue(new BackendError(ErrorCode.CURSOR_EXPIRED, ""));
    link.conn = { call, restart, ack: vi.fn() } as unknown as Connection;
    let state = { messages: { c: [] }, complete: { c: true } } as unknown as Session;
    const api = {
      set: (partial: Partial<Session>) => (state = { ...state, ...partial }),
      get: () => state,
      link,
      fail: vi.fn(),
    } as unknown as Api;
    const none = {} as never;
    await sync(api, none, none, none, none, none).syncFromCursor();
    expect(link.cursor).toBe(0n);
    expect(state.messages).toEqual({});
    expect(state.complete).toEqual({});
    expect(restart).toHaveBeenCalledOnce();
  });
});
