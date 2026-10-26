package wrinkle;

import wrinkle.v1.ErrorCode;
import wrinkle.v1.ErrorReason;
import wrinkle.v1.Request;
import wrinkle.v1.Response;

/** An error with a contract error code. */
public final class ApiError extends RuntimeException {
  private final ErrorCode code;
  private final ErrorReason reason;

  public ApiError(ErrorCode code, String message) {
    this(code, ErrorReason.ERROR_REASON_UNSPECIFIED, message);
  }

  public ApiError(ErrorCode code, ErrorReason reason, String message) {
    super(message, null, false, false);
    this.code = code;
    this.reason = reason;
  }

  public ErrorCode code() {
    return code;
  }

  public ErrorReason reason() {
    return reason;
  }

  public static ApiError invalidArgument(String message) {
    return new ApiError(ErrorCode.ERROR_CODE_INVALID_ARGUMENT, message);
  }

  public static ApiError unauthenticated(String message) {
    return new ApiError(ErrorCode.ERROR_CODE_UNAUTHENTICATED, message);
  }

  public static ApiError permissionDenied(String message) {
    return new ApiError(ErrorCode.ERROR_CODE_PERMISSION_DENIED, message);
  }

  public static ApiError notFound(String message) {
    return new ApiError(ErrorCode.ERROR_CODE_NOT_FOUND, message);
  }

  /** Names the precondition, which clients act on. */
  public static ApiError failedPrecondition(ErrorReason reason, String message) {
    return new ApiError(ErrorCode.ERROR_CODE_FAILED_PRECONDITION, reason, message);
  }

  public static ApiError unavailable(String message) {
    return new ApiError(ErrorCode.ERROR_CODE_UNAVAILABLE, message);
  }

  public static ApiError resourceExhausted(String message) {
    return new ApiError(ErrorCode.ERROR_CODE_RESOURCE_EXHAUSTED, message);
  }

  public static ApiError resourceExhausted(ErrorReason reason, String message) {
    return new ApiError(ErrorCode.ERROR_CODE_RESOURCE_EXHAUSTED, reason, message);
  }

  public static ApiError alreadyExists(String message) {
    return new ApiError(ErrorCode.ERROR_CODE_ALREADY_EXISTS, message);
  }

  public static ApiError cursorExpired(String message) {
    return new ApiError(ErrorCode.ERROR_CODE_CURSOR_EXPIRED, message);
  }

  /** The wire form of any error; internal ones keep their detail out of the response. */
  public static wrinkle.v1.Error toProto(Throwable t) {
    if (t instanceof ApiError e) {
      return wrinkle.v1.Error.newBuilder()
          .setCode(e.code)
          .setMessage(e.getMessage())
          .setReason(e.reason)
          .build();
    }
    return wrinkle.v1.Error.newBuilder()
        .setCode(ErrorCode.ERROR_CODE_INTERNAL)
        .setMessage("internal error")
        .build();
  }

  /** The response to a request that failed with the error. */
  public static Response response(Request req, Throwable t) {
    return Response.newBuilder().setRequestId(req.getId()).setError(toProto(t)).build();
  }
}
