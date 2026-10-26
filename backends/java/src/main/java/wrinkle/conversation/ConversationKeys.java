package wrinkle.conversation;

import com.google.protobuf.ByteString;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashSet;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.accounts.Devices;
import wrinkle.conversation.Rows.Header;
import wrinkle.v1.Device;
import wrinkle.v1.ErrorReason;
import wrinkle.v1.GetConversationKeysResponse;
import wrinkle.v1.KeyRecipient;
import wrinkle.v1.ReceivedKey;
import wrinkle.v1.UserDevices;
import wrinkle.v1.WrappedKey;

/**
 * Conversations' key epochs: the epoch keys member devices wrap for each other, which the server
 * stores and hands out without being able to unwrap them.
 */
public final class ConversationKeys {
  private static final int MAX_KEYS = 500;
  private static final int WRAPPED_BYTES = 60;
  // Wraps inserted per statement.
  private static final int INSERT_BATCH = 200;
  // Keys GetConversationKeys returns at most.
  private static final int MAX_KEY_PAGE = 100;

  private final DataSource db;
  private final DataSource userDb;
  private final Devices devices;
  private final ConversationService conversations;

  public ConversationKeys(
      DataSource conversationDb,
      DataSource userDb,
      Devices devices,
      ConversationService conversations) {
    this.db = conversationDb;
    this.userDb = userDb;
    this.devices = devices;
    this.conversations = conversations;
  }

  private record Wrap(Uuid userId, long deviceId, byte[] wrapped) {}

  private record DeviceRef(Uuid userId, long deviceId) {}

  /**
   * Publishes wraps from the requesting device: the next epoch, more recipients of the current one,
   * or an older one for the user's own devices. Wrapping for the requesting device itself makes the
   * epoch: for an existing epoch that fails unless this device made it, so of two devices
   * publishing the same epoch, the later fails.
   */
  public void publish(
      Uuid userId, long deviceId, Uuid conversationId, long epoch, List<WrappedKey> keys)
      throws SQLException {
    if (keys.isEmpty() || keys.size() > MAX_KEYS) {
      throw ApiError.invalidArgument("keys must have 1 to " + MAX_KEYS + " entries");
    }
    List<Wrap> wraps = new ArrayList<>(keys.size());
    boolean forSelf = false;
    boolean forOthers = false;
    for (WrappedKey k : keys) {
      Uuid recipient = Uuid.parse(k.getRecipientUserId());
      if (recipient == null) {
        throw ApiError.invalidArgument("recipient_user_id must be 16 bytes");
      }
      if (k.getRecipientDeviceId() == 0) {
        throw ApiError.invalidArgument("recipient_device_id must be set");
      }
      if (k.getWrapped().size() != WRAPPED_BYTES) {
        throw ApiError.invalidArgument("wrapped must be " + WRAPPED_BYTES + " bytes");
      }
      wraps.add(new Wrap(recipient, k.getRecipientDeviceId(), k.getWrapped().toByteArray()));
      forSelf |= recipient.equals(userId) && k.getRecipientDeviceId() == deviceId;
      forOthers |= !recipient.equals(userId);
    }
    boolean self = forSelf;
    try (Connection c = db.getConnection()) {
      // Members are told apart before the device's key is looked up in the other keyspace.
      Rows.memberHeader(c, conversationId, userId, false);
    }
    // Across a block, either way, no keys go to the other side; the user's own devices may still
    // share the history.
    if (forOthers) {
      conversations.checkBlocks(userId, conversationId);
    }
    byte[] publicKey = publicKey(userId, deviceId);
    if (publicKey == null) {
      throw ApiError.failedPrecondition(
          ErrorReason.ERROR_REASON_DEVICE_HAS_NO_KEY, "this device has no key");
    }
    requireKeyedDevices(wraps);
    Sql.inTransaction(
        db,
        c -> {
          Header h = Rows.memberHeader(c, conversationId, userId, true);
          long current = h.keyEpoch();
          ApiError notNext =
              ApiError.failedPrecondition(
                  ErrorReason.ERROR_REASON_KEY_EPOCH_TAKEN, "epoch is not next");
          if (epoch == current + 1) {
            requireMembers(h, wraps);
            insert(c, conversationId, epoch, userId, deviceId, publicKey, wraps);
            Sql.update(
                c,
                "UPDATE conversation SET key_epoch = ?, rotate = FALSE WHERE conversation_id = ?",
                epoch,
                conversationId);
          } else if (epoch >= 1 && epoch <= current) {
            if (epoch == current) {
              requireMembers(h, wraps);
            } else {
              for (Wrap w : wraps) {
                if (!w.userId().equals(userId)) {
                  throw ApiError.permissionDenied(
                      "keys of an earlier epoch go only to the user's own devices");
                }
              }
            }
            if (self && !madeBy(c, conversationId, epoch, userId, deviceId)) {
              throw notNext;
            }
            insert(c, conversationId, epoch, userId, deviceId, publicKey, wraps);
          } else {
            throw notNext;
          }
          return null;
        });
  }

  /**
   * Refuses wraps for anything but signed-in devices with keys, so epochs hold real devices only.
   */
  private void requireKeyedDevices(List<Wrap> wraps) throws SQLException {
    Set<Uuid> users = new LinkedHashSet<>();
    for (Wrap w : wraps) {
      users.add(w.userId());
    }
    // More users than a conversation's members is refused there.
    Set<DeviceRef> keyed = new HashSet<>();
    for (UserDevices u : devices.keys(users.stream().map(Uuid::toByteString).toList())) {
      Uuid id = Uuid.parse(u.getUserId());
      for (Device d : u.getDevicesList()) {
        keyed.add(new DeviceRef(id, d.getDeviceId()));
      }
    }
    for (Wrap w : wraps) {
      if (!keyed.contains(new DeviceRef(w.userId(), w.deviceId()))) {
        throw ApiError.invalidArgument(
            "device "
                + Long.toUnsignedString(w.deviceId())
                + " of "
                + w.userId()
                + " is not a signed-in device with a key");
      }
    }
  }

