package wrinkle.services;

import io.grpc.Status;
import io.grpc.stub.StreamObserver;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
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
    Thread.startVirtualThread(
        () -> {
          out.onNext(
              CallResponse.newBuilder()
                  .setResponse(local.call(req.getUserId(), req.getDeviceId(), req.getRequest()))
                  .build());
          out.onCompleted();
        });
  }

  @Override
  public void ack(AckRequest req, StreamObserver<AckResponse> out) {
    Thread.startVirtualThread(
        () -> {
          try {
            local.ack(req.getUserId(), req.getDeviceId(), req.getCursor());
            out.onNext(AckResponse.getDefaultInstance());
            out.onCompleted();
          } catch (Exception e) {
            log.warn("ack failed", e);
            out.onError(Status.INTERNAL.withDescription("ack failed").asRuntimeException());
          }
        });
  }
}
