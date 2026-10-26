package wrinkle.conversation;

import com.google.protobuf.ByteString;
import com.google.protobuf.InvalidProtocolBufferException;
import java.nio.charset.StandardCharsets;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collection;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Ids;
import wrinkle.Kinds;
import wrinkle.MessageBody;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.contacts.Contacts;
import wrinkle.conversation.Rows.Header;
import wrinkle.uploads.UploadService;
import wrinkle.v1.Event;
import wrinkle.v1.Message;
import wrinkle.v1.MessageDeletedEvent;
import wrinkle.v1.Reaction;
import wrinkle.v1.ReactionEvent;
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
  private static final int MAX_REACTION_CIPHERTEXT_BYTES = 1024;
  private static final int DEFAULT_PAGE_SIZE = 50;
  private static final int MAX_PAGE_SIZE = 500;
  // How many of the newest messages a read covers are searched for senders to tell.
  private static final int READ_SENDERS_WINDOW = 1000;
  private static final int MAX_ATTACHMENTS = 10;
  // Bounds the cache of conversations' kinds; it starts over when full.
  private static final int MAX_KINDS = 1 << 20;

  private final DataSource db;
  private final UploadService uploads;
  private final Contacts contacts;
  private final Cold cold;
  // A direct conversation's two members, or no one for a group: neither ever changes.
  private final Map<Uuid, List<Uuid>> directMembers = new ConcurrentHashMap<>();

  public ConversationService(DataSource db, UploadService uploads, Contacts contacts, Cold cold) {
    this.db = db;
    this.uploads = uploads;
    this.contacts = contacts;
    this.cold = cold;
  }

  /**
   * In a direct conversation the user is a member of, refuses the user when either participant
   * blocked the other. A group, or a user who is not a member, is left to the transaction's checks.
   */
  void checkBlocks(Uuid userId, Uuid conversationId) throws SQLException {
    List<Uuid> members = directMembers.get(conversationId);
    if (members == null) {
      try (Connection c = db.getConnection()) {
        Header h = Rows.header(c, conversationId, false);
        members = h.group() ? List.of() : h.members();
      }
      if (directMembers.size() >= MAX_KINDS) {
        directMembers.clear();
      }
      directMembers.put(conversationId, members);
    }
    if (members.size() != 2 || !members.contains(userId)) {
      return;
    }
    contacts.checkMessaging(
        userId, members.get(0).equals(userId) ? members.get(1) : members.get(0));
  }

  /**
   * Finds or creates the direct conversation between two users, userA bytewise before userB. The
   * pair row, on userA's shard, decides between two opening at once; a pair whose conversation row
   * is missing gets it here.
   */
  public Uuid open(Uuid userA, Uuid userB) throws SQLException {
    return open(Kinds.DIRECT, userA, userB);
  }

  /**
   * Finds or creates the user's notes: the pair of the user with themselves, decided by the pair
   * row as a direct conversation is.
   */
  public Uuid openNotes(Uuid userId) throws SQLException {
    return open(Kinds.NOTES, userId, userId);
  }

  private Uuid open(String kind, Uuid userA, Uuid userB) throws SQLException {
    List<Uuid> members = userA.equals(userB) ? List.of(userA) : List.of(userA, userB);
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
          kind,
          Uuid.join(members),
          now);
      for (Uuid member : members) {
        Sql.update(
            c,
            "INSERT IGNORE INTO conversation_member (conversation_id, user_id, admin, joined_seq,"
                + " left_seq, joined_at) VALUES (?, ?, FALSE, 1, NULL, ?)",
            conversationId,
            member,
            now);
      }
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
   * epoch, and not while the conversation waits for a new one. The attachments must be the sender's
   * uploads, already uploaded; they become used, and unused again should the send fail.
   */
  public Message send(
      Uuid userId,
      Uuid conversationId,
      String clientMessageId,
      Sealed sealed,
      List<ByteString> blobIdBytes)
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
    List<Uuid> blobIds = blobIds(blobIdBytes);
    checkBlocks(userId, conversationId);
    if (!blobIds.isEmpty()) {
      // A resend finds its message before the uploads are checked again; after a delete they may
      // be gone.
      try (Connection c = db.getConnection()) {
        Rows.memberHeader(c, conversationId, userId, false);
        Message stored = findSent(c, userId, conversationId, clientMessageId);
        if (stored != null) {
          return stored;
        }
      }
      uploads.useAttachments(userId, conversationId, clientMessageId, blobIds);
    }
    try {
      return store(userId, conversationId, clientMessageId, sealed, blobIdBytes);
    } catch (SQLException | RuntimeException e) {
      // Theirs again, for a retry or the sweeper.
      if (!blobIds.isEmpty()) {
        try {
          uploads.releaseAttachments(userId, blobIds);
        } catch (SQLException r) {
          e.addSuppressed(r);
        }
      }
      throw e;
    }
  }

  /** Stores the message under the conversation row's lock, or returns the one already stored. */
  private Message store(
      Uuid userId,
      Uuid conversationId,
      String clientMessageId,
      Sealed sealed,
      List<ByteString> blobIdBytes)
      throws SQLException {
    return Sql.inTransaction(
        db,
        c -> {
          Header h = Rows.memberHeader(c, conversationId, userId, true);
          Message stored = findSent(c, userId, conversationId, clientMessageId);
          if (stored != null) {
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
                  .addAllBlobIds(blobIdBytes)
                  .build();
          Sql.update(
              c,
              "UPDATE conversation SET last_seq = ? WHERE conversation_id = ?",
              seq,
              conversationId);
          Rows.storeMessage(c, msg, h.members(), h.kind(), h.topic(), h.members().size());
          return msg;
        });
  }

  /** The attachment ids: at most MAX_ATTACHMENTS, 16 bytes each, distinct. */
  private static List<Uuid> blobIds(List<ByteString> bytes) {
    if (bytes.size() > MAX_ATTACHMENTS) {
      throw ApiError.invalidArgument("at most " + MAX_ATTACHMENTS + " blob_ids");
    }
    List<Uuid> ids = new ArrayList<>(bytes.size());
    Set<Uuid> seen = new HashSet<>();
    for (ByteString b : bytes) {
      Uuid id = Uuid.parse(b);
      if (id == null) {
        throw ApiError.invalidArgument("blob_ids must be 16 bytes each");
      }
      if (!seen.add(id)) {
        throw ApiError.invalidArgument("blob_ids must be distinct");
      }
      ids.add(id);
    }
    return ids;
  }

  /**
   * Deletes one of the user's own messages for everyone: its content gives way to deleted_at, and
   * the members, with the user even after leaving, learn of it. Deleting again does nothing. The
   * outbox row names the attachments' uploads, which the fan-out releases for the sweeper. A
   * message in the cold tier is changed there, under the conversation's row lock as in MySQL.
   */
  public void deleteMessage(Uuid userId, Uuid conversationId, long seq) throws SQLException {
    if (seq <= 0) {
      throw ApiError.invalidArgument("seq must be positive");
    }
    cold.inTransaction(
        db,
        (c, changes) -> {
          Header h = Rows.header(c, conversationId, true);
          Readable r = readable(c, conversationId, userId);
          if (!r.covers(seq)) {
            throw ApiError.notFound("message " + seq + " does not exist");
          }
          Message msg;
          long targetOutboxId;
          Cold.Row coldRow = null;
          if (seq <= h.coldSeq()) {
            coldRow = cold.get(conversationId, seq);
            if (coldRow == null) {
              throw ApiError.notFound("message " + seq + " does not exist");
            }
            msg = coldRow.message(conversationId);
            targetOutboxId = coldRow.outboxId();
          } else {
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
              if (!rs.next()) {
                throw ApiError.notFound("message " + seq + " does not exist");
              }
              msg = Rows.message(conversationId, rs);
              targetOutboxId = rs.getLong(Rows.MESSAGE_COLUMN_COUNT + 1);
            }
          }
          if (msg.hasNotice()) {
            throw ApiError.invalidArgument("a notice cannot be deleted");
          }
          if (!userId.matches(msg.getSenderUserId())) {
            throw ApiError.permissionDenied("message " + seq + " was sent by someone else");
          }
          if (msg.hasDeletedAt()) {
            return null;
          }
          LocalDateTime at = Timestamps.now();
          Message deleted = Message.newBuilder().setDeletedAt(Timestamps.toProto(at)).build();
          byte[] body = MessageBody.encode(deleted);
          // The deleted event tells the members to drop the reactions too.
          if (coldRow != null) {
            changes.change(
                conversationId, coldRow, coldRow.withBody(body, new byte[0], new byte[0]));
          } else {
            Sql.update(
                c,
                "UPDATE message SET body = ?, blob_ids = '' WHERE conversation_id = ? AND seq = ?",
                body,
                conversationId,
                seq);
            Sql.update(
                c,
                "DELETE FROM reaction WHERE conversation_id = ? AND seq = ?",
                conversationId,
                seq);
          }
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
          byte[] releasedUploads =
              msg.getBlobIdsCount() == 0
                  ? null
                  : userId
                      .toByteString()
                      .concat(ByteString.copyFrom(msg.getBlobIdsList()))
                      .toByteArray();
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
              targetOutboxId,
              releasedUploads);
          return null;
        });
  }

  /**
   * Sets the user's sealed reactions to a message, replacing earlier ones; without sealed, removes
   * them. Only current members may react, with the current key epoch, and not to notices or deleted
   * messages. A change reaches the current members; no change sends nothing. A message in the cold
   * tier is changed there, under the conversation's row lock as in MySQL.
   */
  public void setReaction(Uuid userId, Uuid conversationId, long seq, Sealed sealed)
      throws SQLException {
    if (seq <= 0) {
      throw ApiError.invalidArgument("seq must be positive");
    }
    if (sealed != null) {
      if (sealed.getNonce().size() != NONCE_BYTES) {
        throw ApiError.invalidArgument("sealed.nonce must be " + NONCE_BYTES + " bytes");
      }
      int ciphertext = sealed.getCiphertext().size();
      if (ciphertext == 0 || ciphertext > MAX_REACTION_CIPHERTEXT_BYTES) {
        throw ApiError.invalidArgument(
            "sealed.ciphertext must be 1 to " + MAX_REACTION_CIPHERTEXT_BYTES + " bytes");
      }
    }
    checkBlocks(userId, conversationId);
    cold.inTransaction(
        db,
        (c, changes) -> {
          Header h = Rows.memberHeader(c, conversationId, userId, true);
          Readable r = readable(c, conversationId, userId);
          if (!r.covers(seq)) {
            throw ApiError.notFound("message " + seq + " does not exist");
          }
          Message msg;
          Cold.Row coldRow = null;
          if (seq <= h.coldSeq()) {
            coldRow = cold.get(conversationId, seq);
            if (coldRow == null) {
              throw ApiError.notFound("message " + seq + " does not exist");
            }
            msg = coldRow.message(conversationId);
          } else {
            try (PreparedStatement ps =
                    Sql.prepare(
                        c,
                        "SELECT "
                            + Rows.MESSAGE_COLUMNS
                            + " FROM message WHERE conversation_id = ? AND seq = ?",
                        conversationId,
                        seq);
                ResultSet rs = ps.executeQuery()) {
              if (!rs.next()) {
                throw ApiError.notFound("message " + seq + " does not exist");
              }
              msg = Rows.message(conversationId, rs);
            }
          }
          if (msg.hasNotice()) {
            throw ApiError.invalidArgument("a notice cannot be reacted to");
          }
          if (msg.hasDeletedAt()) {
            throw ApiError.failedPrecondition("message " + seq + " was deleted");
          }
          if (sealed != null) {
            if (h.keyEpoch() == 0 || Integer.toUnsignedLong(sealed.getEpoch()) != h.keyEpoch()) {
              throw ApiError.failedPrecondition("stale key epoch");
            }
            if (h.rotate()) {
              throw ApiError.failedPrecondition("keys must rotate");
            }
          }
          byte[] previous = null;
          List<Reaction> others = new ArrayList<>();
          if (coldRow != null) {
            for (Reaction reaction : msg.getReactionsList()) {
              if (userId.matches(reaction.getUserId())) {
                previous = reaction.getSealed().toByteArray();
              } else {
                others.add(reaction);
              }
            }
          } else {
            try (PreparedStatement ps =
                    Sql.prepare(
                        c,
                        "SELECT body FROM reaction WHERE conversation_id = ? AND seq = ? AND user_id"
                            + " = ? FOR UPDATE",
                        conversationId,
                        seq,
                        userId);
                ResultSet rs = ps.executeQuery()) {
              previous = rs.next() ? rs.getBytes(1) : null;
            }
          }
          byte[] body = sealed == null ? null : sealed.toByteArray();
          if (Arrays.equals(previous, body)) {
            return null;
          }
          LocalDateTime at = Timestamps.now();
          if (coldRow != null) {
            if (sealed != null) {
              others.add(
                  Reaction.newBuilder()
                      .setUserId(userId.toByteString())
                      .setSealed(sealed)
                      .setUpdatedAt(Timestamps.toProto(at))
                      .build());
            }
            changes.change(
                conversationId, coldRow, coldRow.withReactions(Cold.encodeReactions(others)));
          } else if (body == null) {
            Sql.update(
                c,
                "DELETE FROM reaction WHERE conversation_id = ? AND seq = ? AND user_id = ?",
                conversationId,
                seq,
                userId);
          } else {
            Sql.update(
                c,
                "INSERT INTO reaction (conversation_id, seq, user_id, body, updated_at) VALUES (?, ?,"
                    + " ?, ?, ?) ON DUPLICATE KEY UPDATE body = ?, updated_at = ?",
                conversationId,
                seq,
                userId,
                body,
                at,
                body,
                at);
          }
          ReactionEvent.Builder reaction =
              ReactionEvent.newBuilder()
                  .setConversationId(conversationId.toByteString())
                  .setSeq(seq)
                  .setUserId(userId.toByteString())
                  .setUpdatedAt(Timestamps.toProto(at));
          if (sealed != null) {
            reaction.setSealed(sealed);
          }
          Event event =
              Event.newBuilder()
                  .setOccurredAt(Timestamps.toProto(at))
                  .setReaction(reaction)
                  .build();
          Rows.insertOutbox(
              c,
              conversationId,
              Ids.random(),
              h.members(),
              h.kind(),
              h.topic(),
              h.members().size(),
              "reaction",
              event,
              at);
          return null;
        });
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
    // Across a block, either way, a read tells only the reader's devices.
    boolean blocked = false;
    try {
      checkBlocks(userId, conversationId);
    } catch (ApiError e) {
      blocked = true;
    }
    boolean readerOnly = blocked;
    Sql.inTransaction(
        db,
        c -> {
          Header h = Rows.memberHeader(c, conversationId, userId, true);
          if (seq > h.lastSeq()) {
            throw ApiError.invalidArgument(
                "seq " + seq + " is beyond the last message " + h.lastSeq());
          }
          long previous = lockReadMark(c, userId, conversationId);
          if (previous >= seq) {
            return null;
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
          Collection<Uuid> recipients;
          if (h.group()) {
            recipients = readRecipients(c, h, userId, conversationId, previous, seq);
          } else if (readerOnly) {
            recipients = List.of(userId);
          } else {
            recipients = h.members();
          }
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
          return null;
        });
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
   * read: from the notice that added them on, and up to the one that removed them. The newer ones
   * are in MySQL; the page goes on in the cold tier below the watermark.
   */
  public List<Message> messages(Uuid userId, Uuid conversationId, long beforeSeq, int limit)
      throws SQLException {
    long before = beforeSeq == 0 ? Long.MAX_VALUE : beforeSeq;
    int pageSize = limit == 0 ? DEFAULT_PAGE_SIZE : Math.min(limit, MAX_PAGE_SIZE);
    try (Connection c = db.getConnection()) {
      Rows.header(c, conversationId, false);
      Readable r = readable(c, conversationId, userId);
      while (true) {
        List<Message> page = messages(c, conversationId, r, before, pageSize);
        if (page != null) {
          return page;
        }
      }
    }
  }

  /**
   * Where a page that MySQL gave goes on in the cold tier: from joinedSeq up to hi, when there is
   * room and anything there. A page with a message the watermark has since passed is read again.
   */
  record ColdRange(long hi, boolean more, boolean again) {}

  static ColdRange coldRange(
      List<Message> page, long joinedSeq, long upTo, long beforeSeq, long coldSeq, int limit) {
    long hi = Math.min(Math.min(upTo, beforeSeq - 1), coldSeq);
    if (!page.isEmpty()) {
      long lowest = page.getLast().getSeq();
      if (lowest <= coldSeq) {
        return new ColdRange(0, false, true);
      }
      hi = Math.min(hi, lowest - 1);
    }
    return new ColdRange(hi, page.size() < limit && hi >= joinedSeq, false);
  }

  /**
   * A page of history: MySQL first, then the watermark, then the cold tier below the lowest seq
   * MySQL gave; null to read again. The watermark only rises, and what it covers is in the cold
   * tier before it does, so nothing falls between the two. A move that overlapped the read may have
   * taken the reactions of what MySQL gave, so the read is made again then.
   */
  private List<Message> messages(
      Connection c, Uuid conversationId, Readable r, long before, int pageSize)
      throws SQLException {
    List<Message> page = new ArrayList<>();
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT "
                    + Rows.MESSAGE_COLUMNS
                    + " FROM message WHERE conversation_id = ? AND seq >= ? AND seq <= ? AND seq"
                    + " < ? ORDER BY seq DESC LIMIT ?",
                conversationId,
                r.from(),
                r.to(),
                before,
                pageSize);
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        page.add(Rows.message(conversationId, rs));
      }
    }
    List<Message> out = new ArrayList<>(page.size());
    if (!page.isEmpty()) {
      Map<Long, List<Reaction>> reactions =
          reactions(c, conversationId, page.getLast().getSeq(), page.getFirst().getSeq());
      for (Message m : page) {
        List<Reaction> forMessage = reactions.get(m.getSeq());
        out.add(
            forMessage == null || m.hasDeletedAt()
                ? m
                : m.toBuilder().addAllReactions(forMessage).build());
      }
    }
    long coldSeq;
    try (PreparedStatement ps =
            Sql.prepare(
                c, "SELECT cold_seq FROM conversation WHERE conversation_id = ?", conversationId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw ApiError.notFound("conversation " + conversationId + " does not exist");
      }
      coldSeq = rs.getLong(1);
    }
    ColdRange range = coldRange(out, r.from(), r.to(), before, coldSeq, pageSize);
    if (range.again()) {
      return null;
    }
    if (range.more()) {
      for (Cold.Row row : cold.scan(conversationId, r.from(), range.hi(), pageSize - out.size())) {
        out.add(row.message(conversationId));
      }
    }
    return out;
  }

  /** The reactions to the messages from seq from to seq to, by seq, oldest first. */
  private static Map<Long, List<Reaction>> reactions(
      Connection c, Uuid conversationId, long from, long to) throws SQLException {
    Map<Long, List<Reaction>> out = new HashMap<>();
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT seq, user_id, body, updated_at FROM reaction WHERE conversation_id = ? AND"
                    + " seq >= ? AND seq <= ? ORDER BY updated_at, user_id",
                conversationId,
                from,
                to);
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        Reaction reaction =
            Reaction.newBuilder()
                .setUserId(Uuid.of(rs.getBytes(2)).toByteString())
                .setSealed(sealedBody(rs.getBytes(3)))
                .setUpdatedAt(Timestamps.toProto(rs.getObject(4, LocalDateTime.class)))
                .build();
        out.computeIfAbsent(rs.getLong(1), k -> new ArrayList<>()).add(reaction);
      }
    }
    return out;
  }

  private static Sealed sealedBody(byte[] body) {
    try {
      return Sealed.parseFrom(body);
    } catch (InvalidProtocolBufferException e) {
      throw new IllegalStateException("corrupt reaction body", e);
    }
  }

  /**
   * The part of the sequence a user may read: from the notice that added them to the one removing
   * them.
   */
  private record Readable(long from, long to) {
    boolean covers(long seq) {
      return seq >= from && seq <= to;
    }
  }

  /** The user's readable range; PERMISSION_DENIED if they were never a member. */
  private static Readable readable(Connection c, Uuid conversationId, Uuid userId)
      throws SQLException {
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
      long from = rs.getLong(1);
      long left = rs.getLong(2);
      return new Readable(from, rs.wasNull() ? Long.MAX_VALUE : left);
    }
  }
}