  private static void requireMembers(Header h, List<Wrap> wraps) {
    for (Wrap w : wraps) {
      if (!h.includes(w.userId())) {
        throw ApiError.invalidArgument("recipient " + w.userId() + " is not a member");
      }
    }
  }

  /** Whether the device holds the epoch's key wrapped by itself, that is, it made the epoch. */
  private static boolean madeBy(
      Connection c, Uuid conversationId, long epoch, Uuid userId, long deviceId)
      throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT sender_user_id, sender_device_id FROM conversation_key WHERE"
                    + " conversation_id = ? AND recipient_user_id = ? AND recipient_device_id = ?"
                    + " AND epoch = ?",
                conversationId,
                userId,
                deviceId,
                epoch);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() && userId.equals(Uuid.of(rs.getBytes(1))) && rs.getLong(2) == deviceId;
    }
  }

  /** The device's public key, or null if it has none. */
  private byte[] publicKey(Uuid userId, long deviceId) throws SQLException {
    try (Connection c = userDb.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT public_key FROM device WHERE user_id = ? AND device_id = ?",
                userId,
                deviceId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? rs.getBytes(1) : null;
    }
  }

  /** Inserts the wraps; a recipient's existing wrap of the epoch is kept. */
  private static void insert(
      Connection c,
      Uuid conversationId,
      long epoch,
      Uuid sender,
      long senderDevice,
      byte[] senderKey,
      List<Wrap> wraps)
      throws SQLException {
    LocalDateTime now = Timestamps.now();
    for (int from = 0; from < wraps.size(); from += INSERT_BATCH) {
      List<Wrap> batch = wraps.subList(from, Math.min(from + INSERT_BATCH, wraps.size()));
      List<Object> args = new ArrayList<>(batch.size() * 9);
      for (Wrap w : batch) {
        args.addAll(
            List.of(
                conversationId,
                w.userId(),
                w.deviceId(),
                epoch,
                sender,
                senderDevice,
                senderKey,
                w.wrapped(),
                now));
      }
      Sql.update(
          c,
          "INSERT IGNORE INTO conversation_key (conversation_id, recipient_user_id,"
              + " recipient_device_id, epoch, sender_user_id, sender_device_id, sender_public_key,"
              + " wrapped, created_at) VALUES "
              + String.join(", ", Collections.nCopies(batch.size(), "(?, ?, ?, ?, ?, ?, ?, ?, ?)")),
          args.toArray());
    }
  }

  /**
   * The conversation's key state and the wraps for the requesting device after the given epoch.
   * Former members may ask too; no wraps of later epochs reach them, and they get none of the
   * current membership.
   */
  public GetConversationKeysResponse get(
      Uuid userId, long deviceId, Uuid conversationId, long afterEpoch, long limit)
      throws SQLException {
    int pageSize = limit == 0 || limit > MAX_KEY_PAGE ? MAX_KEY_PAGE : (int) limit;
    try (Connection c = db.getConnection()) {
      Header h = Rows.header(c, conversationId, false);
      boolean current;
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT left_seq FROM conversation_member WHERE conversation_id = ? AND user_id ="
                      + " ?",
                  conversationId,
                  userId);
          ResultSet rs = ps.executeQuery()) {
        if (!rs.next()) {
          throw ApiError.permissionDenied(
              "user " + userId + " is not in conversation " + conversationId);
        }
        rs.getLong(1);
        current = rs.wasNull();
      }
      // A former member gets none of the current membership.
      GetConversationKeysResponse.Builder resp =
          GetConversationKeysResponse.newBuilder()
              .setCurrentEpoch((int) h.keyEpoch())
              .setRotate(current && h.rotate());
      if (current && h.keyEpoch() > 0) {
        try (PreparedStatement ps =
                Sql.prepare(
                    c,
                    "SELECT recipient_user_id, recipient_device_id FROM conversation_key WHERE"
                        + " conversation_id = ? AND epoch = ? ORDER BY recipient_user_id,"
                        + " recipient_device_id",
                    conversationId,
                    h.keyEpoch());
            ResultSet rs = ps.executeQuery()) {
          while (rs.next()) {
            resp.addCurrentRecipients(
                KeyRecipient.newBuilder()
                    .setUserId(ByteString.copyFrom(rs.getBytes(1)))
                    .setDeviceId(rs.getLong(2)));
          }
        }
      }
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT epoch, sender_user_id, sender_device_id, sender_public_key, wrapped FROM"
                      + " conversation_key WHERE conversation_id = ? AND recipient_user_id = ? AND"
                      + " recipient_device_id = ? AND epoch > ? ORDER BY epoch LIMIT ?",
                  conversationId,
                  userId,
                  deviceId,
                  afterEpoch,
                  pageSize + 1);
          ResultSet rs = ps.executeQuery()) {
        while (rs.next()) {
          if (resp.getKeysCount() == pageSize) {
            resp.setHasMore(true);
            break;
          }
          resp.addKeys(
              ReceivedKey.newBuilder()
                  .setEpoch((int) rs.getLong(1))
                  .setSenderUserId(ByteString.copyFrom(rs.getBytes(2)))
                  .setSenderDeviceId(rs.getLong(3))
                  .setSenderPublicKey(ByteString.copyFrom(rs.getBytes(4)))
                  .setWrapped(ByteString.copyFrom(rs.getBytes(5))));
        }
      }
      return resp.build();
    }
  }
}
