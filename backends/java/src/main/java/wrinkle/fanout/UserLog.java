package wrinkle.fanout;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.Duration;
import java.time.LocalDateTime;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import javax.sql.DataSource;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.Metrics;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.presence.Presence;
import wrinkle.v1.Event;
import wrinkle.v1.Message;
import wrinkle.v1.ReadEvent;
import wrinkle.v1.User;

/** Each user's event log and conversation list in the user keyspace, as fan-out writes them. */
final class UserLog {
  private static final Logger log = LoggerFactory.getLogger(UserLog.class);
  // Bounds the cache of known list entries; it starts over when full.
  private static final int MAX_LISTED = 1 << 20;

  /** Changes the user's conversation list in the transaction that appends the event. */
  interface ListUpdate {
    void apply(Connection c, Event event) throws SQLException;
  }

  private record ListKey(Uuid userId, Uuid conversationId) {}

  private final DataSource db;
  private final Presence presence;
  // Conversation list entries known to exist; entries are never removed, so one stays true.
  private final Set<ListKey> listed = ConcurrentHashMap.newKeySet();

  UserLog(DataSource db, Presence presence) {
    this.db = db;
    this.presence = presence;
  }

  DataSource db() {
    return db;
  }

  /** Appends a conversation's event to one participant's log, then pushes it. */
  void deliver(Uuid userId, OutboxRecord o) throws SQLException {
    Uuid peerId = o.peerOf(userId);
    User peer = o.event().hasMessage() ? peerForNewEntry(userId, o.conversationId(), peerId) : null;
    Event event =
        append(
            userId,
            o.conversationId(),
            o.outboxId(),
            o.event(),
            (c, e) -> {
              switch (e.getBodyCase()) {
                case MESSAGE -> recordMessage(c, userId, peerId, peer, o.conversationId(), e);
                case READ -> recordRead(c, userId, o.conversationId(), e.getRead());
                default -> {}
              }
            });
    if (event != null && event.hasMessage()) {
      remember(userId, o.conversationId());
    }
  }

  private void remember(Uuid userId, Uuid conversationId) {
    if (listed.size() >= MAX_LISTED) {
      listed.clear();
    }
    listed.add(new ListKey(userId, conversationId));
  }

