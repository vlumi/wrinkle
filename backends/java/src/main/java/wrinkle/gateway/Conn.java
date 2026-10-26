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
import wrinkle.Uuid;
import wrinkle.services.Backend;
import wrinkle.v1.ErrorCode;
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
  // Bounds the Register and SignIn requests of a connection.
  static final int MAX_SIGN_INS = 5;

  private static final WebSocketCloseStatus SIGNED_OUT =
      WebSocketCloseStatus.ofPrivateUse(4001, "signed out");

  private static final byte[] PONG =
      Frame.newBuilder().setPong(Pong.getDefaultInstance()).build().toByteArray();

  /**
   * Where the connection is in its life: before Connect, or after one was undone; connected as a
   * device; a Connect being undone, still bound until the release is done; or left, the client's
   * side having ended, with whatever identity it had.
   */
  private enum State {
    PENDING,
    BOUND,
    RELEASING,
    GONE
  }

  private final Backend backend;
  private final Connections connections;
  private final WebSocketWriter out;
  private final String remote;
  // Touched only by the inbound stream, which delivers one frame at a time.
  private final TokenBucket tokens;
  private final AckCoalescer.Timer ackTimer;
  private final long ackIntervalNanos;
  private final AtomicInteger inFlight = new AtomicInteger();
  private final AtomicInteger queued = new AtomicInteger();
  private final AtomicBoolean closed = new AtomicBoolean();
  private final AtomicBoolean paused = new AtomicBoolean();
  private volatile Subscription inbound;
  private volatile long lastReceived;
  // Touched only by the sweep.
  private long lastPing;
  // Guarded by this: the state, the identity Connect established, null while pending, and the
  // connect deadline.
  private State state = State.PENDING;
  private Identity identity;
  private long connectBy;
  // Guarded by this: Register and SignIn requests, which may cost a password hash each; those
  // refused as invalid, before hashing, do not count.
  private int signIns;
  // Guarded by this; made on the first ack.
  private AckCoalescer acks;

  private record Identity(Uuid userId, long deviceId) {}

  Conn(
      Backend backend,
      Connections connections,
      WebSocketWriter out,
      String remote,
      AckCoalescer.Timer ackTimer,
      long ackIntervalNanos) {
    this.backend = backend;
    this.connections = connections;
    this.out = out;
    this.remote = remote;
    long now = System.nanoTime();
    this.connectBy = now + CONNECT_DEADLINE_NANOS;
    this.lastReceived = now;
    this.lastPing = now;
    this.tokens = new TokenBucket(RATE_BURST, RATE_PER_SECOND, now);
    this.ackTimer = ackTimer;
    this.ackIntervalNanos = ackIntervalNanos;
  }

  /** The user and device Connect established, or null before it. */
  private synchronized Identity identity() {
    return identity;
  }

  /** The user Connect established; null before it. */
  Uuid userId() {
    Identity id = identity();
    return id == null ? null : id.userId();
  }

  /** The device Connect established; 0 before it. */
  long deviceId() {
    Identity id = identity();
    return id == null ? 0 : id.deviceId();
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
      // Keepalive takes no tokens: only protobuf frames count against the rate.
      case PING -> send(WebSocketFrame.ofPong(frame.array()));
      case PONG -> {}
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
        if (allowed && identity() != null) {
          acks().offer(f.getAck().getCursor());
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
      flushAcks();
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
    flushAcks();
  }

  /** Closes the outbound side once the client's side has ended. */
  void end() {
    closed.set(true);
    out.close();
    flushAcks();
  }

  void closeForShutdown() {
    close(WebSocketCloseStatus.ENDPOINT_UNAVAILABLE, "shutting down");
  }

  /** The device was signed out or the account deleted; the client does not reconnect. */
  void closeSignedOut() {
    close(SIGNED_OUT, "signed out");
  }

  /** The periodic check: the connect deadline, idleness, and the keepalive ping. */
  void sweep(long now) {
    if (closed.get()) {
      return;
    }
    if (connectOverdue(now)) {
      close(WebSocketCloseStatus.POLICY_VIOLATION, "connect first");
    } else if (now - lastReceived >= IDLE_TIMEOUT_NANOS) {
      close(WebSocketCloseStatus.ENDPOINT_UNAVAILABLE, "idle");
    } else if (now - lastPing >= PING_INTERVAL_NANOS) {
      lastPing = now;
      send(WebSocketFrame.ofPing());
    }
  }

  private synchronized boolean connectOverdue(long now) {
    return state == State.PENDING && now - connectBy >= 0;
  }

  /** Counts a Register or SignIn; false beyond the connection's limit. */
  private synchronized boolean countSignIn() {
    if (signIns >= MAX_SIGN_INS) {
      return false;
    }
    signIns++;
    return true;
  }

  private synchronized void uncountSignIn() {
    signIns--;
  }

  /** Connect gets this long again: after a Register or SignIn succeeds, or when one was undone. */
  private synchronized void restartConnectDeadline() {
    connectBy = System.nanoTime() + CONNECT_DEADLINE_NANOS;
  }

  /**
   * The connection has left. True the first time, if it was connected and not being released, so
   * its presence must go.
   */
  synchronized boolean leave() {
    if (state == State.GONE) {
      return false;
    }
    boolean release = state == State.BOUND;
    state = State.GONE;
    return release;
  }

  private synchronized AckCoalescer acks() {
    if (acks == null) {
      acks =
          new AckCoalescer(
              this::runAckWrite, ackTimer, System::nanoTime, ackIntervalNanos, this::writeAck);
    }
    return acks;
  }

  /** The connection is ending: its pending ack is written now. */
  private void flushAcks() {
    AckCoalescer a;
    synchronized (this) {
      a = acks;
    }
    if (a != null) {
      a.end();
    }
  }

  /** Counts as a request in flight, so a drain waits for it. */
  private void runAckWrite(Runnable write) {
    connections.requestStarted();
    Thread.startVirtualThread(
        () -> {
          try {
            write.run();
          } finally {
            connections.requestFinished();
          }
        });
  }

  private void writeAck(long cursor) {
    Identity id = identity();
    if (id == null) {
      return;
    }
    try {
      backend.ack(id.userId(), id.deviceId(), cursor);
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
    boolean beforeConnect =
        switch (req.getBodyCase()) {
          case REGISTER, SIGN_IN, CONNECT -> true;
          default -> false;
        };
    Identity id = identity();
    if (beforeConnect && id != null) {
      return ApiError.response(req, ApiError.failedPrecondition("already connected"));
    }
    if (!beforeConnect && id == null) {
      return ApiError.response(req, ApiError.unauthenticated("connect first"));
    }
    boolean signIn = req.hasRegister() || req.hasSignIn();
    if (signIn && !countSignIn()) {
      return ApiError.response(
          req, ApiError.resourceExhausted("at most " + MAX_SIGN_INS + " sign-ins per connection"));
    }
    Response resp;
    try {
      resp = backend.call(id == null ? null : id.userId(), id == null ? 0 : id.deviceId(), req);
    } catch (Exception e) {
      log.warn("backend call for request {} failed: {}", req.getId(), e.toString());
      return ApiError.response(req, ApiError.unavailable("services unavailable"));
    }
    if (resp.hasConnect()) {
      Uuid connectedAs = Uuid.parse(resp.getConnect().getUser().getUserId());
      if (connectedAs == null) {
        log.error("connect returned no user id");
        return ApiError.response(req, new IllegalStateException("connect returned no user id"));
      }
      CompletableFuture<Void> subscribed = attached(connectedAs, resp.getConnect().getDeviceId());
      if (subscribed == null) {
        return ApiError.response(req, ApiError.failedPrecondition("already connected"));
      }
      // Pushes that follow the response must not be missed.
      try {
        subscribed.get(SUBSCRIBE_TIMEOUT_MILLIS, TimeUnit.MILLISECONDS);
      } catch (InterruptedException e) {
        Thread.currentThread().interrupt();
        unattached();
        return ApiError.response(req, ApiError.unavailable("push channel unavailable"));
      } catch (ExecutionException | TimeoutException e) {
        log.warn("push subscription of user {} not ready: {}", connectedAs, e.toString());
        unattached();
        return ApiError.response(req, ApiError.unavailable("push channel unavailable"));
      }
    }
    if (signIn && resp.getError().getCode() == ErrorCode.ERROR_CODE_INVALID_ARGUMENT) {
      uncountSignIn();
    }
    if (signIn && !resp.hasError()) {
      // Hashing the password may have taken a while. A failed attempt does not restart the
      // deadline, so it cannot hold the connection open.
      restartConnectDeadline();
    }
    return resp;
  }

  /**
   * Records the identity Connect established; null if another Connect got there first or the
   * connection has left, otherwise completes once the user's push channel is subscribed.
   */
  private synchronized CompletableFuture<Void> attached(Uuid user, long device) {
    if (state != State.PENDING) {
      return null;
    }
    identity = new Identity(user, device);
    state = State.BOUND;
    return connections.attach(this);
  }

  /**
   * Not connected after all: Connect may be retried, within a fresh deadline. A connection that has
   * left is released by its close instead.
   */
  private void unattached() {
    synchronized (this) {
      if (state == State.GONE) {
        return;
      }
      state = State.RELEASING;
    }
    connections.release(this);
    synchronized (this) {
      if (state == State.RELEASING) {
        state = State.PENDING;
        restartConnectDeadline();
      }
      identity = null;
    }
  }

  @Override
  public String toString() {
    return "conn " + remote + " " + identity();
  }
}
