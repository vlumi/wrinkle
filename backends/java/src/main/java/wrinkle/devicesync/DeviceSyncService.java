package wrinkle.devicesync;

import com.google.protobuf.ByteString;
import com.google.protobuf.InvalidProtocolBufferException;
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
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.accounts.AccountService;
import wrinkle.conversation.ConversationService;
import wrinkle.v1.Conversation;
import wrinkle.v1.Event;
import wrinkle.v1.Message;
import wrinkle.v1.PushService;
import wrinkle.v1.SyncResponse;
import wrinkle.v1.User;

/**
 * The sync service: devices' acks and push registrations, the user's event log, and the
 * conversation list, against the user keyspace.
 */
public final class DeviceSyncService {
  private static final int MAX_PUSH_TOKEN = 200;
  private static final int DEFAULT_SYNC_LIMIT = 100;
  private static final int MAX_SYNC_LIMIT = 1000;
  private static final int MAX_CONVERSATION_LIST = 200;

  private static final String SELECT_CONVERSATIONS =
      "SELECT conversation_id, peer_user_id, peer_name, peer_icon, last_seq,"
          + " last_sender_user_id, last_client_message_id, last_body, last_sent_at, read_seq,"
          + " peer_read_seq"
          + " FROM user_conversation";

  private final DataSource db;
  private final AccountService accounts;
  private final ConversationService conversations;

  public DeviceSyncService(
      DataSource db, AccountService accounts, ConversationService conversations) {
    this.db = db;
    this.accounts = accounts;
    this.conversations = conversations;
  }

  /** Records how far a device has processed the log. */
  public void ack(Uuid userId, long deviceId, long cursor) throws SQLException {
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
  public void registerPushToken(Uuid userId, long deviceId, PushService service, String token)
      throws SQLException {
    if (!token.isEmpty()) {
      if (service != PushService.PUSH_SERVICE_APNS
          && service != PushService.PUSH_SERVICE_APNS_SANDBOX) {
        throw ApiError.invalidArgument("unknown push service " + service);
      }
      if (token.getBytes(StandardCharsets.UTF_8).length > MAX_PUSH_TOKEN) {
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
  public SyncResponse sync(Uuid userId, long cursor, int limit) throws SQLException {
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
  public Conversation open(Uuid userId, String peerName) throws SQLException {
    if (!AccountService.validName(peerName)) {
      throw ApiError.invalidArgument("peer_name is not a valid name");
    }
    Uuid peerId = accounts.resolve(peerName);
    if (peerId == null) {
      throw ApiError.notFound("no user named \"" + peerName + "\"");
    }
    if (peerId.equals(userId)) {
      throw ApiError.invalidArgument("a conversation needs two different users");
    }
    User peer = accounts.profile(peerId);
    if (peer == null) {
      throw ApiError.notFound("no user named \"" + peerName + "\"");
    }
    boolean ordered = userId.compareTo(peerId) < 0;
    Uuid conversationId = conversations.open(ordered ? userId : peerId, ordered ? peerId : userId);
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          "INSERT IGNORE INTO user_conversation (user_id, conversation_id, peer_user_id,"
              + " peer_name, peer_icon, last_seq, read_seq, peer_read_seq, updated_at) VALUES (?,"
              + " ?, ?, ?, ?, 0, 0, 0, ?)",
          userId,
          conversationId,
          peerId,
          peer.getName(),
          peer.getIcon(),
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

  /** The user's conversations, most recently active first. */
  public List<Conversation> listConversations(Uuid userId) throws SQLException {
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
    ByteString conversationId = ByteString.copyFrom(rs.getBytes(1));
    long lastSeq = rs.getLong(5);
    Conversation.Builder c =
        Conversation.newBuilder()
            .setConversationId(conversationId)
            .setPeer(
                User.newBuilder()
                    .setUserId(ByteString.copyFrom(rs.getBytes(2)))
                    .setName(rs.getString(3))
                    .setIcon(rs.getString(4)))
            .setLastSeq(lastSeq)
            .setReadSeq(rs.getLong(10))
            .setPeerReadSeq(rs.getLong(11));
    byte[] sender = rs.getBytes(6);
    if (lastSeq > 0 && sender != null) {
      c.setLastMessage(
          Message.newBuilder()
              .setConversationId(conversationId)
              .setSeq(lastSeq)
              .setSenderUserId(ByteString.copyFrom(sender))
              .setClientMessageId(rs.getString(7))
              .setText(rs.getString(8))
              .setSentAt(Timestamps.toProto(rs.getObject(9, LocalDateTime.class))));
    }
    return c.build();
  }
}
