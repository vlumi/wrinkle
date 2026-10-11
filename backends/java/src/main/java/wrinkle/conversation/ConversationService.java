package wrinkle.conversation;

import java.nio.charset.StandardCharsets;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.Collection;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Ids;
import wrinkle.MessageBody;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.conversation.Rows.Header;
import wrinkle.v1.Event;
import wrinkle.v1.Message;
import wrinkle.v1.MessageDeletedEvent;
import wrinkle.v1.ReadEvent;
import wrinkle.v1.Sealed;

/**
 * The conversation service: direct conversations, sending, read marks and history, against the
 * conversation keyspace. Every change is stored with an outbox row in the same transaction, which
 * also deletes it: the change data capture picks up the insert, and the table stays empty.
 */
public final class ConversationService {
  private static final int MAX_CLIENT_MESSAGE_ID = 64;
  private static final int NONCE_BYTES = 12;
  private static final int MAX_CIPHERTEXT_BYTES = 49152;
  private static final int DEFAULT_PAGE_SIZE = 50;
  private static final int MAX_PAGE_SIZE = 500;
  // How many of the newest messages a read covers are searched for senders to tell.
  private static final int READ_SENDERS_WINDOW = 1000;

  private final DataSource db;

  public ConversationService(DataSource db) {
    this.db = db;
  }

  /**
   * Finds or creates the direct conversation between two users, userA bytewise before userB. The
   * pair row, on userA's shard, decides between two opening at once; a pair whose conversation row
   * is missing gets it here.
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
      LocalDateTime now = Timestamps.now();
      Sql.update(
          c,
          "INSERT IGNORE INTO conversation (conversation_id, kind, topic, members, last_seq,"
              + " created_at) VALUES (?, ?, '', ?, 0, ?)",
          conversationId,
          Rows.DIRECT,
          Uuid.join(List.of(userA, userB)),
          now);
      Sql.update(
          c,
          "INSERT IGNORE INTO conversation_member (conversation_id, user_id, admin, joined_seq,"
              + " left_seq, joined_at) VALUES (?, ?, FALSE, 1, NULL, ?), (?, ?, FALSE, 1, NULL, ?)",
          conversationId,
          userA,
          now,
          conversationId,
          userB,
          now);
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

  /**
   * Stores a message, assigning the next sequence number. Resending with the same client message id
   * returns the message stored the first time. The content must be sealed with the current key
   * epoch, and not while the conversation waits for a new one.
   */
  public Message send(Uuid userId, Uuid conversationId, String clientMessageId, Sealed sealed)
      throws SQLException {
    if (clientMessageId.isEmpty()
        || clientMessageId.getBytes(StandardCharsets.UTF_8).length > MAX_CLIENT_MESSAGE_ID) {
      throw ApiError.invalidArgument(
          "client_message_id must be 1 to " + MAX_CLIENT_MESSAGE_ID + " bytes");
    }
    if (sealed.getNonce().size() != NONCE_BYTES) {
      throw ApiError.invalidArgument("sealed.nonce must be " + NONCE_BYTES + " bytes");
    }
    int ciphertext = sealed.getCiphertext().size();
    if (ciphertext == 0 || ciphertext > MAX_CIPHERTEXT_BYTES) {
      throw ApiError.invalidArgument(
          "sealed.ciphertext must be 1 to " + MAX_CIPHERTEXT_BYTES + " bytes");
    }
    try (Connection c = db.getConnection()) {
      c.setAutoCommit(false);
      try {
        Header h = Rows.memberHeader(c, conversationId, userId, true);
        Message stored = findSent(c, userId, conversationId, clientMessageId);
        if (stored != null) {
          c.rollback();
          return stored;
        }
        if (h.keyEpoch() == 0 || Integer.toUnsignedLong(sealed.getEpoch()) != h.keyEpoch()) {
          throw ApiError.failedPrecondition("stale key epoch");
        }
        if (h.rotate()) {
          throw ApiError.failedPrecondition("keys must rotate");
        }
        LocalDateTime sentAt = Timestamps.now();
        long seq = h.lastSeq() + 1;
        Message msg =
            Message.newBuilder()
                .setConversationId(conversationId.toByteString())
                .setSeq(seq)
                .setSenderUserId(userId.toByteString())
                .setClientMessageId(clientMessageId)
                .setSealed(sealed)
                .setSentAt(Timestamps.toProto(sentAt))
                .build();
        Sql.update(
            c,
            "UPDATE conversation SET last_seq = ? WHERE conversation_id = ?",
            seq,
            conversationId);
        Rows.storeMessage(c, msg, h.members(), h.kind(), h.topic(), h.members().size());
        c.commit();
        return msg;
      } catch (SQLException | RuntimeException e) {
        c.rollback();
        throw e;
      }
    }
  }

