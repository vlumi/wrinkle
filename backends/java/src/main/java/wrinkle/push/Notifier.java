package wrinkle.push;

import java.io.IOException;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Semaphore;
import javax.sql.DataSource;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.Metrics;
import wrinkle.Sql;
import wrinkle.Uuid;
import wrinkle.contacts.Contacts;
import wrinkle.presence.Presence;
import wrinkle.v1.Message;
import wrinkle.v1.MessageDeletedEvent;
import wrinkle.v1.PushService;
import wrinkle.v1.Sealed;

/**
 * Pushes a message, or the deletion of one, through APNs to a user's devices that are not connected
 * and have registered a token: the fan-out tells it after appending a message or a deletion to a
 * user's log; connected devices get the event over their WebSocket instead. Sends run in the
 * background, bounded, so the fan-out never waits for APNs.
 */
public final class Notifier {
  private static final Logger log = LoggerFactory.getLogger(Notifier.class);
  private static final int MAX_IN_FLIGHT = 64;
  private static final Duration SEND_TIMEOUT = Duration.ofSeconds(10);
  // What an alert says when the extension cannot decrypt the message.
  private static final String GENERIC_BODY = "New message";

  private final Apns apns;
  private final DataSource db;
  private final Contacts contacts;
  private final Presence presence;
  private final Semaphore slots = new Semaphore(MAX_IN_FLIGHT);
  private final ExecutorService tasks = Executors.newVirtualThreadPerTaskExecutor();

  public Notifier(Apns apns, DataSource userDb, Contacts contacts, Presence presence) {
    this.apns = apns;
    this.db = userDb;
    this.contacts = contacts;
    this.presence = presence;
  }

  private record Registration(long deviceId, String service, String token) {}

  private interface Send {
    void run(Instant deadline) throws Exception;
  }

  /**
   * After a message was appended to the user's log. The sender's own devices are not notified, nor
   * anyone of notices, nor in a group a user who blocked the sender.
   */
  public void messageDelivered(Uuid userId, Message msg, boolean group) {
    if (userId.matches(msg.getSenderUserId()) || msg.hasNotice()) {
      return;
    }
    Uuid senderId = group ? Uuid.parse(msg.getSenderUserId()) : null;
    background(userId, deadline -> notify(userId, senderId, msg, deadline));
  }

  /**
   * After a deletion was appended to the user's log. A background notification lets the app
   * withdraw the message's alert if it showed one; none goes to a user who blocked the sender, when
   * the sender is given.
   */
  public void messageDeleted(Uuid userId, MessageDeletedEvent deleted, Uuid senderId) {
    background(userId, deadline -> withdraw(userId, senderId, deleted, deadline));
  }

  /** Runs a send on a virtual thread, when a slot is free. */
  private void background(Uuid userId, Send send) {
    if (!slots.tryAcquire()) {
      // APNs is slow or down; the device syncs when it opens anyway.
      Metrics.PUSH_SENT.labelValues("dropped").inc();
      return;
    }
    tasks.execute(
        () -> {
          try {
            send.run(Instant.now().plus(SEND_TIMEOUT));
          } catch (Exception e) {
            log.warn("push to user {} failed: {}", userId, e.toString());
          } finally {
            slots.release();
          }
        });
  }

  /** The user's registered devices, none when the user blocked the sender, if one is given. */
  private List<Registration> registrationsFor(Uuid userId, Uuid senderId) throws SQLException {
    List<Registration> regs = registrations(userId);
    if (regs.isEmpty() || senderId == null) {
      return regs;
    }
    return contacts.blocks(userId, senderId) ? List.of() : regs;
  }

  private void notify(Uuid userId, Uuid senderId, Message msg, Instant deadline)
      throws SQLException, InterruptedException {
    List<Registration> regs = registrationsFor(userId, senderId);
    if (regs.isEmpty()) {
      return;
    }
    Uuid conversationId = Uuid.parse(msg.getConversationId());
    if (conversationId == null) {
      throw new IllegalArgumentException("message without a conversation id");
    }
    String title = alertTitle(userId, conversationId);
    int badge = unread(userId);
    sendDisconnected(userId, regs, messageAlert(title, badge, conversationId, msg), deadline);
  }

