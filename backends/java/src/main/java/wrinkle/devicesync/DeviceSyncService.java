package wrinkle.devicesync;

import com.google.protobuf.InvalidProtocolBufferException;
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
import wrinkle.conversation.ConversationService;
import wrinkle.v1.ConnectRequest;
import wrinkle.v1.ConnectResponse;
import wrinkle.v1.Conversation;
import wrinkle.v1.Event;
import wrinkle.v1.Message;
import wrinkle.v1.PushService;
import wrinkle.v1.SyncResponse;
import wrinkle.v1.User;

/**
 * The sync service: sign-in and devices, the user's event log, and the conversation list, against
 * the user keyspace.
 */
public final class DeviceSyncService {
  private static final int MAX_NAME_LENGTH = 64;
  private static final int MAX_PUSH_TOKEN = 200;
  private static final int DEFAULT_SYNC_LIMIT = 100;
  private static final int MAX_SYNC_LIMIT = 1000;
  private static final int MAX_CONVERSATION_LIST = 200;

  private static final String SELECT_CONVERSATIONS =
      "SELECT conversation_id, peer_user_id, peer_name, last_seq, last_sender_user_id,"
          + " last_client_message_id, last_body, last_sent_at, read_seq, peer_read_seq"
          + " FROM user_conversation";

  private final DataSource db;
  private final ConversationService conversations;

  public DeviceSyncService(DataSource db, ConversationService conversations) {
    this.db = db;
    this.conversations = conversations;
  }

  private static boolean validName(String name) {
    return !name.isEmpty() && name.codePointCount(0, name.length()) <= MAX_NAME_LENGTH;
  }

