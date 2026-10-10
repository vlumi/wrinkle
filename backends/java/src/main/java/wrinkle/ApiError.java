package wrinkle;

import wrinkle.v1.ErrorCode;

/** An error with a contract error code. */
public final class ApiError extends RuntimeException {
  private final ErrorCode code;

  public ApiError(ErrorCode code, String message) {
    super(message, null, false, false);
    this.code = code;
  }

  public ErrorCode code() {
    return code;
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

  public static ApiError failedPrecondition(String message) {
    return new ApiError(ErrorCode.ERROR_CODE_FAILED_PRECONDITION, message);
  }

  /** The wire form of any error; internal ones keep their detail out of the response. */
  public static wrinkle.v1.Error toProto(Throwable t) {
    if (t instanceof ApiError e) {
      return wrinkle.v1.Error.newBuilder().setCode(e.code).setMessage(e.getMessage()).build();
    }
    return wrinkle.v1.Error.newBuilder()
        .setCode(ErrorCode.ERROR_CODE_INTERNAL)
        .setMessage("internal error")
        .build();
  }
}
