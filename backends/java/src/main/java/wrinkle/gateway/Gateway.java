package wrinkle.gateway;

import com.linecorp.armeria.common.HttpResponse;
import com.linecorp.armeria.common.HttpStatus;
import com.linecorp.armeria.common.websocket.WebSocket;
import com.linecorp.armeria.common.websocket.WebSocketFrame;
import com.linecorp.armeria.common.websocket.WebSocketWriter;
import com.linecorp.armeria.server.ServerBuilder;
import com.linecorp.armeria.server.ServiceRequestContext;
import com.linecorp.armeria.server.websocket.WebSocketService;
import com.linecorp.armeria.server.websocket.WebSocketServiceHandler;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.function.BooleanSupplier;
import org.reactivestreams.Subscriber;
import org.reactivestreams.Subscription;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.Metrics;
import wrinkle.presence.Presence;
import wrinkle.services.Backend;

/**
 * The WebSocket endpoint: hands requests to the services, in-process or in a services process, and
 * pushes events to connected devices.
 */
public final class Gateway implements WebSocketServiceHandler {
  private static final Logger log = LoggerFactory.getLogger(Gateway.class);
  private static final int MAX_FRAME_BYTES = 64 * 1024;
  private static final long SWEEP_INTERVAL_MILLIS = 1000;
  private static final long HEARTBEAT_INTERVAL_MILLIS = 30_000;
  private static final long DRAIN_TIMEOUT_MILLIS = 5000;

  private final Backend backend;
  private final Presence presence;
  private final Connections connections;
  private final ScheduledExecutorService timer =
      Executors.newSingleThreadScheduledExecutor(
          Thread.ofPlatform().name("gateway-timer").daemon().factory());

  public Gateway(Backend backend, Presence presence) {
    this.backend = backend;
    this.presence = presence;
    this.connections = new Connections(presence);
  }

  /** Serves WebSocket upgrades until draining, then refuses them. */
  public void register(ServerBuilder sb, BooleanSupplier draining) {
    sb.service(
        "/v1/ws",
        WebSocketService.builder(this)
            .allowedOrigins("*")
            .maxFramePayloadLength(MAX_FRAME_BYTES)
            .aggregateContinuation(true)
            .build()
            .decorate(
                (delegate, ctx, req) ->
                    draining.getAsBoolean()
                        ? HttpResponse.of(HttpStatus.SERVICE_UNAVAILABLE)
                        : delegate.serve(ctx, req)));
  }

  /** Announces this instance and starts the periodic connection checks. */
  public void start() {
    presence.heartbeat();
    timer.scheduleWithFixedDelay(
        () -> run("heartbeat", presence::heartbeat),
        HEARTBEAT_INTERVAL_MILLIS,
        HEARTBEAT_INTERVAL_MILLIS,
        TimeUnit.MILLISECONDS);
    timer.scheduleWithFixedDelay(
        () -> run("sweep", connections::sweep),
        SWEEP_INTERVAL_MILLIS,
        SWEEP_INTERVAL_MILLIS,
        TimeUnit.MILLISECONDS);
  }

  /** Keeps a failure from cancelling the periodic task. */
  private static void run(String task, Runnable r) {
    try {
      r.run();
    } catch (RuntimeException e) {
      log.warn("{} failed", task, e);
    }
  }

  @Override
  public WebSocket handle(ServiceRequestContext ctx, WebSocket in) {
    WebSocketWriter out = WebSocket.streaming();
    Conn conn = new Conn(backend, connections, out, ctx.remoteAddress().toString());
    connections.opened(conn);
    Metrics.CONNECTIONS.inc();
    in.subscribe(
        new Subscriber<WebSocketFrame>() {
          @Override
          public void onSubscribe(Subscription s) {
            conn.subscribed(s);
          }

          @Override
          public void onNext(WebSocketFrame frame) {
            conn.receive(frame);
          }

          @Override
          public void onError(Throwable t) {
            end();
          }

          @Override
          public void onComplete() {
            end();
          }

          private void end() {
            Metrics.CONNECTIONS.dec();
            conn.end();
            connections.closed(conn);
          }
        });
    return out;
  }

  /**
   * Closes every connection and removes its presence, gives requests in flight time to finish, and
   * withdraws this instance.
   */
  public void drain() throws InterruptedException {
    timer.shutdownNow();
    connections.drain(DRAIN_TIMEOUT_MILLIS);
    presence.withdraw();
  }
}
