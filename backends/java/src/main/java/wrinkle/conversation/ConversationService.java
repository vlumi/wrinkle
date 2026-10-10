package wrinkle.conversation;

import com.google.protobuf.Timestamp;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.Instant;
import java.time.LocalDateTime;
import java.time.ZoneOffset;
import java.util.ArrayList;
import java.util.List;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Ids;
import wrinkle.v1.Event;
import wrinkle.v1.Message;
import wrinkle.v1.MessageEvent;
import wrinkle.v1.ReadEvent;

/**
 * The conversation service: sending, read marks and history, against the conversation keyspace.
 * Every change is stored with an outbox row in the same transaction.
 */
public final class ConversationService {
  private static final int MAX_CLIENT_MESSAGE_ID = 64;
  private static final int MAX_TEXT_LENGTH = 4000;
  private static final int DEFAULT_PAGE_SIZE = 50;
  private static final int MAX_PAGE_SIZE = 500;

  private final DataSource db;

  public ConversationService(DataSource db) {
    this.db = db;
  }

  public static LocalDateTime now() {
    return LocalDateTime.now(ZoneOffset.UTC);
  }

  public static Timestamp toProto(LocalDateTime t) {
    Instant i = t.toInstant(ZoneOffset.UTC);
    return Timestamp.newBuilder().setSeconds(i.getEpochSecond()).setNanos(i.getNano()).build();
  }

  public static LocalDateTime fromProto(Timestamp t) {
    return LocalDateTime.ofEpochSecond(t.getSeconds(), t.getNanos(), ZoneOffset.UTC);
  }