  /**
   * The peer's current profile when the user's list may not have the conversation yet, for the
   * entry a message creates; null once the entry is known to exist.
   */
  private User peerForNewEntry(Uuid userId, Uuid conversationId, Uuid peerId) throws SQLException {
    if (listed.contains(new ListKey(userId, conversationId))) {
      return null;
    }
    try (Connection c = db.getConnection()) {
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT 1 FROM user_conversation WHERE user_id = ? AND conversation_id = ?",
                  userId,
                  conversationId);
          ResultSet rs = ps.executeQuery()) {
        if (rs.next()) {
          remember(userId, conversationId);
          return null;
        }
      }
      User.Builder peer = User.newBuilder().setUserId(peerId.toByteString());
      try (PreparedStatement ps =
              Sql.prepare(c, "SELECT name, icon FROM user WHERE user_id = ?", peerId);
          ResultSet rs = ps.executeQuery()) {
        if (rs.next()) {
          peer.setName(rs.getString(1)).setIcon(rs.getString(2));
        }
      }
      return peer.build();
    }
  }

  /**
   * Appends the event to the user's log, in one transaction with the list update, then pushes it.
   * Returns the event as stored, or null when there is nothing to push: the user does not exist, or
   * the log already has the event from this source row.
   */
  Event append(Uuid userId, Uuid sourceId, long outboxId, Event source, ListUpdate update)
      throws SQLException {
    Event event;
    byte[] payload;
    try (Connection c = db.getConnection()) {
      c.setAutoCommit(false);
      try {
        Long head = lockHead(c, userId);
        if (head == null) {
          log.warn("no such user {}, dropping event", userId);
          c.rollback();
          return null;
        }
        event = source.toBuilder().setCursor(head + 1).build();
        payload = event.toByteArray();
        try {
          Sql.update(
              c,
              "INSERT INTO user_event (user_id, position, source_id, outbox_id, payload,"
                  + " occurred_at) VALUES (?, ?, ?, ?, ?, ?)",
              userId,
              event.getCursor(),
              sourceId,
              outboxId,
              payload,
              Timestamps.fromProto(event.getOccurredAt()));
        } catch (SQLException e) {
          if (!Sql.isDuplicate(e)) {
            throw e;
          }
          // The unique key on the source outbox row: this is a redelivery.
          c.rollback();
          return null;
        }
        Sql.update(c, "UPDATE user SET log_head = ? WHERE user_id = ?", event.getCursor(), userId);
        if (update != null) {
          update.apply(c, event);
        }
        c.commit();
      } catch (SQLException | RuntimeException e) {
        c.rollback();
        throw e;
      }
    }
    LocalDateTime occurredAt = Timestamps.fromProto(event.getOccurredAt());
    Metrics.FANOUT_DELIVERIES.inc();
    Metrics.FANOUT_LAG.observe(Duration.between(occurredAt, Timestamps.now()).toNanos() / 1e9);
    presence.publish(userId, payload);
    return event;
  }

  /** The user's log head, locked for the transaction, or null if there is no such user. */
  private static Long lockHead(Connection c, Uuid userId) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(c, "SELECT log_head FROM user WHERE user_id = ? FOR UPDATE", userId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? rs.getLong(1) : null;
    }
  }

  /**
   * Adds the conversation to the user's list, or makes the message its last unless a later one is
   * already there. The sender's own read position moves to the message. A new entry takes the
   * peer's profile; an existing one keeps what profile changes put there.
   */
  private static void recordMessage(
      Connection c, Uuid userId, Uuid peerId, User peer, Uuid conversationId, Event event)
      throws SQLException {
    Message msg = event.getMessage().getMessage();
    LocalDateTime sentAt = Timestamps.fromProto(msg.getSentAt());
    LocalDateTime occurredAt = Timestamps.fromProto(event.getOccurredAt());
    long ownReadSeq = userId.matches(msg.getSenderUserId()) ? msg.getSeq() : 0;
    String peerName = peer == null ? "" : peer.getName();
    String peerIcon = peer == null ? "" : peer.getIcon();
    byte[] sender = msg.getSenderUserId().toByteArray();
    Sql.update(
        c,
        "INSERT INTO user_conversation (user_id, conversation_id, peer_user_id, peer_name,"
            + " peer_icon, last_seq, last_sender_user_id, last_client_message_id, last_body,"
            + " last_sent_at, read_seq, peer_read_seq, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?,"
            + " ?, ?, ?, 0, ?) ON DUPLICATE KEY UPDATE last_seq = GREATEST(last_seq, ?),"
            + " last_sender_user_id = IF(? >= last_seq, ?, last_sender_user_id),"
            + " last_client_message_id = IF(? >= last_seq, ?, last_client_message_id),"
            + " last_body = IF(? >= last_seq, ?, last_body),"
            + " last_sent_at = IF(? >= last_seq, ?, last_sent_at),"
            + " read_seq = GREATEST(read_seq, ?),"
            + " updated_at = GREATEST(updated_at, ?)",
        userId,
        conversationId,
        peerId,
        peerName,
        peerIcon,
        msg.getSeq(),
        sender,
        msg.getClientMessageId(),
        msg.getText(),
        sentAt,
        ownReadSeq,
        occurredAt,
        msg.getSeq(),
        msg.getSeq(),
        sender,
        msg.getSeq(),
        msg.getClientMessageId(),
        msg.getSeq(),
        msg.getText(),
        msg.getSeq(),
        sentAt,
        ownReadSeq,
        occurredAt);
  }

  /** Moves the user's own read position or the peer's, whichever the event is about. */
  private static void recordRead(Connection c, Uuid userId, Uuid conversationId, ReadEvent read)
      throws SQLException {
    String column = userId.matches(read.getUserId()) ? "read_seq" : "peer_read_seq";
    Sql.update(
        c,
        "UPDATE user_conversation SET "
            + column
            + " = GREATEST("
            + column
            + ", ?) WHERE user_id = ? AND conversation_id = ?",
        read.getSeq(),
        userId,
        conversationId);
  }
}