  /**
   * Deletes one of the user's own messages for everyone: its content gives way to deleted_at, and
   * the members, with the user even after leaving, learn of it. Deleting again does nothing.
   */
  public void deleteMessage(Uuid userId, Uuid conversationId, long seq) throws SQLException {
    if (seq <= 0) {
      throw ApiError.invalidArgument("seq must be positive");
    }
    try (Connection c = db.getConnection()) {
      c.setAutoCommit(false);
      try {
        Header h = Rows.header(c, conversationId, true);
        long from;
        long to;
        try (PreparedStatement ps =
                Sql.prepare(
                    c,
                    "SELECT joined_seq, left_seq FROM conversation_member WHERE conversation_id = ?"
                        + " AND user_id = ?",
                    conversationId,
                    userId);
            ResultSet rs = ps.executeQuery()) {
          if (!rs.next()) {
            throw ApiError.permissionDenied(
                "user " + userId + " is not in conversation " + conversationId);
          }
          from = rs.getLong(1);
          long left = rs.getLong(2);
          to = rs.wasNull() ? Long.MAX_VALUE : left;
        }
        Message msg;
        long targetOutboxId;
        try (PreparedStatement ps =
                Sql.prepare(
                    c,
                    "SELECT "
                        + Rows.MESSAGE_COLUMNS
                        + ", outbox_id FROM message WHERE conversation_id = ? AND seq = ? FOR"
                        + " UPDATE",
                    conversationId,
                    seq);
            ResultSet rs = ps.executeQuery()) {
          if (!rs.next() || seq < from || seq > to) {
            throw ApiError.notFound("message " + seq + " does not exist");
          }
          msg = Rows.message(conversationId, rs);
          targetOutboxId = rs.getLong(6);
        }
        if (msg.hasNotice()) {
          throw ApiError.invalidArgument("a notice cannot be deleted");
        }
        if (!userId.matches(msg.getSenderUserId())) {
          throw ApiError.permissionDenied("message " + seq + " was sent by someone else");
        }
        if (msg.hasDeletedAt()) {
          c.commit();
          return;
        }
        LocalDateTime at = Timestamps.now();
        Message deleted = Message.newBuilder().setDeletedAt(Timestamps.toProto(at)).build();
        Sql.update(
            c,
            "UPDATE message SET body = ? WHERE conversation_id = ? AND seq = ?",
            MessageBody.encode(deleted),
            conversationId,
            seq);
        Event event =
            Event.newBuilder()
                .setOccurredAt(deleted.getDeletedAt())
                .setMessageDeleted(
                    MessageDeletedEvent.newBuilder()
                        .setConversationId(conversationId.toByteString())
                        .setSeq(seq)
                        .setDeletedAt(deleted.getDeletedAt()))
                .build();
        List<Uuid> recipients = new ArrayList<>(h.members());
        if (!h.includes(userId)) {
          recipients.add(userId);
        }
        Rows.insertOutbox(
            c,
            conversationId,
            Ids.random(),
            recipients,
            h.kind(),
            h.topic(),
            h.members().size(),
            "message_deleted",
            event,
            at,
            targetOutboxId);
        c.commit();
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
                "SELECT "
                    + Rows.MESSAGE_COLUMNS
                    + " FROM message WHERE conversation_id = ? AND sender_user_id = ? AND"
                    + " client_message_id = ?",
                conversationId,
                userId,
                clientMessageId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? Rows.message(conversationId, rs) : null;
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
        Header h = Rows.memberHeader(c, conversationId, userId, true);
        if (seq > h.lastSeq()) {
          throw ApiError.invalidArgument(
              "seq " + seq + " is beyond the last message " + h.lastSeq());
        }
        long previous = lockReadMark(c, userId, conversationId);
        if (previous >= seq) {
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
        Collection<Uuid> recipients =
            h.group() ? readRecipients(c, h, userId, conversationId, previous, seq) : h.members();
        Rows.insertOutbox(
            c,
            conversationId,
            Ids.random(),
            recipients,
            h.kind(),
            h.topic(),
            h.members().size(),
            "read",
            event,
            at);
        c.commit();
      } catch (SQLException | RuntimeException e) {
        c.rollback();
        throw e;
      }
    }
  }

  /**
   * Who learns of a read in a group: the reader, and the current members who sent any of the newest
   * messages it covers.
   */
  private static Collection<Uuid> readRecipients(
      Connection c, Header h, Uuid userId, Uuid conversationId, long from, long to)
      throws SQLException {
    Set<Uuid> recipients = new LinkedHashSet<>();
    recipients.add(userId);
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT DISTINCT sender_user_id FROM (SELECT sender_user_id FROM message WHERE"
                    + " conversation_id = ? AND seq > ? AND seq <= ? ORDER BY seq DESC LIMIT ?)"
                    + " AS m",
                conversationId,
                from,
                to,
                READ_SENDERS_WINDOW);
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        Uuid sender = Uuid.of(rs.getBytes(1));
        if (h.includes(sender)) {
          recipients.add(sender);
        }
      }
    }
    return recipients;
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

  /**
   * Messages with seq below beforeSeq, newest first, within the part of the sequence the user may
   * read: from the notice that added them on, and up to the one that removed them.
   */
  public List<Message> messages(Uuid userId, Uuid conversationId, long beforeSeq, int limit)
      throws SQLException {
    long before = beforeSeq == 0 ? Long.MAX_VALUE : beforeSeq;
    int pageSize = limit == 0 ? DEFAULT_PAGE_SIZE : Math.min(limit, MAX_PAGE_SIZE);
    try (Connection c = db.getConnection()) {
      Rows.header(c, conversationId, false);
      long from;
      long to;
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT joined_seq, left_seq FROM conversation_member WHERE conversation_id = ?"
                      + " AND user_id = ?",
                  conversationId,
                  userId);
          ResultSet rs = ps.executeQuery()) {
        if (!rs.next()) {
          throw ApiError.permissionDenied(
              "user " + userId + " is not in conversation " + conversationId);
        }
        from = rs.getLong(1);
        long left = rs.getLong(2);
        to = rs.wasNull() ? Long.MAX_VALUE : left;
      }
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT "
                      + Rows.MESSAGE_COLUMNS
                      + " FROM message WHERE conversation_id = ? AND seq >= ? AND seq <= ? AND seq"
                      + " < ? ORDER BY seq DESC LIMIT ?",
                  conversationId,
                  from,
                  to,
                  before,
                  pageSize);
          ResultSet rs = ps.executeQuery()) {
        List<Message> out = new ArrayList<>();
        while (rs.next()) {
          out.add(Rows.message(conversationId, rs));
        }
        return out;
      }
    }
  }
}
