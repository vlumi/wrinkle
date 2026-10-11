package wrinkle.gateway;

import com.google.protobuf.CodedOutputStream;
import com.google.protobuf.WireFormat;
import java.io.IOException;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.Metrics;
import wrinkle.presence.Presence;
import wrinkle.v1.Frame;

/**
 * The connected devices on this node by user, with one push subscription per user that has a
 * connection here.
 */
final class Connections {
  private static final Logger log = LoggerFactory.getLogger(Connections.class);

  private final Presence presence;
  private final Map<Long, Set<Conn>> byUser = new ConcurrentHashMap<>();

  Connections(Presence presence) {
    this.presence = presence;
    presence.listen(this::push);
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

  void closeAll() {
    for (Set<Conn> conns : byUser.values()) {
      for (Conn c : conns) {
        c.closeForRestart();
      }
    }
  }

  private void push(long userId, byte[] event) {
    Set<Conn> conns = byUser.get(userId);
    if (conns == null) {
      return;
    }
    byte[] frame = eventFrame(event);
    for (Conn c : conns) {
      c.send(frame);
      Metrics.EVENTS_PUSHED.inc();
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
