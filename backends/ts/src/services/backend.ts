// A device's requests: handled in-process by the services, or forwarded to a
// services process over the internal RPC. The gateway owns the connection
// state and uses one of the two.
import type { Request, Response } from "../gen/wrinkle/v1/frame_pb.js";
import type { UUID } from "../internal/ids.ts";

export interface Backend {
  /**
   * Handles a request for a device; the user id is undefined until the
   * connection has completed Connect. It throws only when the request could
   * not be handled at all; a request's own failure is the response's error.
   */
  call(userId: UUID | undefined, deviceId: bigint, req: Request): Promise<Response>;
  ack(userId: UUID, deviceId: bigint, cursor: bigint): Promise<void>;
}

export type RequestCase = NonNullable<Request["body"]["case"]>;
export type RequestBody<K extends RequestCase> = Extract<Request["body"], { case: K }>["value"];
export type ResponseBody = Response["body"];

/** Who a request is for, once connected. */
export interface Caller {
  userId: UUID;
  deviceId: bigint;
}

export type Handler<K extends RequestCase> = (caller: Caller, body: RequestBody<K>) => Promise<ResponseBody>;
export type Handlers = { [K in RequestCase]?: Handler<K> };

/** Handlers for the requests allowed before Connect, which have no caller yet. */
export type SignInHandler<K extends "register" | "signIn" | "connect"> = (
  body: RequestBody<K>,
) => Promise<ResponseBody>;
export type SignInHandlers = { [K in "register" | "signIn" | "connect"]?: SignInHandler<K> };
