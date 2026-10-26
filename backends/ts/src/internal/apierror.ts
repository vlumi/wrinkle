// Contract error codes carried on thrown errors.
import { create } from "@bufbuild/protobuf";
import { ErrorCode, ErrorReason, ErrorSchema, ResponseSchema, type Response } from "../gen/wrinkle/v1/frame_pb.js";

export class ApiError extends Error {
  readonly code: ErrorCode;
  reason = ErrorReason.UNSPECIFIED;
  constructor(code: ErrorCode, message: string) {
    super(message);
    this.code = code;
  }

  /** Sets the error's reason. */
  because(reason: ErrorReason): this {
    this.reason = reason;
    return this;
  }
}

export const invalidArgument = (m: string) => new ApiError(ErrorCode.INVALID_ARGUMENT, m);
export const unauthenticated = (m: string) => new ApiError(ErrorCode.UNAUTHENTICATED, m);
export const permissionDenied = (m: string) => new ApiError(ErrorCode.PERMISSION_DENIED, m);
export const notFound = (m: string) => new ApiError(ErrorCode.NOT_FOUND, m);
/** Names the precondition, which clients act on. */
export const failedPrecondition = (reason: ErrorReason, m: string) =>
  new ApiError(ErrorCode.FAILED_PRECONDITION, m).because(reason);
export const unavailable = (m: string) => new ApiError(ErrorCode.UNAVAILABLE, m);
export const resourceExhausted = (m: string) => new ApiError(ErrorCode.RESOURCE_EXHAUSTED, m);
export const alreadyExists = (m: string) => new ApiError(ErrorCode.ALREADY_EXISTS, m);
export const cursorExpired = (m: string) => new ApiError(ErrorCode.CURSOR_EXPIRED, m);

/** The code of an error; anything that is not an ApiError is internal. */
export const codeOf = (err: unknown): ErrorCode => (err instanceof ApiError ? err.code : ErrorCode.INTERNAL);

/** The response a device gets for a failed request; internal details stay here. */
export function errorResponse(requestId: bigint, err: unknown): Response {
  const code = codeOf(err);
  const message = code === ErrorCode.INTERNAL ? "internal error" : (err as Error).message;
  const reason = err instanceof ApiError ? err.reason : ErrorReason.UNSPECIFIED;
  return create(ResponseSchema, {
    requestId,
    body: { case: "error", value: create(ErrorSchema, { code, message, reason }) },
  });
}
