package wrinkle.gateway;

import com.linecorp.armeria.common.websocket.WebSocket;
import com.linecorp.armeria.common.websocket.WebSocketFrame;
import com.linecorp.armeria.common.websocket.WebSocketWriter;
import com.linecorp.armeria.server.ServerBuilder;
import com.linecorp.armeria.server.ServiceRequestContext;
import com.linecorp.armeria.server.websocket.WebSocketService;
import com.linecorp.armeria.server.websocket.WebSocketServiceHandler;
import org.reactivestreams.Subscriber;
import org.reactivestreams.Subscription;
import wrinkle.Metrics;
import wrinkle.presence.Presence;
import wrinkle.services.Backend;

/**
 * The WebSocket endpoint: hands requests to the services, in-process or in a services process, and
 * pushes events to connected devices.
 */
public final class Gateway implements WebSocketServiceHandler {
  private static final int MAX_FRAME_BYTES = 64 * 1024;

  private final Backend backend;
  private final Connections connections;

  public Gateway(Backend backend, Presence presence) {
    this.backend = backend;
    this.connections = new Connections(presence);
  }

  public void register(ServerBuilder sb) {
    sb.service(
        "/v1/ws",
        WebSocketService.builder(this)
            .allowedOrigins("*")
            .maxFramePayloadLength(MAX_FRAME_BYTES)
            .build());
  }

  @Override
  public WebSocket handle(ServiceRequestContext ctx, WebSocket in) {
    WebSocketWriter out = WebSocket.streaming();
    Conn conn = new Conn(backend, connections, out, ctx.remoteAddress().toString());
    Metrics.CONNECTIONS.inc();
    in.subscribe(
        new Subscriber<WebSocketFrame>() {
          @Override
          public void onSubscribe(Subscription s) {
            s.request(Long.MAX_VALUE);
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
            connections.detach(conn);
          }
        });
    return out;
  }

  public void close() {
    connections.closeAll();
  }
}
