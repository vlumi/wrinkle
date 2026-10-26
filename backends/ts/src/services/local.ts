// The services in-process: each feature registers handlers for its requests.
import { create } from "@bufbuild/protobuf";
import { ErrorCode, ResponseSchema, type Request, type Response } from "../gen/wrinkle/v1/frame_pb.js";
import {
  ApiError,
  codeOf,
  errorResponse,
  failedPrecondition,
  invalidArgument,
  unauthenticated,
} from "../internal/apierror.ts";
import { withDeadline } from "../internal/deadline.ts";
import { isZero, type UUID } from "../internal/ids.ts";
import { log } from "../internal/log.ts";
import type { Backend, Handler, Handlers, RequestCase, SignInHandler, SignInHandlers } from "./backend.ts";

export class Local implements Backend {
  private readonly handlers: Handlers;
  private readonly signIn: SignInHandlers;
  private readonly onAck: (userId: UUID, deviceId: bigint, cursor: bigint) => Promise<void>;

  constructor(
    handlers: Handlers,
    signIn: SignInHandlers,
    ack: (userId: UUID, deviceId: bigint, cursor: bigint) => Promise<void>,
  ) {
    this.handlers = handlers;
    this.signIn = signIn;
    this.onAck = ack;
  }

  async call(userId: UUID | undefined, deviceId: bigint, req: Request, signal: AbortSignal): Promise<Response> {
    try {
      const body = await withDeadline(signal, () => this.dispatch(userId, deviceId, req));
      return create(ResponseSchema, { requestId: req.id, body });
    } catch (err) {
      if (codeOf(err) === ErrorCode.INTERNAL) log.error("request failed", { request: req.id, err });
      return errorResponse(req.id, err);
    }
  }

  ack(userId: UUID, deviceId: bigint, cursor: bigint): Promise<void> {
    return this.onAck(userId, deviceId, cursor);
  }

  private async dispatch(userId: UUID | undefined, deviceId: bigint, req: Request): Promise<Response["body"]> {
    const body = req.body;
    if (body.case === undefined) throw invalidArgument("request has no body");
    if (body.case === "register" || body.case === "signIn" || body.case === "connect") {
      if (!isZero(userId)) throw failedPrecondition("already connected");
      const h = this.signIn[body.case] as SignInHandler<typeof body.case> | undefined;
      if (!h) throw unimplemented(body.case);
      return h(body.value);
    }
    if (!userId || isZero(userId)) throw unauthenticated("connect first");
    const h = this.handlers[body.case] as Handler<RequestCase> | undefined;
    if (!h) throw unimplemented(body.case);
    return h({ userId, deviceId }, body.value);
  }
}

const unimplemented = (kind: string) => new ApiError(ErrorCode.INTERNAL, `${kind} is not implemented`);
