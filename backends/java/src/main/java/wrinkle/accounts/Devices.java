package wrinkle.accounts;

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
import wrinkle.v1.Device;

/** A user's signed-in devices: listing, renaming and signing out, each with its DeviceEvent. */
public final class Devices {
  static final int MAX_NAME_BYTES = 64;
  private static final String SELECT =
      "SELECT device_id, name, created_at, last_seen_at, push_token IS NOT NULL FROM device";

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
        .build();
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
    return inTransaction(
        c -> {
          lockSignedIn(c, userId, deviceId);
          Sql.update(
              c,
              "UPDATE device SET name = ? WHERE user_id = ? AND device_id = ?",
              name,
              userId,
              deviceId);
          UserOutbox.device(c, userId, deviceId, false);
          return read(c, userId, deviceId);
        });
  }

  /** Signs out another of the user's devices. */
  public void signOutOther(Uuid userId, long ownDeviceId, long deviceId) throws SQLException {
    if (deviceId == ownDeviceId) {
      throw ApiError.invalidArgument("this device; use SignOut");
    }
    inTransaction(
        c -> {
          lockSignedIn(c, userId, deviceId);
          signOut(c, userId, deviceId);
          return null;
        });
  }

  /** Signs this device out. */
  public void signOut(Uuid userId, long deviceId) throws SQLException {
    inTransaction(
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

  interface Work<T> {
    T run(Connection c) throws SQLException;
  }

  /** Runs the work in one transaction, rolled back on any failure. */
  static <T> T inTransaction(DataSource db, Work<T> work) throws SQLException {
    try (Connection c = db.getConnection()) {
      c.setAutoCommit(false);
      try {
        T result = work.run(c);
        c.commit();
        return result;
      } catch (SQLException | RuntimeException e) {
        c.rollback();
        throw e;
      }
    }
  }

  private <T> T inTransaction(Work<T> work) throws SQLException {
    return inTransaction(db, work);
  }
}
