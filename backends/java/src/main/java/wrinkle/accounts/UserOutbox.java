package wrinkle.accounts;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.v1.Device;
import wrinkle.v1.DeviceEvent;
import wrinkle.v1.Event;

/** A user's own changes as outbox rows, written in the transaction that makes the change. */
final class UserOutbox {
  private UserOutbox() {}

  /**
   * Takes the user's next outbox id, locking the user row for the rest of the transaction, and
   * writes the event. Like the conversation outbox, the row is deleted again at once; the binlog
   * keeps the insert.
   */
  static void write(Connection c, Uuid userId, String kind, Event event) throws SQLException {
    Sql.update(c, "UPDATE user SET last_outbox_id = last_outbox_id + 1 WHERE user_id = ?", userId);
    long outboxId;
    try (PreparedStatement ps =
            Sql.prepare(c, "SELECT last_outbox_id FROM user WHERE user_id = ?", userId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw new SQLException("user " + userId + " missing");
      }
      outboxId = rs.getLong(1);
    }
    Sql.update(
        c,
        "INSERT INTO user_outbox (user_id, outbox_id, kind, payload, created_at) VALUES (?, ?, ?,"
            + " ?, ?)",
        userId,
        outboxId,
        kind,
        event.toByteArray(),
        Timestamps.fromProto(event.getOccurredAt()));
    Sql.update(c, "DELETE FROM user_outbox WHERE user_id = ? AND outbox_id = ?", userId, outboxId);
  }

  /** Writes a DeviceEvent for the device as it is now in the transaction. */
  static void device(Connection c, Uuid userId, long deviceId, boolean signedOut)
      throws SQLException {
    Device device = Devices.read(c, userId, deviceId);
    if (device == null) {
      throw new SQLException("device " + deviceId + " of user " + userId + " missing");
    }
    LocalDateTime now = Timestamps.now();
    write(
        c,
        userId,
        "device",
        Event.newBuilder()
            .setOccurredAt(Timestamps.toProto(now))
            .setDevice(DeviceEvent.newBuilder().setDevice(device).setSignedOut(signedOut))
            .build());
  }
}
