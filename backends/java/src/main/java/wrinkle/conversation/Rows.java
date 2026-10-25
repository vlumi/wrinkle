package wrinkle.conversation;

import com.google.protobuf.ByteString;
import java.security.SecureRandom;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.util.Collection;
import java.util.HexFormat;
import java.util.List;
import wrinkle.ApiError;
import wrinkle.Ids;
import wrinkle.Kinds;
import wrinkle.MessageBody;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.v1.Event;
import wrinkle.v1.Message;
import wrinkle.v1.MessageEvent;

/** The conversation keyspace's rows as the conversation and group services share them. */
final class Rows {
  private Rows() {}

  static final int MAX_MEMBERS = 256;

  private static final SecureRandom RANDOM = new SecureRandom();

  /**
   * The conversation row: its kind, topic, current members (sorted bytewise), last seq, current key
   * epoch, whether the membership changed since that epoch, and the seq up to which the history is
   * in the cold tier.
   */
  record Header(
      String kind,
      String topic,
      List<Uuid> members,
      long lastSeq,
      long keyEpoch,
      boolean rotate,
      long coldSeq) {
    boolean includes(Uuid userId) {
      return members.contains(userId);
    }

    boolean group() {
      return Kinds.GROUP.equals(kind);
    }
  }

  /** The conversation's header, locked for the transaction when locking; NOT_FOUND if missing. */
  static Header header(Connection c, Uuid conversationId, boolean locking) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT kind, topic, members, last_seq, key_epoch, rotate, cold_seq FROM conversation"
                    + " WHERE conversation_id = ?"
                    + (locking ? " FOR UPDATE" : ""),
                conversationId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw ApiError.notFound("conversation " + conversationId + " does not exist");
      }
      return new Header(
          rs.getString(1),
          rs.getString(2),
          Uuid.split(rs.getBytes(3)),
          rs.getLong(4),
          rs.getLong(5),
          rs.getBoolean(6),
          rs.getLong(7));
    }
  }

  /** The header, if the user is a current member; PERMISSION_DENIED otherwise. */
  static Header memberHeader(Connection c, Uuid conversationId, Uuid userId, boolean locking)
      throws SQLException {
    Header h = header(c, conversationId, locking);
    requireMember(h, conversationId, userId);
    return h;
  }

  static void requireMember(Header h, Uuid conversationId, Uuid userId) {
    if (!h.includes(userId)) {
      throw ApiError.permissionDenied(
          "user " + userId + " is not in conversation " + conversationId);
    }
  }

  /**
   * After a membership change the current key epoch must not be used again: sends wait for the next
   * one. Before the first epoch there is nothing to replace.
   */
  static void requireRotation(Connection c, Uuid conversationId) throws SQLException {
    Sql.update(
        c,
        "UPDATE conversation SET rotate = TRUE WHERE conversation_id = ? AND key_epoch > 0",
        conversationId);
  }

  /** A client message id for a notice, chosen by the server. */
  static String noticeClientId() {
    byte[] b = new byte[8];
    RANDOM.nextBytes(b);
    return "notice-" + HexFormat.of().formatHex(b);
  }

  /**
   * Stores the message and its outbox row: the event goes to the recipients, with the conversation
   * as it is after the change for their lists. The sender's read position moves to the message,
   * without a read event.
   */
  static void storeMessage(
      Connection c,
      Message msg,
      Collection<Uuid> recipients,
      String kind,
      String topic,
      int memberCount)
      throws SQLException {
    long outboxId = Ids.random();
    LocalDateTime sentAt = Timestamps.fromProto(msg.getSentAt());
    Sql.update(
        c,
        "INSERT INTO message (conversation_id, seq, sender_user_id, client_message_id, body,"
            + " sent_at, outbox_id, blob_ids) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
        msg.getConversationId().toByteArray(),
        msg.getSeq(),
        msg.getSenderUserId().toByteArray(),
        msg.getClientMessageId(),
        MessageBody.encode(msg),
        sentAt,
        outboxId,
        ByteString.copyFrom(msg.getBlobIdsList()).toByteArray());
    // The sender has read what they sent; their own list learns it from the message itself.
    Sql.update(
        c,
        "INSERT INTO read_mark (conversation_id, user_id, seq, updated_at) VALUES (?, ?, ?, ?)"
            + " ON DUPLICATE KEY UPDATE seq = GREATEST(seq, ?), updated_at = ?",
        msg.getConversationId().toByteArray(),
        msg.getSenderUserId().toByteArray(),
        msg.getSeq(),
        sentAt,
        msg.getSeq(),
        sentAt);
    Event event =
        Event.newBuilder()
            .setOccurredAt(msg.getSentAt())
            .setMessage(MessageEvent.newBuilder().setMessage(msg))
            .build();
    insertOutbox(
        c,
        Uuid.of(msg.getConversationId().toByteArray()),
        outboxId,
        recipients,
        kind,
        topic,
        memberCount,
        "message",
        event,
        sentAt);
  }

  /** Inserts the outbox row and deletes it again; the change data capture picks up the insert. */
  static void insertOutbox(
      Connection c,
      Uuid conversationId,
      long outboxId,
      Collection<Uuid> recipients,
      String conversationKind,
      String topic,
      int memberCount,
      String kind,
      Event event,
      LocalDateTime at)
      throws SQLException {
    insertOutbox(
        c,
        conversationId,
        outboxId,
        recipients,
        conversationKind,
        topic,
        memberCount,
        kind,
        event,
        at,
        null);
  }

  /** As above, naming the outbox row of the message a message_deleted row is about. */
  static void insertOutbox(
      Connection c,
      Uuid conversationId,
      long outboxId,
      Collection<Uuid> recipients,
      String conversationKind,
      String topic,
      int memberCount,
      String kind,
      Event event,
      LocalDateTime at,
      Long targetOutboxId)
      throws SQLException {
    Sql.update(
        c,
        "INSERT INTO outbox (conversation_id, outbox_id, recipients, conversation_kind, topic,"
            + " member_count, kind, target_outbox_id, payload, created_at) VALUES (?, ?, ?, ?, ?,"
            + " ?, ?, ?, ?, ?)",
        conversationId,
        outboxId,
        Uuid.join(recipients),
        conversationKind,
        topic,
        memberCount,
        kind,
        targetOutboxId,
        event.toByteArray(),
        at);
    Sql.update(
        c,
        "DELETE FROM outbox WHERE conversation_id = ? AND outbox_id = ?",
        conversationId,
        outboxId);
  }

  /** A message row's columns from seq on, in the order selectMessage reads them. */
  static final String MESSAGE_COLUMNS =
      "seq, sender_user_id, client_message_id, body, sent_at, blob_ids";

  static final int MESSAGE_COLUMN_COUNT = 6;

  static Message message(Uuid conversationId, ResultSet rs) throws SQLException {
    Message.Builder m =
        Message.newBuilder()
            .setConversationId(conversationId.toByteString())
            .setSeq(rs.getLong(1))
            .setSenderUserId(Uuid.of(rs.getBytes(2)).toByteString())
            .setClientMessageId(rs.getString(3))
            .setSentAt(Timestamps.toProto(rs.getObject(5, LocalDateTime.class)));
    MessageBody.decode(m, rs.getBytes(4)).clearBlobIds();
    for (Uuid id : Uuid.split(rs.getBytes(6))) {
      m.addBlobIds(id.toByteString());
    }
    return m.build();
  }
}
