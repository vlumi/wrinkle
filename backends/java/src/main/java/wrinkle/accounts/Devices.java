package wrinkle.accounts;

import com.google.protobuf.ByteString;
import java.nio.charset.StandardCharsets;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.HashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.v1.Device;
import wrinkle.v1.UserDevices;

/**
 * A user's signed-in devices: listing, renaming, signing out and setting their keys, each with its
 * DeviceEvent; and the key directory.
 */
public final class Devices {
  static final int MAX_NAME_BYTES = 64;
  private static final String SELECT =
      "SELECT device_id, name, created_at, last_seen_at, push_token IS NOT NULL, public_key FROM"
          + " device";
  private static final int PUBLIC_KEY_BYTES = 32;
  private static final int MAX_KEY_USERS = 256;

  private final DataSource db;

  public Devices(DataSource db) {
    this.db = db;
  }

  static void checkName(String name, String field) {
    if (name.getBytes(StandardCharsets.UTF_8).length > MAX_NAME_BYTES) {
      throw ApiError.invalidArgument(
          field + " must be UTF-8 of at most " + MAX_NAME_BYTES + " bytes");
    }
  }

  /** The device as stored, or null. */
  static Device read(Connection c, Uuid userId, long deviceId) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(c, SELECT + " WHERE user_id = ? AND device_id = ?", userId, deviceId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? device(rs) : null;
    }
  }

  private static Device device(ResultSet rs) throws SQLException {
    return Device.newBuilder()
        .setDeviceId(rs.getLong(1))
        .setName(rs.getString(2))
        .setCreatedAt(Timestamps.toProto(rs.getObject(3, LocalDateTime.class)))
        .setLastSeenAt(Timestamps.toProto(rs.getObject(4, LocalDateTime.class)))
        .setPush(rs.getBoolean(5))
        .setPublicKey(bytes(rs.getBytes(6)))
        .build();
  }

  private static ByteString bytes(byte[] b) {
    return b == null ? ByteString.EMPTY : ByteString.copyFrom(b);
  }

  /** The user's signed-in devices, oldest first. */
  public List<Device> list(Uuid userId) throws SQLException {
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                SELECT
                    + " WHERE user_id = ? AND token_hash IS NOT NULL ORDER BY created_at,"
                    + " device_id",
                userId);
        ResultSet rs = ps.executeQuery()) {
      List<Device> list = new ArrayList<>();
      while (rs.next()) {
        list.add(device(rs));
      }
      return list;
    }
  }

  /** Renames one of the user's signed-in devices. */
  public Device rename(Uuid userId, long deviceId, String name) throws SQLException {
    checkName(name, "name");
    return Sql.inTransaction(
        db,
        c -> {
          lockSignedIn(c, userId, deviceId);
          Sql.update(
              c,
              "UPDATE device SET name = ? WHERE user_id = ? AND device_id = ?",
              name,
              userId,
              deviceId);
          UserOutbox.deviceName(c, userId, deviceId);
          return read(c, userId, deviceId);
        });
  }

  /**
   * Sets the device's public key, once: the same key again does nothing, a different one is
   * refused.
   */
  public void setKey(Uuid userId, long deviceId, ByteString publicKey) throws SQLException {
    if (publicKey.size() != PUBLIC_KEY_BYTES) {
      throw ApiError.invalidArgument("public_key must be " + PUBLIC_KEY_BYTES + " bytes");
    }
    Sql.inTransaction(
        db,
        c -> {
          byte[] stored;
          try (PreparedStatement ps =
                  Sql.prepare(
                      c,
                      "SELECT public_key FROM device WHERE user_id = ? AND device_id = ? AND"
                          + " token_hash IS NOT NULL FOR UPDATE",
                      userId,
                      deviceId);
              ResultSet rs = ps.executeQuery()) {
            if (!rs.next()) {
              throw ApiError.notFound("no such device");
            }
            stored = rs.getBytes(1);
          }
          if (stored != null) {
            if (!publicKey.equals(ByteString.copyFrom(stored))) {
              throw ApiError.failedPrecondition("this device has a different key");
            }
            return null;
          }
          Sql.update(
              c,
              "UPDATE device SET public_key = ? WHERE user_id = ? AND device_id = ?",
              publicKey.toByteArray(),
              userId,
              deviceId);
          UserOutbox.device(c, userId, deviceId, false);
          return null;
        });
  }

  /**
   * The signed-in devices with keys of each user asked for, as only their ids and keys, by device
   * id. Unknown and deleted users are left out.
   */
  public List<UserDevices> keys(List<ByteString> userIds) throws SQLException {
    if (userIds.isEmpty() || userIds.size() > MAX_KEY_USERS) {
      throw ApiError.invalidArgument("user_ids must have 1 to " + MAX_KEY_USERS + " entries");
    }
    Set<Uuid> ids = new LinkedHashSet<>();
    for (ByteString b : userIds) {
      Uuid id = Uuid.parse(b);
      if (id == null) {
        throw ApiError.invalidArgument("user_ids must be 16 bytes each");
      }
      ids.add(id);
    }
    String in = String.join(", ", Collections.nCopies(ids.size(), "?"));
    Map<Uuid, UserDevices.Builder> found = new HashMap<>();
    try (Connection c = db.getConnection()) {
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT user_id FROM user WHERE user_id IN (" + in + ") AND deleted_at IS NULL",
                  ids.toArray());
          ResultSet rs = ps.executeQuery()) {
        while (rs.next()) {
          Uuid id = Uuid.of(rs.getBytes(1));
          found.put(id, UserDevices.newBuilder().setUserId(id.toByteString()));
        }
      }
      if (!found.isEmpty()) {
        try (PreparedStatement ps =
                Sql.prepare(
                    c,
                    "SELECT user_id, device_id, public_key FROM device WHERE user_id IN ("
                        + String.join(", ", Collections.nCopies(found.size(), "?"))
                        + ") AND token_hash IS NOT NULL AND public_key IS NOT NULL",
                    found.keySet().toArray());
            ResultSet rs = ps.executeQuery()) {
          Map<Uuid, List<Device>> devices = new HashMap<>();
          while (rs.next()) {
            devices
                .computeIfAbsent(Uuid.of(rs.getBytes(1)), k -> new ArrayList<>())
                .add(
                    Device.newBuilder()
                        .setDeviceId(rs.getLong(2))
                        .setPublicKey(ByteString.copyFrom(rs.getBytes(3)))
                        .build());
          }
          devices.forEach(
              (id, list) -> {
                list.sort(Comparator.comparing(Device::getDeviceId, Long::compareUnsigned));
                found.get(id).addAllDevices(list);
              });
        }
      }
    }
    List<UserDevices> out = new ArrayList<>(found.size());
    for (Uuid id : ids) {
      UserDevices.Builder u = found.get(id);
      if (u != null) {
        out.add(u.build());
      }
    }
    return out;
  }

  /** Signs out another of the user's devices. */
  public void signOutOther(Uuid userId, long ownDeviceId, long deviceId) throws SQLException {
    if (deviceId == ownDeviceId) {
      throw ApiError.invalidArgument("this device; use SignOut");
    }
    Sql.inTransaction(
        db,
        c -> {
          lockSignedIn(c, userId, deviceId);
          signOut(c, userId, deviceId);
          return null;
        });
  }

  /** Signs this device out. */
  public void signOut(Uuid userId, long deviceId) throws SQLException {
    Sql.inTransaction(
        db,
        c -> {
          signOut(c, userId, deviceId);
          return null;
        });
  }

  /** Clears the device's token and push registration and records it signed out. */
  static void signOut(Connection c, Uuid userId, long deviceId) throws SQLException {
    Sql.update(
        c,
        "UPDATE device SET token_hash = NULL, push_service = NULL, push_token = NULL WHERE"
            + " user_id = ? AND device_id = ?",
        userId,
        deviceId);
    UserOutbox.device(c, userId, deviceId, true);
  }

  /** Locks the device row; NOT_FOUND unless it is the user's and signed in. */
  private static void lockSignedIn(Connection c, Uuid userId, long deviceId) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT 1 FROM device WHERE user_id = ? AND device_id = ? AND token_hash IS NOT"
                    + " NULL FOR UPDATE",
                userId,
                deviceId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw ApiError.notFound("no such device");
      }
    }
  }
}
