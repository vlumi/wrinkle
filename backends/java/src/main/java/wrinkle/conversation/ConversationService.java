package wrinkle.conversation;

import com.google.protobuf.ByteString;
import java.nio.charset.StandardCharsets;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.List;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Ids;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.v1.Event;
import wrinkle.v1.Message;
import wrinkle.v1.MessageEvent;
import wrinkle.v1.ReadEvent;

/**
 * The conversation service: sending, read marks and history, against the conversation keyspace.
 * Every change is stored with an outbox row in the same transaction, which also deletes it: the
 * change data capture picks up the insert, and the table stays empty.
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

  /**
   * Finds or creates the conversation between two users, userA bytewise before userB. The pair row,
   * on userA's shard, decides between two opening at once; a pair whose conversation row is missing
   * gets it here.
   */
  public Uuid open(Uuid userA, Uuid userB) throws SQLException {
    try (Connection c = db.getConnection()) {
      Uuid conversationId = pair(c, userA, userB);
      if (conversationId == null) {
        conversationId = Uuid.random();
        try {
          Sql.update(
              c,
              "INSERT INTO conversation_pair (user_a, user_b, conversation_id, created_at) VALUES"
                  + " (?, ?, ?, ?)",
              userA,
              userB,
              conversationId,
              Timestamps.now());
        } catch (SQLException e) {
          if (!Sql.isDuplicate(e)) {
            throw e;
          }
          conversationId = pair(c, userA, userB);
          if (conversationId == null) {
            throw new SQLException("conversation pair missing after a duplicate key", e);
          }
        }
      }
      Sql.update(
          c,
          "INSERT IGNORE INTO conversation (conversation_id, user_a, user_b, last_seq, created_at)"
              + " VALUES (?, ?, ?, 0, ?)",
          conversationId,
          userA,
          userB,
          Timestamps.now());
      return conversationId;
    }
  }

  private static Uuid pair(Connection c, Uuid userA, Uuid userB) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT conversation_id FROM conversation_pair WHERE user_a = ? AND user_b = ?",
                userA,
                userB);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? Uuid.of(rs.getBytes(1)) : null;
    }
  }

  private record Header(Uuid userA, Uuid userB, long lastSeq) {
    boolean includes(Uuid userId) {
      return userId.equals(userA) || userId.equals(userB);
    }
  }

  /** The conversation's header, locked for the transaction, if the user is in it. */
  private static Header lockHeader(Connection c, Uuid conversationId, Uuid userId)
      throws SQLException {
    return header(c, conversationId, userId, " FOR UPDATE");
  }

  /** The conversation's header, if the user is in it. */
  private static Header readHeader(Connection c, Uuid conversationId, Uuid userId)
      throws SQLException {
    return header(c, conversationId, userId, "");
  }

  private static Header header(Connection c, Uuid conversationId, Uuid userId, String locking)
      throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT user_a, user_b, last_seq FROM conversation WHERE conversation_id = ?"
                    + locking,
                conversationId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw ApiError.notFound("conversation " + conversationId + " does not exist");
      }
      Header h = new Header(Uuid.of(rs.getBytes(1)), Uuid.of(rs.getBytes(2)), rs.getLong(3));
      if (!h.includes(userId)) {
        throw ApiError.permissionDenied(
            "user " + userId + " is not in conversation " + conversationId);
      }
      return h;
    }
  }

  /**
   * Stores a message, assigning the next sequence number. Resending with the same client message id
   * returns the message stored the first time.
   */
  public Message send(Uuid userId, Uuid conversationId, String clientMessageId, String text)
      throws SQLException {
    if (clientMessageId.isEmpty()
        || clientMessageId.getBytes(StandardCharsets.UTF_8).length > MAX_CLIENT_MESSAGE_ID) {
      throw ApiError.invalidArgument(
          "client_message_id must be 1 to " + MAX_CLIENT_MESSAGE_ID + " bytes");
    }
    if (text.isEmpty() || text.codePointCount(0, text.length()) > MAX_TEXT_LENGTH) {
      throw ApiError.invalidArgument("text must be 1 to " + MAX_TEXT_LENGTH + " characters");
    }
    try (Connection c = db.getConnection()) {
      c.setAutoCommit(false);
      try {
        Header h = lockHeader(c, conversationId, userId);
        Message stored = findSent(c, userId, conversationId, clientMessageId);
        if (stored != null) {
          c.rollback();
          return stored;
        }
        LocalDateTime sentAt = Timestamps.now();
        long seq = h.lastSeq() + 1;
        Message msg =
            Message.newBuilder()
                .setConversationId(conversationId.toByteString())
                .setSeq(seq)
                .setSenderUserId(userId.toByteString())
                .setClientMessageId(clientMessageId)
                .setText(text)
                .setSentAt(Timestamps.toProto(sentAt))
                .build();
        Sql.update(
            c,
            "UPDATE conversation SET last_seq = ? WHERE conversation_id = ?",
            seq,
            conversationId);
        Sql.update(
            c,
            "INSERT INTO message (conversation_id, seq, sender_user_id, client_message_id, body,"
                + " sent_at) VALUES (?, ?, ?, ?, ?, ?)",
            conversationId,
            seq,
            userId,
            clientMessageId,
            text,
            sentAt);
        Event event =
            Event.newBuilder()
                .setOccurredAt(msg.getSentAt())
                .setMessage(MessageEvent.newBuilder().setMessage(msg))
                .build();
        insertOutbox(c, h, conversationId, "message", event, sentAt);
        c.commit();
        return msg;
      } catch (SQLException | RuntimeException e) {
        c.rollback();
        throw e;
      }
    }
  }

  /** The message the user already sent with this client message id, or null. */
  private static Message findSent(
      Connection c, Uuid userId, Uuid conversationId, String clientMessageId) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT seq, body, sent_at FROM message WHERE conversation_id = ? AND"
                    + " sender_user_id = ? AND client_message_id = ?",
                conversationId,
                userId,
                clientMessageId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        return null;
      }
      return Message.newBuilder()
          .setConversationId(conversationId.toByteString())
          .setSeq(rs.getLong(1))
          .setSenderUserId(userId.toByteString())
          .setClientMessageId(clientMessageId)
          .setText(rs.getString(2))
          .setSentAt(Timestamps.toProto(rs.getObject(3, LocalDateTime.class)))
          .build();
    }
  }

  /** Moves the user's read position forward; backwards or in place does nothing. */
  public void markRead(Uuid userId, Uuid conversationId, long seq) throws SQLException {
    if (seq <= 0) {
      throw ApiError.invalidArgument("seq must be positive");
    }
    try (Connection c = db.getConnection()) {
      c.setAutoCommit(false);
      try {
        Header h = lockHeader(c, conversationId, userId);
        if (seq > h.lastSeq()) {
          throw ApiError.invalidArgument(
              "seq " + seq + " is beyond the last message " + h.lastSeq());
        }
        if (lockReadMark(c, userId, conversationId) >= seq) {
          c.rollback();
          return;
        }
        LocalDateTime at = Timestamps.now();
        Sql.update(
            c,
            "INSERT INTO read_mark (conversation_id, user_id, seq, updated_at) VALUES (?, ?, ?, ?)"
                + " ON DUPLICATE KEY UPDATE seq = ?, updated_at = ?",
            conversationId,
            userId,
            seq,
            at,
            seq,
            at);
        Event event =
            Event.newBuilder()
                .setOccurredAt(Timestamps.toProto(at))
                .setRead(
                    ReadEvent.newBuilder()
                        .setConversationId(conversationId.toByteString())
                        .setUserId(userId.toByteString())
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

  /** The user's read position, locked for the transaction; 0 before the first read mark. */
  private static long lockReadMark(Connection c, Uuid userId, Uuid conversationId)
      throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT seq FROM read_mark WHERE conversation_id = ? AND user_id = ? FOR UPDATE",
                conversationId,
                userId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? rs.getLong(1) : 0;
    }
  }

  /** Messages with seq below beforeSeq, newest first. */
  public List<Message> messages(Uuid userId, Uuid conversationId, long beforeSeq, int limit)
      throws SQLException {
    long before = beforeSeq == 0 ? Long.MAX_VALUE : beforeSeq;
    int pageSize = limit == 0 ? DEFAULT_PAGE_SIZE : Math.min(limit, MAX_PAGE_SIZE);
    try (Connection c = db.getConnection()) {
      readHeader(c, conversationId, userId);
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT seq, sender_user_id, client_message_id, body, sent_at FROM message WHERE"
                      + " conversation_id = ? AND seq < ? ORDER BY seq DESC LIMIT ?",
                  conversationId,
                  before,
                  pageSize);
          ResultSet rs = ps.executeQuery()) {
        List<Message> out = new ArrayList<>();
        while (rs.next()) {
          out.add(
              Message.newBuilder()
                  .setConversationId(conversationId.toByteString())
                  .setSeq(rs.getLong(1))
                  .setSenderUserId(ByteString.copyFrom(rs.getBytes(2)))
                  .setClientMessageId(rs.getString(3))
                  .setText(rs.getString(4))
                  .setSentAt(Timestamps.toProto(rs.getObject(5, LocalDateTime.class)))
                  .build());
        }
        return out;
      }
    }
  }

  private static void insertOutbox(
      Connection c, Header h, Uuid conversationId, String kind, Event event, LocalDateTime at)
      throws SQLException {
    long outboxId = Ids.random();
    Sql.update(
        c,
        "INSERT INTO outbox (conversation_id, outbox_id, user_a, user_b, kind, payload,"
            + " created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
        conversationId,
        outboxId,
        h.userA(),
        h.userB(),
        kind,
        event.toByteArray(),
        at);
    Sql.update(
        c,
        "DELETE FROM outbox WHERE conversation_id = ? AND outbox_id = ?",
        conversationId,
        outboxId);
  }
}
