package wrinkle.gateway;

import com.google.protobuf.InvalidProtocolBufferException;
import com.linecorp.armeria.common.websocket.WebSocketCloseStatus;
import com.linecorp.armeria.common.websocket.WebSocketFrame;
import com.linecorp.armeria.common.websocket.WebSocketWriter;
import com.linecorp.armeria.server.websocket.WebSocketProtocolViolationException;
import java.util.Locale;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import org.reactivestreams.Subscription;
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

/** One device's WebSocket connection, with its limits. */
final class Conn {
  private static final Logger log = LoggerFactory.getLogger(Conn.class);

  private static final long CONNECT_DEADLINE_NANOS = TimeUnit.SECONDS.toNanos(5);
  private static final long PING_INTERVAL_NANOS = TimeUnit.SECONDS.toNanos(30);
  private static final long IDLE_TIMEOUT_NANOS = TimeUnit.SECONDS.toNanos(90);
  private static final int RATE_BURST = 100;
  private static final int RATE_PER_SECOND = 50;
  private static final int MAX_IN_FLIGHT = 32;
  private static final int MAX_QUEUED_FRAMES = 32;
  private static final long SUBSCRIBE_TIMEOUT_MILLIS = 5000;

  private static final byte[] PONG =
      Frame.newBuilder().setPong(Pong.getDefaultInstance()).build().toByteArray();

  private final Backend backend;
  private final Connections connections;
  private final WebSocketWriter out;
  private final String remote;
  private final long openedAt;
  // Touched only by the inbound stream, which delivers one frame at a time.
  private final TokenBucket tokens;
  private final AckCoalescer acks;
  private final AtomicInteger inFlight = new AtomicInteger();
  private final AtomicInteger queued = new AtomicInteger();
  private final AtomicBoolean closed = new AtomicBoolean();
  private final AtomicBoolean paused = new AtomicBoolean();
  private volatile Subscription inbound;
  private volatile long lastReceived;
  // Touched only by the sweep.
  private long lastPing;
  private volatile boolean connected;
  private volatile boolean pushUnavailable;
  // Guarded by this.
  private boolean left;
  private volatile long userId;
  private volatile long deviceId;

  Conn(Backend backend, Connections connections, WebSocketWriter out, String remote) {
    this.backend = backend;
    this.connections = connections;
    this.out = out;
    this.remote = remote;
    this.openedAt = System.nanoTime();
    this.lastReceived = openedAt;
    this.lastPing = openedAt;
    this.tokens = new TokenBucket(RATE_BURST, RATE_PER_SECOND, openedAt);
    this.acks = new AckCoalescer(Thread::startVirtualThread, this::writeAck);
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
    try {
      receive0(frame);
    } finally {
      readMore();
    }
  }

  private void receive0(WebSocketFrame frame) {
    long now = System.nanoTime();
    lastReceived = now;
    switch (frame.type()) {
      case BINARY -> receive(frame.array(), now);
      case TEXT -> close(WebSocketCloseStatus.INVALID_MESSAGE_TYPE, "frames are binary protobuf");
      case PING -> {
        if (tokens.take(now)) {
          send(WebSocketFrame.ofPong(frame.array()));
        }
      }
      case PONG -> tokens.take(now);
      // Close frames end the inbound stream; continuations arrive aggregated.
      default -> {}
    }
  }

  private void receive(byte[] bytes, long now) {
    Frame f;
    try {
      f = Frame.parseFrom(bytes);
    } catch (InvalidProtocolBufferException e) {
      close(WebSocketCloseStatus.POLICY_VIOLATION, "malformed frame");
      return;
    }
    boolean allowed = tokens.take(now);
    switch (f.getKindCase()) {
      case REQUEST -> request(f.getRequest(), allowed);
      case ACK -> {
        if (allowed && connected) {
          acks.offer(f.getAck().getCursor());
        }
      }
      case PING -> {
        if (allowed) {
          send(PONG);
        }
      }
      case PONG -> {}
      default -> close(WebSocketCloseStatus.POLICY_VIOLATION, "unexpected frame");
    }
  }

  private void request(Request req, boolean allowed) {
    long start = System.nanoTime();
    if (!allowed) {
      respond(req, ApiError.response(req, ApiError.resourceExhausted("rate limit")), start);
      return;
    }
    if (inFlight.incrementAndGet() > MAX_IN_FLIGHT) {
      inFlight.decrementAndGet();
      respond(req, ApiError.response(req, ApiError.resourceExhausted("too many requests")), start);
      return;
    }
    connections.requestStarted();
    Thread.startVirtualThread(
        () -> {
          try {
            Response resp = dispatch(req);
            respond(req, resp, start);
            if (req.hasConnect() && pushUnavailable) {
              // Attached, but pushes cannot reach it; the device reconnects.
              close(WebSocketCloseStatus.TRY_AGAIN_LATER, "push channel unavailable");
            }
          } finally {
            inFlight.decrementAndGet();
            connections.requestFinished();
          }
        });
  }

