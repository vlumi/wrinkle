package wrinkle.services;

import io.grpc.Context;
import io.grpc.Status;
import io.grpc.stub.StreamObserver;
import java.util.concurrent.TimeUnit;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.Deadline;
import wrinkle.Uuid;
import wrinkle.backend.v1.AckRequest;
import wrinkle.backend.v1.AckResponse;
import wrinkle.backend.v1.BackendServiceGrpc;
import wrinkle.backend.v1.CallRequest;
import wrinkle.backend.v1.CallResponse;

/**
 * Exposes the in-process services over the internal RPC, for gateways in other processes or
 * languages. Each call runs on a virtual thread, off Armeria's event loop.
 */
public final class RpcServer extends BackendServiceGrpc.BackendServiceImplBase {
  private static final Logger log = LoggerFactory.getLogger(RpcServer.class);

  private final LocalBackend local;

  public RpcServer(LocalBackend local) {
    this.local = local;
  }

  @Override
  public void call(CallRequest req, StreamObserver<CallResponse> out) {
    Uuid userId = Uuid.parse(req.getUserId());
    if (userId == null && !req.getUserId().isEmpty()) {
      out.onError(
          Status.INVALID_ARGUMENT
              .withDescription("user_id must be empty or 16 bytes")
              .asRuntimeException());
      return;
    }
    // The caller's deadline, or the request timeout when it set none.
    var callerDeadline = Context.current().getDeadline();
    long deadline =
        System.nanoTime()
            + (callerDeadline == null
                ? Deadline.REQUEST_TIMEOUT_NANOS
                : callerDeadline.timeRemaining(TimeUnit.NANOSECONDS));
    Thread.startVirtualThread(
        () -> {
          out.onNext(
              CallResponse.newBuilder()
                  .setResponse(local.call(userId, req.getDeviceId(), req.getRequest(), deadline))
                  .build());
          out.onCompleted();
        });
  }

  @Override
  public void ack(AckRequest req, StreamObserver<AckResponse> out) {
    Uuid userId = Uuid.parse(req.getUserId());
    if (userId == null) {
      out.onError(
          Status.INVALID_ARGUMENT.withDescription("user_id must be 16 bytes").asRuntimeException());
      return;
    }
    Thread.startVirtualThread(
        () -> {
          try {
            local.ack(userId, req.getDeviceId(), req.getCursor());
            out.onNext(AckResponse.getDefaultInstance());
            out.onCompleted();
          } catch (Exception e) {
            log.warn("ack failed", e);
            out.onError(Status.INTERNAL.withDescription("ack failed").asRuntimeException());
          }
        });
  }
}
