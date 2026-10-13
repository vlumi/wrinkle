package wrinkle.fanout;

import com.google.protobuf.InvalidProtocolBufferException;
import com.google.protobuf.UnsafeByteOperations;
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
import wrinkle.MessageBody;
import wrinkle.Metrics;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.accounts.AccountService;
import wrinkle.backend.v1.Push;
import wrinkle.backend.v1.SignedOut;
import wrinkle.presence.Presence;
import wrinkle.v1.Event;
import wrinkle.v1.MemberLeft;
import wrinkle.v1.Message;
import wrinkle.v1.MessageDeletedEvent;
import wrinkle.v1.Notice;
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
  private final Set<ListKey> listedCache = ConcurrentHashMap.newKeySet();

  UserLog(DataSource db, Presence presence) {
    this.db = db;
    this.presence = presence;
  }

  DataSource db() {
    return db;
  }

  /** Appends a conversation's event to one recipient's log, then pushes it. */
  void deliver(Uuid userId, OutboxRecord o) throws SQLException {
    Uuid peerId = o.peerOf(userId);
    User peer =
        o.event().hasMessage() && peerId != null
            ? peerForNewEntry(userId, o.conversationId(), peerId)
            : null;
    Event event =
        append(
            userId,
            o.conversationId(),
            o.outboxId(),
            o.event(),
            (c, e) -> {
              switch (e.getBodyCase()) {
                case MESSAGE -> recordMessage(c, userId, peerId, peer, o, e);
                case READ -> recordRead(c, userId, o, e.getRead());
                case MESSAGE_DELETED -> recordDeletion(c, userId, o, e.getMessageDeleted());
                default -> {}
              }
            });
    if (event != null && event.hasMessage()) {
      remember(userId, o.conversationId());
    }
  }

  private void remember(Uuid userId, Uuid conversationId) {
    if (listedCache.size() >= MAX_LISTED) {
      listedCache.clear();
    }
    listedCache.add(new ListKey(userId, conversationId));
  }

  /**
   * The peer's current profile when the user's list may not have the conversation yet, for the
   * entry a message creates; null once the entry is known to exist.
   */
  private User peerForNewEntry(Uuid userId, Uuid conversationId, Uuid peerId) throws SQLException {
    if (listedCache.contains(new ListKey(userId, conversationId))) {
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
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT name, icon, avatar, deleted_at IS NOT NULL FROM user WHERE user_id = ?",
                  peerId);
          ResultSet rs = ps.executeQuery()) {
        if (rs.next()) {
          return AccountService.user(
              peerId, rs.getString(1), rs.getString(2), rs.getBytes(3), rs.getBoolean(4));
        }
      }
      return User.newBuilder().setUserId(peerId.toByteString()).build();
    }
  }

  /**
   * Appends the event to the user's log, in one transaction with the list update, then pushes it.
   * Returns the event as stored, or null when there is nothing to push: the user does not exist or
   * was deleted, or the log already has the event from this source row.
   */
  Event append(Uuid userId, Uuid sourceId, long outboxId, Event source, ListUpdate update)
      throws SQLException {
    Stored stored =
        Sql.inTransaction(
            db,
            c -> {
              Head head = lockHead(c, userId);
              if (head.gone()) {
                return null;
              }
              Event event = source.toBuilder().setCursor(head.position() + 1).build();
              byte[] payload = event.toByteArray();
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
                return null;
              }
              Sql.update(
                  c, "UPDATE user SET log_head = ? WHERE user_id = ?", event.getCursor(), userId);
              if (update != null) {
                update.apply(c, event);
              }
              return new Stored(event, payload);
            });
    if (stored == null) {
      return null;
    }
    LocalDateTime occurredAt = Timestamps.fromProto(stored.event().getOccurredAt());
    Metrics.FANOUT_DELIVERIES.inc();
    Metrics.FANOUT_LAG.observe(Duration.between(occurredAt, Timestamps.now()).toNanos() / 1e9);
    presence.publish(
        userId,
        Push.newBuilder().setEvent(UnsafeByteOperations.unsafeWrap(stored.payload())).build());
    return stored.event();
  }

  /** An event as appended, and its bytes as stored, which the push carries. */
  private record Stored(Event event, byte[] payload) {}

  /** Tells the gateways to close the connections of devices signed out. */
  void pushSignedOut(Uuid userId, SignedOut signedOut) {
    presence.publish(userId, Push.newBuilder().setSignedOut(signedOut).build());
  }

  /** The user's log head; gone when there is no such user or the account was deleted. */
  private record Head(long position, boolean gone) {}

  /** The user's log head, locked for the transaction. */
  private static Head lockHead(Connection c, Uuid userId) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c, "SELECT log_head, deleted_at FROM user WHERE user_id = ? FOR UPDATE", userId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        log.warn("no such user {}, dropping event", userId);
        return new Head(0, true);
      }
      return new Head(rs.getLong(1), rs.getObject(2) != null);
    }
  }

  /** How a message changes the user's membership flags; null leaves a flag as it is. */
  record Membership(Boolean member, Boolean admin) {
    static final Membership UNCHANGED = new Membership(null, null);

    /** From the notice the message carries, for the given recipient. */
    static Membership of(Uuid userId, Message msg) {
      if (!msg.hasNotice()) {
        return UNCHANGED;
      }
      boolean sender = userId.matches(msg.getSenderUserId());
      Notice n = msg.getNotice();
      return switch (n.getKindCase()) {
        case GROUP_CREATED -> new Membership(true, sender);
        case MEMBERS_ADDED ->
            n.getMembersAdded().getUsersList().stream().anyMatch(u -> userId.matches(u.getUserId()))
                ? new Membership(true, false)
                : UNCHANGED;
        case MEMBER_REMOVED ->
            userId.matches(n.getMemberRemoved().getUser().getUserId())
                ? new Membership(false, false)
                : UNCHANGED;
        case MEMBER_LEFT -> {
          if (sender) {
            yield new Membership(false, false);
          }
          MemberLeft left = n.getMemberLeft();
          yield left.hasPromoted() && userId.matches(left.getPromoted().getUserId())
              ? new Membership(null, true)
              : UNCHANGED;
        }
        case ADMIN_CHANGED ->
            userId.matches(n.getAdminChanged().getUser().getUserId())
                ? new Membership(null, n.getAdminChanged().getAdmin())
                : UNCHANGED;
        default -> UNCHANGED;
      };
    }
  }

  /**
   * Adds the conversation to the user's list, or makes the message its last unless a later one is
   * already there; a group's topic and member count follow the same rule. The sender's own read
   * position moves to the message. A new direct entry takes the peer's profile; an existing one
   * keeps what profile changes put there. A group notice about the user changes their membership.
   */
  private static void recordMessage(
      Connection c, Uuid userId, Uuid peerId, User peer, OutboxRecord o, Event event)
      throws SQLException {
    Message msg = event.getMessage().getMessage();
    LocalDateTime sentAt = Timestamps.fromProto(msg.getSentAt());
    LocalDateTime occurredAt = Timestamps.fromProto(event.getOccurredAt());
    long ownReadSeq = userId.matches(msg.getSenderUserId()) ? msg.getSeq() : 0;
    String peerName = peer == null ? "" : peer.getName();
    String peerIcon = peer == null ? "" : peer.getIcon();
    byte[] peerAvatar =
        peer == null || peer.getAvatar().isEmpty() ? null : peer.getAvatar().toByteArray();
    boolean peerDeleted = peer != null && peer.getDeleted();
    byte[] sender = msg.getSenderUserId().toByteArray();
    byte[] body = MessageBody.encode(msg);
    Membership m = o.group() ? Membership.of(userId, msg) : Membership.UNCHANGED;
    long seq = msg.getSeq();
    // Assignments run in order, so the IFs compare with last_seq already moved forward.
    Sql.update(
        c,
        "INSERT INTO user_conversation (user_id, conversation_id, kind, peer_user_id, peer_name,"
            + " peer_icon, peer_avatar, peer_deleted, topic, member_count, member, admin,"
            + " last_seq, last_sender_user_id, last_client_message_id, last_body, last_sent_at,"
            + " read_seq, peer_read_seq, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,"
            + " ?, ?, ?, ?, ?, ?, 0, ?) ON DUPLICATE KEY"
            + " UPDATE last_seq = GREATEST(last_seq, ?),"
            + " last_sender_user_id = IF(? >= last_seq, ?, last_sender_user_id),"
            + " last_client_message_id = IF(? >= last_seq, ?, last_client_message_id),"
            + " last_body = IF(? >= last_seq, ?, last_body),"
            + " last_sent_at = IF(? >= last_seq, ?, last_sent_at),"
            + " topic = IF(? >= last_seq, ?, topic),"
            + " member_count = IF(? >= last_seq, ?, member_count),"
            + " member = COALESCE(?, member),"
            + " admin = COALESCE(?, admin),"
            + " read_seq = GREATEST(read_seq, ?),"
            + " updated_at = GREATEST(updated_at, ?)",
        userId,
        o.conversationId(),
        o.conversationKind(),
        peerId,
        peerName,
        peerIcon,
        peerAvatar,
        peerDeleted,
        o.topic(),
        o.memberCount(),
        m.member() == null || m.member(),
        m.admin() != null && m.admin(),
        seq,
        sender,
        msg.getClientMessageId(),
        body,
        sentAt,
        ownReadSeq,
        occurredAt,
        seq,
        seq,
        sender,
        seq,
        msg.getClientMessageId(),
        seq,
        body,
        seq,
        sentAt,
        seq,
        o.topic(),
        seq,
        o.memberCount(),
        m.member(),
        m.admin(),
        ownReadSeq,
        occurredAt);
  }

  /**
   * Takes the content out of the user's copy of the deleted message, so a device syncing from
   * before it never receives the content, and out of the list when the message is the last.
   */
  private static void recordDeletion(
      Connection c, Uuid userId, OutboxRecord o, MessageDeletedEvent deleted) throws SQLException {
    if (o.targetOutboxId() != null) {
      long position = 0;
      Event copy = null;
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT position, payload FROM user_event WHERE user_id = ? AND source_id = ? AND"
                      + " outbox_id = ? FOR UPDATE",
                  userId,
                  o.conversationId(),
                  o.targetOutboxId());
          ResultSet rs = ps.executeQuery()) {
        if (rs.next()) {
          position = rs.getLong(1);
          try {
            copy = Event.parseFrom(rs.getBytes(2));
          } catch (InvalidProtocolBufferException e) {
            log.warn("corrupt event {} of user {}, not rewriting it", position, userId);
          }
        }
      }
      if (copy != null && copy.hasMessage()) {
        Event.Builder b = copy.toBuilder();
        b.getMessageBuilder()
            .getMessageBuilder()
            .clearContent()
            .clearBlobIds()
            .setDeletedAt(deleted.getDeletedAt());
        Sql.update(
            c,
            "UPDATE user_event SET payload = ? WHERE user_id = ? AND position = ?",
            b.build().toByteArray(),
            userId,
            position);
      }
    }
    Sql.update(
        c,
        "UPDATE user_conversation SET last_body = ? WHERE user_id = ? AND conversation_id = ? AND"
            + " last_seq = ?",
        MessageBody.encode(Message.newBuilder().setDeletedAt(deleted.getDeletedAt()).build()),
        userId,
        o.conversationId(),
        deleted.getSeq());
  }

  /**
   * Moves the user's own read position, or in a direct conversation the peer's, whichever the event
   * is about.
   */
  private static void recordRead(Connection c, Uuid userId, OutboxRecord o, ReadEvent read)
      throws SQLException {
    String column;
    if (userId.matches(read.getUserId())) {
      column = "read_seq";
    } else if (!o.group()) {
      column = "peer_read_seq";
    } else {
      return;
    }
    Sql.update(
        c,
        "UPDATE user_conversation SET "
            + column
            + " = GREATEST("
            + column
            + ", ?) WHERE user_id = ? AND conversation_id = ?",
        read.getSeq(),
        userId,
        o.conversationId());
  }
}