  /** Signs the user in by name, creating the user on first use, and registers the device. */
  public ConnectResponse connect(ConnectRequest req) throws SQLException {
    if (!validName(req.getUserName())) {
      throw ApiError.invalidArgument("user_name must be 1 to " + MAX_NAME_LENGTH + " characters");
    }
    if (!req.getDeviceName().isEmpty() && !validName(req.getDeviceName())) {
      throw ApiError.invalidArgument(
          "device_name must be at most " + MAX_NAME_LENGTH + " characters");
    }
    long userId = Ids.user(req.getUserName());
    LocalDateTime now = Timestamps.now();
    long deviceId = req.getDeviceId() == 0 ? Ids.random() : req.getDeviceId();
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          "INSERT INTO user (user_id, name, log_head, created_at) VALUES (?, ?, 0, ?) ON DUPLICATE"
              + " KEY UPDATE name = name",
          userId,
          req.getUserName(),
          now);
      Sql.update(
          c,
          "INSERT INTO device (user_id, device_id, name, acked_position, created_at, last_seen_at)"
              + " VALUES (?, ?, ?, 0, ?, ?) ON DUPLICATE KEY UPDATE last_seen_at = ?",
          userId,
          deviceId,
          req.getDeviceName(),
          now,
          now,
          now);
      ConnectResponse.Builder resp =
          ConnectResponse.newBuilder()
              .setUser(User.newBuilder().setUserId(userId).setName(req.getUserName()))
              .setDeviceId(deviceId);
      Long deviceCursor =
          selectLong(
              c,
              "SELECT acked_position FROM device WHERE user_id = ? AND device_id = ?",
              userId,
              deviceId);
      if (deviceCursor != null) {
        resp.setDeviceCursor(deviceCursor);
      }
      Long logHead = selectLong(c, "SELECT log_head FROM user WHERE user_id = ?", userId);
      if (logHead != null) {
        resp.setLogHead(logHead);
      }
      return resp.build();
    }
  }

  /** The first column of the first row, or null without one. */
  private static Long selectLong(Connection c, String sql, Object... args) throws SQLException {
    try (PreparedStatement ps = Sql.prepare(c, sql, args);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? rs.getLong(1) : null;
    }
  }

  /** Records how far a device has processed the log. */
  public void ack(long userId, long deviceId, long cursor) throws SQLException {
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          "UPDATE device SET acked_position = GREATEST(acked_position, ?), last_seen_at = ? WHERE"
              + " user_id = ? AND device_id = ?",
          cursor,
          Timestamps.now(),
          userId,
          deviceId);
    }
  }

  /**
   * Records where to push for the device while it is not connected; an empty token removes the
   * registration.
   */
  public void registerPushToken(long userId, long deviceId, PushService service, String token)
      throws SQLException {
    if (!token.isEmpty()) {
      if (service != PushService.PUSH_SERVICE_APNS
          && service != PushService.PUSH_SERVICE_APNS_SANDBOX) {
        throw ApiError.invalidArgument("unknown push service " + service);
      }
      if (token.length() > MAX_PUSH_TOKEN) {
        throw ApiError.invalidArgument("push token is longer than " + MAX_PUSH_TOKEN + " bytes");
      }
    }
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          "UPDATE device SET push_service = ?, push_token = ? WHERE user_id = ? AND device_id = ?",
          token.isEmpty() ? null : service.name(),
          token.isEmpty() ? null : token,
          userId,
          deviceId);
    }
  }

  /** Events after the cursor, oldest first. */
  public SyncResponse sync(long userId, long cursor, int limit) throws SQLException {
    int pageSize = limit == 0 ? DEFAULT_SYNC_LIMIT : Math.min(limit, MAX_SYNC_LIMIT);
    SyncResponse.Builder resp = SyncResponse.newBuilder().setCursor(cursor);
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT payload FROM user_event WHERE user_id = ? AND position > ? ORDER BY"
                    + " position LIMIT ?",
                userId,
                cursor,
                pageSize + 1);
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        if (resp.getEventsCount() == pageSize) {
          resp.setHasMore(true);
          break;
        }
        Event event;
        try {
          event = Event.parseFrom(rs.getBytes(1));
        } catch (InvalidProtocolBufferException e) {
          throw new SQLException("unmarshal event for user " + userId, e);
        }
        resp.addEvents(event);
        resp.setCursor(event.getCursor());
      }
    }
    return resp.build();
  }

  /** Finds or creates the conversation with the named peer. */
  public Conversation open(long userId, String peerName) throws SQLException {
    if (!validName(peerName)) {
      throw ApiError.invalidArgument(
          "peer_user_name must be 1 to " + MAX_NAME_LENGTH + " characters");
    }
    long peerId = Ids.user(peerName);
    if (peerId == userId) {
      throw ApiError.invalidArgument("a conversation needs two different users");
    }
    try (Connection c = db.getConnection()) {
      requireUser(c, peerId, peerName);
      long userA = Math.min(userId, peerId);
      long userB = Math.max(userId, peerId);
      long conversationId = Ids.conversation(userA, userB);
      conversations.ensure(conversationId, userA, userB);
      Sql.update(
          c,
          "INSERT IGNORE INTO user_conversation (user_id, conversation_id, peer_user_id,"
              + " peer_name, last_seq, read_seq, peer_read_seq, updated_at) VALUES (?, ?, ?, ?, 0,"
              + " 0, 0, ?)",
          userId,
          conversationId,
          peerId,
          peerName,
          Timestamps.now());
      List<Conversation> list =
          selectConversations(
              c,
              SELECT_CONVERSATIONS + " WHERE user_id = ? AND conversation_id = ?",
              userId,
              conversationId);
      if (list.isEmpty()) {
        throw new SQLException("conversation " + conversationId + " missing after insert");
      }
      return list.getFirst();
    }
  }

  /**
   * Fails unless the user with this id has exactly this name: the id is a hash, which can collide.
   */
  private static void requireUser(Connection c, long userId, String name) throws SQLException {
    try (PreparedStatement ps = Sql.prepare(c, "SELECT name FROM user WHERE user_id = ?", userId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next() || !rs.getString(1).equals(name)) {
        throw ApiError.notFound("no user named \"" + name + "\"");
      }
    }
  }

  /** The user's conversations, most recently active first. */
  public List<Conversation> listConversations(long userId) throws SQLException {
    try (Connection c = db.getConnection()) {
      return selectConversations(
          c,
          SELECT_CONVERSATIONS + " WHERE user_id = ? ORDER BY updated_at DESC LIMIT ?",
          userId,
          MAX_CONVERSATION_LIST);
    }
  }

  private static List<Conversation> selectConversations(Connection c, String sql, Object... args)
      throws SQLException {
    try (PreparedStatement ps = Sql.prepare(c, sql, args);
        ResultSet rs = ps.executeQuery()) {
      List<Conversation> list = new ArrayList<>();
      while (rs.next()) {
        list.add(conversation(rs));
      }
      return list;
    }
  }

  private static Conversation conversation(ResultSet rs) throws SQLException {
    long conversationId = rs.getLong(1);
    long lastSeq = rs.getLong(4);
    Conversation.Builder c =
        Conversation.newBuilder()
            .setConversationId(conversationId)
            .setPeer(User.newBuilder().setUserId(rs.getLong(2)).setName(rs.getString(3)))
            .setLastSeq(lastSeq)
            .setReadSeq(rs.getLong(9))
            .setPeerReadSeq(rs.getLong(10));
    long sender = rs.getLong(5);
    if (lastSeq > 0 && !rs.wasNull()) {
      c.setLastMessage(
          Message.newBuilder()
              .setConversationId(conversationId)
              .setSeq(lastSeq)
              .setSenderUserId(sender)
              .setClientMessageId(rs.getString(6))
              .setText(rs.getString(7))
              .setSentAt(Timestamps.toProto(rs.getObject(8, LocalDateTime.class))));
    }
    return c.build();
  }
}