  /** Creates the conversation between two users if it does not exist. */
  public void ensure(long conversationId, long userA, long userB) throws SQLException {
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            c.prepareStatement(
                "INSERT IGNORE INTO conversation (conversation_id, user_a, user_b, last_seq,"
                    + " created_at) VALUES (?, ?, ?, 0, ?)")) {
      ps.setLong(1, conversationId);
      ps.setLong(2, userA);
      ps.setLong(3, userB);
      ps.setObject(4, now());
      ps.executeUpdate();
    }
  }

  private record Header(long userA, long userB, long lastSeq) {
    boolean has(long userId) {
      return userId == userA || userId == userB;
    }
  }

  private static Header lock(Connection c, long conversationId, long userId, boolean forUpdate)
      throws SQLException {
    try (PreparedStatement ps =
        c.prepareStatement(
            "SELECT user_a, user_b, last_seq FROM conversation WHERE conversation_id = ?"
                + (forUpdate ? " FOR UPDATE" : ""))) {
      ps.setLong(1, conversationId);
      try (ResultSet rs = ps.executeQuery()) {
        if (!rs.next()) {
          throw ApiError.notFound("conversation " + conversationId + " does not exist");
        }
        Header h = new Header(rs.getLong(1), rs.getLong(2), rs.getLong(3));
        if (!h.has(userId)) {
          throw ApiError.permissionDenied(
              "user " + userId + " is not in conversation " + conversationId);
        }
        return h;
      }
    }
  }

  /**
   * Stores a message, assigning the next sequence number. Resending with the same client message id
   * returns the message stored the first time.
   */
  public Message send(long userId, long conversationId, String clientMessageId, String text)
      throws SQLException {
    if (clientMessageId.isEmpty() || clientMessageId.length() > MAX_CLIENT_MESSAGE_ID) {
      throw ApiError.invalidArgument(
          "client_message_id must be 1 to " + MAX_CLIENT_MESSAGE_ID + " bytes");
    }
    if (text.isEmpty() || text.codePointCount(0, text.length()) > MAX_TEXT_LENGTH) {
      throw ApiError.invalidArgument("text must be 1 to " + MAX_TEXT_LENGTH + " characters");
    }
    try (Connection c = db.getConnection()) {
      c.setAutoCommit(false);
      try {
        Header h = lock(c, conversationId, userId, true);
        Message.Builder msg =
            Message.newBuilder()
                .setConversationId(conversationId)
                .setSenderUserId(userId)
                .setClientMessageId(clientMessageId)
                .setText(text);
        try (PreparedStatement ps =
            c.prepareStatement(
                "SELECT seq, body, sent_at FROM message WHERE conversation_id = ? AND"
                    + " sender_user_id = ? AND client_message_id = ?")) {
          ps.setLong(1, conversationId);
          ps.setLong(2, userId);
          ps.setString(3, clientMessageId);
          try (ResultSet rs = ps.executeQuery()) {
            if (rs.next()) {
              c.rollback();
              return msg.setSeq(rs.getLong(1))
                  .setText(rs.getString(2))
                  .setSentAt(toProto(rs.getObject(3, LocalDateTime.class)))
                  .build();
            }
          }
        }
        LocalDateTime sentAt = now();
        long seq = h.lastSeq() + 1;
        msg.setSeq(seq).setSentAt(toProto(sentAt));
        try (PreparedStatement ps =
            c.prepareStatement("UPDATE conversation SET last_seq = ? WHERE conversation_id = ?")) {
          ps.setLong(1, seq);
          ps.setLong(2, conversationId);
          ps.executeUpdate();
        }
        try (PreparedStatement ps =
            c.prepareStatement(
                "INSERT INTO message (conversation_id, seq, sender_user_id, client_message_id,"
                    + " body, sent_at) VALUES (?, ?, ?, ?, ?, ?)")) {
          ps.setLong(1, conversationId);
          ps.setLong(2, seq);
          ps.setLong(3, userId);
          ps.setString(4, clientMessageId);
          ps.setString(5, text);
          ps.setObject(6, sentAt);
          ps.executeUpdate();
        }
        Message built = msg.build();
        Event event =
            Event.newBuilder()
                .setOccurredAt(built.getSentAt())
                .setMessage(MessageEvent.newBuilder().setMessage(built))
                .build();
        insertOutbox(c, h, conversationId, "message", event, sentAt);
        c.commit();
        return built;
      } catch (SQLException | RuntimeException e) {
        c.rollback();
        throw e;
      }
    }
  }

  /** Moves the user's read position forward; backwards or in place does nothing. */
  public void markRead(long userId, long conversationId, long seq) throws SQLException {
    if (seq <= 0) {
      throw ApiError.invalidArgument("seq must be positive");
    }
    try (Connection c = db.getConnection()) {
      c.setAutoCommit(false);
      try {
        Header h = lock(c, conversationId, userId, true);
        if (seq > h.lastSeq()) {
          throw ApiError.invalidArgument(
              "seq " + seq + " is beyond the last message " + h.lastSeq());
        }
        long current = 0;
        try (PreparedStatement ps =
            c.prepareStatement(
                "SELECT seq FROM read_mark WHERE conversation_id = ? AND user_id = ? FOR UPDATE")) {
          ps.setLong(1, conversationId);
          ps.setLong(2, userId);
          try (ResultSet rs = ps.executeQuery()) {
            if (rs.next()) {
              current = rs.getLong(1);
            }
          }
        }
        if (current >= seq) {
          c.rollback();
          return;
        }
        LocalDateTime at = now();
        try (PreparedStatement ps =
            c.prepareStatement(
                "INSERT INTO read_mark (conversation_id, user_id, seq, updated_at) VALUES (?, ?,"
                    + " ?, ?) ON DUPLICATE KEY UPDATE seq = ?, updated_at = ?")) {
          ps.setLong(1, conversationId);
          ps.setLong(2, userId);
          ps.setLong(3, seq);
          ps.setObject(4, at);
          ps.setLong(5, seq);
          ps.setObject(6, at);
          ps.executeUpdate();
        }
        Event event =
            Event.newBuilder()
                .setOccurredAt(toProto(at))
                .setRead(
                    ReadEvent.newBuilder()
                        .setConversationId(conversationId)
                        .setUserId(userId)
                        .setSeq(seq))
                .build();
        insertOutbox(c, h, conversationId, "read", event, at);
        c.commit();
      } catch (SQLException | RuntimeException e) {
        c.rollback();
        throw e;
      }
    }
  }

  /** Messages with seq below beforeSeq, newest first. */
  public List<Message> messages(long userId, long conversationId, long beforeSeq, int limit)
      throws SQLException {
    try (Connection c = db.getConnection()) {
      lock(c, conversationId, userId, false);
      if (beforeSeq == 0) {
        beforeSeq = Long.MAX_VALUE;
      }
      if (limit == 0) {
        limit = DEFAULT_PAGE_SIZE;
      }
      limit = Math.min(limit, MAX_PAGE_SIZE);
      try (PreparedStatement ps =
          c.prepareStatement(
              "SELECT seq, sender_user_id, client_message_id, body, sent_at FROM message WHERE"
                  + " conversation_id = ? AND seq < ? ORDER BY seq DESC LIMIT ?")) {
        ps.setLong(1, conversationId);
        ps.setLong(2, beforeSeq);
        ps.setInt(3, limit);
        List<Message> out = new ArrayList<>();
        try (ResultSet rs = ps.executeQuery()) {
          while (rs.next()) {
            out.add(
                Message.newBuilder()
                    .setConversationId(conversationId)
                    .setSeq(rs.getLong(1))
                    .setSenderUserId(rs.getLong(2))
                    .setClientMessageId(rs.getString(3))
                    .setText(rs.getString(4))
                    .setSentAt(toProto(rs.getObject(5, LocalDateTime.class)))
                    .build());
          }
        }
        return out;
      }
    }
  }

  private static void insertOutbox(
      Connection c, Header h, long conversationId, String kind, Event event, LocalDateTime at)
      throws SQLException {
    try (PreparedStatement ps =
        c.prepareStatement(
            "INSERT INTO outbox (conversation_id, outbox_id, user_a, user_b, kind, payload,"
                + " created_at) VALUES (?, ?, ?, ?, ?, ?, ?)")) {
      ps.setLong(1, conversationId);
      ps.setLong(2, Ids.random());
      ps.setLong(3, h.userA());
      ps.setLong(4, h.userB());
      ps.setString(5, kind);
      ps.setBytes(6, event.toByteArray());
      ps.setObject(7, at);
      ps.executeUpdate();
    }
  }
}
