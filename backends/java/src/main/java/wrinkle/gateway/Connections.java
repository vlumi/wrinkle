package wrinkle.gateway;

import com.google.protobuf.CodedOutputStream;
import com.google.protobuf.WireFormat;
import java.io.IOException;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.Metrics;
import wrinkle.Uuid;
import wrinkle.presence.Presence;
import wrinkle.v1.Frame;

/**
 * The connections on this node, and the connected devices among them by user, with one push
 * subscription per user that has a connection here.
 */
final class Connections {
  private static final Logger log = LoggerFactory.getLogger(Connections.class);
  private static final long DRAIN_POLL_MILLIS = 20;

  private final Presence presence;
  private final Subscriptions<Uuid> subscriptions;
  private final Set<Conn> all = ConcurrentHashMap.newKeySet();
  private final Map<Uuid, Set<Conn>> byUser = new ConcurrentHashMap<>();
  private final AtomicInteger inFlight = new AtomicInteger();

  Connections(Presence presence) {
    this.presence = presence;
    this.subscriptions =
        new Subscriptions<>(presence::subscribe, presence::unsubscribe, Thread::startVirtualThread);
    presence.listen(this::push);
  }

  void opened(Conn c) {
    all.add(c);
  }

  void closed(Conn c) {
    all.remove(c);
    detach(c);
  }

  void requestStarted() {
    inFlight.incrementAndGet();
  }

  void requestFinished() {
    inFlight.decrementAndGet();
  }

  void sweep() {
    long now = System.nanoTime();
    for (Conn c : all) {
      c.sweep(now);
    }
  }

  /**
   * Routes the user's pushes to the connection; completes once the user's channel is subscribed. No
   * network call happens on the caller's thread.
   */
  CompletableFuture<Void> attach(Conn c) {
    // A detach may remove the set it finds before it is locked.
    while (true) {
      Set<Conn> conns = byUser.computeIfAbsent(c.userId(), k -> ConcurrentHashMap.newKeySet());
      synchronized (conns) {
        if (byUser.get(c.userId()) == conns) {
          conns.add(c);
          break;
        }
      }
    }
    CompletableFuture<Void> subscribed = subscriptions.add(c.userId());
    presence.deviceConnected(c.userId(), c.deviceId());
    return subscribed;
  }

  private void detach(Conn c) {
    if (c.leave()) {
      release(c);
    }
  }

  /** Undoes an attach: of a connection that has left, or of a Connect that did not complete. */
  void release(Conn c) {
    Set<Conn> conns = byUser.get(c.userId());
    if (conns != null) {
      synchronized (conns) {
        conns.remove(c);
        if (conns.isEmpty()) {
          byUser.remove(c.userId(), conns);
        }
      }
    }
    subscriptions.remove(c.userId());
    try {
      presence.deviceDisconnected(c.userId(), c.deviceId());
    } catch (RuntimeException e) {
      log.warn("presence update failed", e);
    }
  }

  /**
   * Closes every connection and removes its presence, then waits until the requests in flight
   * finish and the connections end, or the timeout passes.
   */
  void drain(long timeoutMillis) throws InterruptedException {
    for (Conn c : all) {
      c.closeForShutdown();
      detach(c);
    }
    long deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMillis);
    while ((inFlight.get() > 0 || !all.isEmpty()) && System.nanoTime() < deadline) {
      Thread.sleep(DRAIN_POLL_MILLIS);
    }
    subscriptions.stop();
  }

  private void push(Uuid userId, byte[] event) {
    Set<Conn> conns = byUser.get(userId);
    if (conns == null) {
      return;
    }
    byte[] frame = eventFrame(event);
    for (Conn c : conns) {
      if (c.push(frame)) {
        Metrics.EVENTS_PUSHED.inc();
      }
    }
  }

  /** Wraps an already serialized Event in a Frame without decoding it. */
  private static byte[] eventFrame(byte[] event) {
    int tagSize = CodedOutputStream.computeTagSize(Frame.EVENT_FIELD_NUMBER);
    int lengthSize = CodedOutputStream.computeUInt32SizeNoTag(event.length);
    byte[] frame = new byte[tagSize + lengthSize + event.length];
    CodedOutputStream out = CodedOutputStream.newInstance(frame);
    try {
      out.writeTag(Frame.EVENT_FIELD_NUMBER, WireFormat.WIRETYPE_LENGTH_DELIMITED);
      out.writeUInt32NoTag(event.length);
      out.writeRawBytes(event);
      out.flush();
    } catch (IOException e) {
      throw new IllegalStateException(e);
    }
    return frame;
  }
}