  /**
   * The notification of a message: the server cannot read it, so the body is generic and the sealed
   * content rides along, when it fits, for the app's extension to decrypt and show instead.
   */
  static Apns.Alert messageAlert(String title, int badge, Uuid conversationId, Message msg) {
    // Clients find the conversation by the lowercase hex of its id.
    Map<String, Object> plain = new LinkedHashMap<>();
    plain.put("conversation_id", conversationId.hex());
    plain.put("seq", msg.getSeq());
    Apns.Alert alert =
        new Apns.Alert(title, GENERIC_BODY, badge, conversationId.hex(), false, true, plain);
    if (!msg.hasSealed()) {
      return alert;
    }
    Sealed sealed = msg.getSealed();
    Map<String, Object> data = new LinkedHashMap<>(plain);
    data.put("epoch", Integer.toUnsignedLong(sealed.getEpoch()));
    data.put("nonce", Base64.getEncoder().encodeToString(sealed.getNonce().toByteArray()));
    data.put(
        "ciphertext", Base64.getEncoder().encodeToString(sealed.getCiphertext().toByteArray()));
    Uuid sender = Uuid.parse(msg.getSenderUserId());
    data.put("sender_user_id", sender == null ? "" : sender.hex());
    data.put("client_message_id", msg.getClientMessageId());
    Apns.Alert full =
        new Apns.Alert(title, GENERIC_BODY, badge, conversationId.hex(), false, true, data);
    return full.payload().length > Apns.MAX_PAYLOAD ? alert : full;
  }

  private void withdraw(Uuid userId, Uuid senderId, MessageDeletedEvent deleted, Instant deadline)
      throws SQLException, InterruptedException {
    List<Registration> regs = registrationsFor(userId, senderId);
    if (regs.isEmpty()) {
      return;
    }
    Uuid conversationId = Uuid.parse(deleted.getConversationId());
    if (conversationId == null) {
      throw new IllegalArgumentException("deletion without a conversation id");
    }
    Map<String, Object> data = new LinkedHashMap<>();
    data.put("conversation_id", conversationId.hex());
    data.put("seq", deleted.getSeq());
    data.put("deleted", true);
    sendDisconnected(userId, regs, Apns.Alert.background(data), deadline);
  }

  /** Sends the alert to each registered device that is not connected. */
  private void sendDisconnected(
      Uuid userId, List<Registration> regs, Apns.Alert alert, Instant deadline)
      throws InterruptedException {
    Set<Long> connected = presence.connectedDevices(userId);
    for (Registration r : regs) {
      if (!connected.contains(r.deviceId())) {
        send(userId, r, alert, deadline);
      }
    }
  }

  /**
   * The notification's title: in a direct conversation the peer's current name, from the
   * conversation list; in a group the topic.
   */
  private String alertTitle(Uuid userId, Uuid conversationId) throws SQLException {
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT kind, peer_name, topic FROM user_conversation WHERE user_id = ? AND"
                    + " conversation_id = ?",
                userId,
                conversationId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw new SQLException("conversation entry: no such row");
      }
      return rs.getString(1).equals("group") ? rs.getString(3) : rs.getString(2);
    }
  }

  private void send(Uuid userId, Registration r, Apns.Alert alert, Instant deadline)
      throws InterruptedException {
    Duration left = Duration.between(Instant.now(), deadline);
    if (left.isNegative() || left.isZero()) {
      Metrics.PUSH_SENT.labelValues("error").inc();
      log.warn("push send to user {} device {} timed out", userId, r.deviceId());
      return;
    }
    boolean sandbox = r.service().equals(PushService.PUSH_SERVICE_APNS_SANDBOX.name());
    try {
      apns.send(sandbox, r.token(), alert, left);
      Metrics.PUSH_SENT.labelValues("ok").inc();
    } catch (Apns.UnregisteredException e) {
      Metrics.PUSH_SENT.labelValues("unregistered").inc();
      try {
        unregister(userId, r);
      } catch (SQLException ex) {
        log.warn("clearing push token failed: {}", ex.toString());
      }
    } catch (IOException e) {
      Metrics.PUSH_SENT.labelValues("error").inc();
      log.warn("push send to user {} device {} failed: {}", userId, r.deviceId(), e.toString());
    }
  }

  private List<Registration> registrations(Uuid userId) throws SQLException {
    List<Registration> regs = new ArrayList<>();
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT device_id, push_service, push_token FROM device WHERE user_id = ? AND"
                    + " push_token IS NOT NULL",
                userId);
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        String service = rs.getString(2);
        regs.add(new Registration(rs.getLong(1), service == null ? "" : service, rs.getString(3)));
      }
    }
    return regs;
  }

  /** The user's unread messages across conversations, for the badge. */
  private int unread(Uuid userId) throws SQLException {
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT COALESCE(SUM(last_seq - read_seq), 0) FROM user_conversation WHERE"
                    + " user_id = ?",
                userId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? rs.getInt(1) : 0;
    }
  }

  /** Removes a token APNs rejected, unless the device has registered a new one meanwhile. */
  private void unregister(Uuid userId, Registration r) throws SQLException {
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          "UPDATE device SET push_service = NULL, push_token = NULL WHERE user_id = ? AND"
              + " device_id = ? AND push_token = ?",
          userId,
          r.deviceId(),
          r.token());
    }
  }
}
