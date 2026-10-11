package wrinkle.gateway;

import com.google.protobuf.InvalidProtocolBufferException;
import com.linecorp.armeria.common.websocket.WebSocketCloseStatus;
import com.linecorp.armeria.common.websocket.WebSocketFrame;
import com.linecorp.armeria.common.websocket.WebSocketFrameType;
import com.linecorp.armeria.common.websocket.WebSocketWriter;
import java.util.Locale;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.ApiError;
import wrinkle.Metrics;
import wrinkle.services.Backend;
import wrinkle.v1.ConnectResponse;
import wrinkle.v1.Frame;
import wrinkle.v1.Pong;
import wrinkle.v1.Request;
import wrinkle.v1.Response;

/** One device's WebSocket connection. */
final class Conn {
  private static final Logger log = LoggerFactory.getLogger(Conn.class);

  private final Backend backend;
  private final Connections connections;
  private final WebSocketWriter out;
  private final String remote;
  private volatile boolean connected;
  private volatile long userId;
  private volatile long deviceId;

  Conn(Backend backend, Connections connections, WebSocketWriter out, String remote) {
    this.backend = backend;
    this.connections = connections;
    this.out = out;
    this.remote = remote;
  }

  boolean isConnected() {
    return connected;
  }

  long userId() {
    return userId;
  }

  long deviceId() {
    return deviceId;
  }

  void receive(WebSocketFrame frame) {
    if (frame.type() != WebSocketFrameType.BINARY) {
      if (frame.type() == WebSocketFrameType.TEXT) {
        out.close(WebSocketCloseStatus.INVALID_MESSAGE_TYPE, "frames are binary protobuf");
      }
      return;
    }
    Frame f;
    try {
      f = Frame.parseFrom(frame.array());
    } catch (InvalidProtocolBufferException e) {
      out.close(WebSocketCloseStatus.POLICY_VIOLATION, "malformed frame");
      return;
    }
    switch (f.getKindCase()) {
      case REQUEST -> Thread.startVirtualThread(() -> handleRequest(f.getRequest()));
      case ACK -> {
        if (connected) {
          long cursor = f.getAck().getCursor();
          Thread.startVirtualThread(() -> ack(cursor));
        }
      }
      case PING -> send(Frame.newBuilder().setPong(Pong.getDefaultInstance()).build());
      case PONG -> {}
      default -> out.close(WebSocketCloseStatus.POLICY_VIOLATION, "unexpected frame");
    }
  }

  /** Queues bytes for the client. A closed writer drops them; the device syncs on reconnect. */
  void send(byte[] frame) {
    out.tryWrite(WebSocketFrame.ofBinary(frame));
  }

  private void send(Frame frame) {
    send(frame.toByteArray());
  }

  /** Closes the outbound side once the client's side has ended. */
  void end() {
    out.close();
  }

  void closeForRestart() {
    out.close(WebSocketCloseStatus.SERVICE_RESTART, "shutting down");
  }

  private void ack(long cursor) {
    try {
      backend.ack(userId, deviceId, cursor);
    } catch (Exception e) {
      log.warn("ack failed", e);
    }
  }

  private void handleRequest(Request req) {
    long start = System.nanoTime();
    Response resp = dispatch(req);
    send(Frame.newBuilder().setResponse(resp).build());
    String type = requestType(req);
    String code = resp.hasError() ? resp.getError().getCode().name() : "ok";
    Metrics.REQUESTS.labelValues(type, code).inc();
    Metrics.REQUEST_DURATION.labelValues(type).observe((System.nanoTime() - start) / 1e9);
  }

  /** The request's metric label: the name of its body field in the contract. */
  private static String requestType(Request req) {
    Request.BodyCase body = req.getBodyCase();
    return body == Request.BodyCase.BODY_NOT_SET ? "unknown" : body.name().toLowerCase(Locale.ROOT);
  }

  /** Applies the connection's state rules, then hands the request to the backend. */
  private Response dispatch(Request req) {
    if (req.hasConnect() && connected) {
      return ApiError.response(req, ApiError.failedPrecondition("already connected"));
    }
    if (!req.hasConnect() && !connected) {
      return ApiError.response(req, ApiError.unauthenticated("connect first"));
    }
    Response resp;
    try {
      resp = backend.call(userId, deviceId, req);
    } catch (Exception e) {
      log.error("backend call for request {} failed", req.getId(), e);
      return ApiError.response(req, ApiError.unavailable("services unavailable"));
    }
    if (resp.hasConnect() && !attached(resp.getConnect())) {
      return ApiError.response(req, ApiError.failedPrecondition("already connected"));
    }
    return resp;
  }

  /** Records the identity Connect established; false if another Connect got there first. */
  private boolean attached(ConnectResponse resp) {
    synchronized (this) {
      if (connected) {
        return false;
      }
      userId = resp.getUser().getUserId();
      deviceId = resp.getDeviceId();
      connected = true;
    }
    connections.attach(this);
    return true;
  }

  @Override
  public String toString() {
    return "conn " + remote + " user " + userId + " device " + deviceId;
  }
}