  /**
   * Queues a pushed event, closing the connection if the client has not kept up. A closed writer
   * drops it; the device syncs on reconnect. False if the event was dropped.
   */
  boolean push(byte[] frame) {
    if (queued.get() >= MAX_QUEUED_FRAMES) {
      // Aborting skips the queued frames, so the close frame goes out next.
      if (closed.compareAndSet(false, true)) {
        out.abort(
            new WebSocketProtocolViolationException(
                WebSocketCloseStatus.TRY_AGAIN_LATER, "too slow"));
      }
      return false;
    }
    return send(frame);
  }

  private boolean send(byte[] frame) {
    return send(WebSocketFrame.ofBinary(frame));
  }

  /**
   * Queues a frame. Replies are bounded by the requests in flight and by reading pausing while the
   * queue is full. False if the writer is closed.
   */
  private boolean send(WebSocketFrame frame) {
    queued.incrementAndGet();
    if (!out.tryWrite(frame)) {
      written();
      return false;
    }
    out.whenConsumed().thenRun(this::written);
    return true;
  }

  private void written() {
    if (queued.decrementAndGet() < MAX_QUEUED_FRAMES && paused.compareAndSet(true, false)) {
      inbound.request(1);
    }
  }

  void subscribed(Subscription s) {
    inbound = s;
    s.request(1);
  }

  /** Reads the next frame, unless the client is not reading its replies. */
  private void readMore() {
    if (queued.get() < MAX_QUEUED_FRAMES) {
      inbound.request(1);
      return;
    }
    paused.set(true);
    // The queue may have drained before the pause was visible.
    if (queued.get() < MAX_QUEUED_FRAMES && paused.compareAndSet(true, false)) {
      inbound.request(1);
    }
  }

  private void close(WebSocketCloseStatus status, String reason) {
    if (closed.compareAndSet(false, true)) {
      out.close(status, reason);
    }
  }

  /** Closes the outbound side once the client's side has ended. */
  void end() {
    closed.set(true);
    out.close();
  }

  void closeForShutdown() {
    close(WebSocketCloseStatus.ENDPOINT_UNAVAILABLE, "shutting down");
  }

  /** The periodic check: the connect deadline, idleness, and the keepalive ping. */
  void sweep(long now) {
    if (closed.get()) {
      return;
    }
    if (!connected && now - openedAt >= CONNECT_DEADLINE_NANOS) {
      close(WebSocketCloseStatus.POLICY_VIOLATION, "connect first");
    } else if (now - lastReceived >= IDLE_TIMEOUT_NANOS) {
      close(WebSocketCloseStatus.ENDPOINT_UNAVAILABLE, "idle");
    } else if (now - lastPing >= PING_INTERVAL_NANOS) {
      lastPing = now;
      send(WebSocketFrame.ofPing());
    }
  }

  /** True the first time the connection leaves if it had connected, so its presence must go. */
  synchronized boolean leave() {
    if (left) {
      return false;
    }
    left = true;
    return connected;
  }

  private void writeAck(long cursor) {
    try {
      backend.ack(userId, deviceId, cursor);
    } catch (Exception e) {
      log.warn("ack failed", e);
    }
  }

  private void respond(Request req, Response resp, long start) {
    send(Frame.newBuilder().setResponse(resp).build().toByteArray());
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
    if (resp.hasConnect()) {
      CompletableFuture<Void> subscribed = attached(resp.getConnect());
      if (subscribed == null) {
        return ApiError.response(req, ApiError.failedPrecondition("already connected"));
      }
      // Pushes that follow the response must not be missed.
      try {
        subscribed.get(SUBSCRIBE_TIMEOUT_MILLIS, TimeUnit.MILLISECONDS);
      } catch (InterruptedException e) {
        Thread.currentThread().interrupt();
        pushUnavailable = true;
        return ApiError.response(req, ApiError.unavailable("push channel unavailable"));
      } catch (ExecutionException | TimeoutException e) {
        log.warn("push subscription of user {} not ready: {}", userId, e.toString());
        pushUnavailable = true;
        return ApiError.response(req, ApiError.unavailable("push channel unavailable"));
      }
    }
    return resp;
  }

  /**
   * Records the identity Connect established; null if another Connect got there first or the
   * connection has left, otherwise completes once the user's push channel is subscribed.
   */
  private synchronized CompletableFuture<Void> attached(ConnectResponse resp) {
    if (connected || left) {
      return null;
    }
    userId = resp.getUser().getUserId();
    deviceId = resp.getDeviceId();
    connected = true;
    return connections.attach(this);
  }

  @Override
  public String toString() {
    return "conn " + remote + " user " + userId + " device " + deviceId;
  }
}
