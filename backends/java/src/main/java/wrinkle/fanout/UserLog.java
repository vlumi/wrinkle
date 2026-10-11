package wrinkle.fanout;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.SQLIntegrityConstraintViolationException;
import java.time.Duration;
import java.time.LocalDateTime;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import javax.sql.DataSource;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.Metrics;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.presence.Presence;
import wrinkle.v1.Event;
import wrinkle.v1.Message;

/** Each user's event log and conversation list in the user keyspace, as fan-out writes them. */
final class UserLog {
  private static final Logger log = LoggerFactory.getLogger(UserLog.class);

  private final DataSource db;
  private final Presence presence;
  private final Map<Long, String> names = new ConcurrentHashMap<>();

  UserLog(DataSource db, Presence presence) {
    this.db = db;
    this.presence = presence;
  }

  /**
   * Appends the event to one user's log, in one transaction with the conversation list update, then
   * pushes it. A redelivered record is appended once.
   */
  void append(long userId, OutboxRecord o) throws SQLException {
    long peerId = o.peerOf(userId);
    String peerName = o.event().hasMessage() ? name(peerId) : null;

    try (Connection c = db.getConnection()) {
      c.setAutoCommit(false);
      try {
        Long head = lockHead(c, userId);
        if (head == null) {
          log.warn("no such user {}, dropping event", userId);
          c.rollback();
          return;
        }
        Event event = o.event().toBuilder().setCursor(head + 1).build();
        byte[] payload = event.toByteArray();
        LocalDateTime occurredAt = Timestamps.fromProto(event.getOccurredAt());
        try {
          Sql.update(
              c,
              "INSERT INTO user_event (user_id, position, conversation_id, outbox_id, payload,"
                  + " occurred_at) VALUES (?, ?, ?, ?, ?, ?)",
              userId,
              event.getCursor(),
              o.conversationId(),
              o.outboxId(),
              payload,
              occurredAt);
        } catch (SQLIntegrityConstraintViolationException e) {
          // The unique key on the source outbox row: this is a redelivery.
          c.rollback();
          return;
        }
        Sql.update(c, "UPDATE user SET log_head = ? WHERE user_id = ?", event.getCursor(), userId);
        switch (event.getBodyCase()) {
          case MESSAGE -> upsertConversation(c, userId, peerId, peerName, o, event, occurredAt);
          case READ -> updateReadMark(c, userId, o, event);
          default -> {}
        }
        c.commit();
        Metrics.FANOUT_DELIVERIES.inc();
        Metrics.FANOUT_LAG.observe(Duration.between(occurredAt, Timestamps.now()).toNanos() / 1e9);
        presence.publish(userId, payload);
      } catch (SQLException | RuntimeException e) {
        c.rollback();
        throw e;
      }
    }
  }

  /** The user's log head, locked for the transaction, or null if there is no such user. */
  private static Long lockHead(Connection c, long userId) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(c, "SELECT log_head FROM user WHERE user_id = ? FOR UPDATE", userId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? rs.getLong(1) : null;
    }
  }

  private static void upsertConversation(
      Connection c,
      long userId,
      long peerId,
      String peerName,
      OutboxRecord o,
      Event event,
      LocalDateTime occurredAt)
      throws SQLException {
    Message msg = event.getMessage().getMessage();
    LocalDateTime sentAt = Timestamps.fromProto(msg.getSentAt());
    Sql.update(
        c,
        "INSERT INTO user_conversation (user_id, conversation_id, peer_user_id, peer_name,"
            + " last_seq, last_sender_user_id, last_client_message_id, last_body, last_sent_at,"
            + " read_seq, peer_read_seq, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?)"
            + " ON DUPLICATE KEY UPDATE last_seq = GREATEST(last_seq, ?),"
            + " last_sender_user_id = IF(? >= last_seq, ?, last_sender_user_id),"
            + " last_client_message_id = IF(? >= last_seq, ?, last_client_message_id),"
            + " last_body = IF(? >= last_seq, ?, last_body),"
            + " last_sent_at = IF(? >= last_seq, ?, last_sent_at),"
            + " updated_at = GREATEST(updated_at, ?)",
        userId,
        o.conversationId(),
        peerId,
        peerName,
        msg.getSeq(),
        msg.getSenderUserId(),
        msg.getClientMessageId(),
        msg.getText(),
        sentAt,
        occurredAt,
        msg.getSeq(),
        msg.getSeq(),
        msg.getSenderUserId(),
        msg.getSeq(),
        msg.getClientMessageId(),
        msg.getSeq(),
        msg.getText(),
        msg.getSeq(),
        sentAt,
        occurredAt);
  }

  /** Moves the user's read position in the conversation list forward, or the peer's. */
  private static void updateReadMark(Connection c, long userId, OutboxRecord o, Event event)
      throws SQLException {
    String column = event.getRead().getUserId() == userId ? "read_seq" : "peer_read_seq";
    Sql.update(
        c,
        "UPDATE user_conversation SET "
            + column
            + " = GREATEST("
            + column
            + ", ?) WHERE user_id = ? AND conversation_id = ?",
        event.getRead().getSeq(),
        userId,
        o.conversationId());
  }

  /** The user's name; names never change, so they are cached for good. */
  private String name(long userId) throws SQLException {
    String cached = names.get(userId);
    if (cached != null) {
      return cached;
    }
    try (Connection c = db.getConnection();
        PreparedStatement ps = Sql.prepare(c, "SELECT name FROM user WHERE user_id = ?", userId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw new SQLException("name of user " + userId + ": no such user");
      }
      String name = rs.getString(1);
      names.put(userId, name);
      return name;
    }
  }
}
