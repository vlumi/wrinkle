package wrinkle.gateway;

import com.linecorp.armeria.common.websocket.WebSocket;
import com.linecorp.armeria.common.websocket.WebSocketFrame;
import com.linecorp.armeria.common.websocket.WebSocketWriter;
import com.linecorp.armeria.server.ServerBuilder;
import com.linecorp.armeria.server.ServiceRequestContext;
import com.linecorp.armeria.server.websocket.WebSocketService;
import com.linecorp.armeria.server.websocket.WebSocketServiceHandler;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import org.reactivestreams.Subscriber;
import org.reactivestreams.Subscription;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.Metrics;
import wrinkle.conversation.ConversationService;
import wrinkle.devicesync.DeviceSyncService;
import wrinkle.presence.Presence;

/**
 * Holds the WebSocket connections: dispatches requests to the services and pushes events to
 * connected devices. One push subscription per user with a connection on this node.
 */
public final class Gateway implements WebSocketServiceHandler {
  private static final Logger log = LoggerFactory.getLogger(Gateway.class);
  static final int MAX_FRAME_BYTES = 64 * 1024;

  final ConversationService conversations;
  final DeviceSyncService users;
  final Presence presence;
  private final Map<Long, Set<Conn>> byUser = new ConcurrentHashMap<>();

  public Gateway(ConversationService conversations, DeviceSyncService users, Presence presence) {
    this.conversations = conversations;
    this.users = users;
    this.presence = presence;
    presence.listen(this::push);
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
    Conn conn = new Conn(this, out, ctx.remoteAddress().toString());
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
            conn.closed();
            detach(conn);
          }
        });
    return out;
  }

  /** Wraps an already serialized Event in a Frame without decoding it: Frame.event is field 3. */
  private void push(long userId, byte[] payload) {
    Set<Conn> conns = byUser.get(userId);
    if (conns == null) {
      return;
    }
    byte[] frame = Conn.eventFrame(payload);
    for (Conn c : conns) {
      c.send(frame);
    }
  }

  void attach(Conn c) {
    Set<Conn> conns = byUser.computeIfAbsent(c.userId(), k -> ConcurrentHashMap.newKeySet());
    boolean first;
    synchronized (conns) {
      first = conns.isEmpty();
      conns.add(c);
    }
    if (first) {
      presence.subscribe(c.userId());
    }
    presence.deviceConnected(c.userId(), c.deviceId());
  }

  void detach(Conn c) {
    if (!c.isConnected()) {
      return;
    }
    Set<Conn> conns = byUser.get(c.userId());
    boolean last = false;
    if (conns != null) {
      synchronized (conns) {
        conns.remove(c);
        if (conns.isEmpty()) {
          byUser.remove(c.userId(), conns);
          last = true;
        }
      }
    }
    try {
      if (last) {
        presence.unsubscribe(c.userId());
      }
      presence.deviceDisconnected(c.userId(), c.deviceId());
    } catch (RuntimeException e) {
      log.warn("presence update failed", e);
    }
  }

  public void close() {
    for (Set<Conn> conns : byUser.values()) {
      for (Conn c : conns) {
        c.close();
      }
    }
  }
}
