package wrinkle.services;

import com.linecorp.armeria.client.grpc.GrpcClients;
import wrinkle.backend.v1.AckRequest;
import wrinkle.backend.v1.BackendServiceGrpc;
import wrinkle.backend.v1.CallRequest;
import wrinkle.v1.Request;
import wrinkle.v1.Response;

/**
 * Forwards to a services process, or a balancer in front of several, over one HTTP/2 connection.
 * The stub blocks, which costs nothing on the virtual thread each request runs on.
 */
public final class RpcClient implements Backend {
  private final BackendServiceGrpc.BackendServiceBlockingStub rpc;

  public RpcClient(String address) {
    rpc =
        GrpcClients.builder("http://" + address)
            .responseTimeoutMillis(10_000)
            .build(BackendServiceGrpc.BackendServiceBlockingStub.class);
  }

  @Override
  public Response call(long userId, long deviceId, Request req) {
    return rpc.call(
            CallRequest.newBuilder()
                .setUserId(userId)
                .setDeviceId(deviceId)
                .setRequest(req)
                .build())
        .getResponse();
  }

  @Override
  public void ack(long userId, long deviceId, long cursor) {
    rpc.ack(
        AckRequest.newBuilder().setUserId(userId).setDeviceId(deviceId).setCursor(cursor).build());
  }
}
