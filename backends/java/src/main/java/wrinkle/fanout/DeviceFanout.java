package wrinkle.fanout;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Future;
import java.util.concurrent.Semaphore;
import javax.sql.DataSource;
import wrinkle.Kinds;
import wrinkle.Sql;
import wrinkle.Uuid;
import wrinkle.backend.v1.SignedOut;
import wrinkle.v1.Device;
import wrinkle.v1.DeviceEvent;
import wrinkle.v1.Event;

/**
 * Delivers a device change to the user's own log. When the device has a key, its id and key also go
 * to the user's direct conversation partners and the members of the user's groups, each once, so
 * they can encrypt for it.
 */
final class DeviceFanout {
  // How many list entries are read at a time.
  private static final int PAGE = 500;
  // How many others are delivered to at a time.
  private static final int CONCURRENCY = 16;

  private final UserLog userLog;
  private final DataSource conversationDb;
  private final ExecutorService tasks;

  DeviceFanout(UserLog userLog, DataSource conversationDb, ExecutorService tasks) {
    this.userLog = userLog;
    this.conversationDb = conversationDb;
    this.tasks = tasks;
  }

  /** A sign-out also closes the device's connections, even when delivered before. */
  void deliver(UserOutboxRecord o) throws Exception {
    userLog.append(o.userId(), o.userId(), o.outboxId(), o.event(), null);
    DeviceEvent d = o.event().getDevice();
    if (d.getSignedOut()) {
      userLog.pushSignedOut(
          o.userId(), SignedOut.newBuilder().addDeviceIds(d.getDevice().getDeviceId()).build());
    }
    if (d.getDevice().getPublicKey().isEmpty()) {
      return;
    }
    Event keyOnly =
        o.event().toBuilder()
            .setDevice(
                DeviceEvent.newBuilder()
                    .setUserId(o.userId().toByteString())
                    .setDevice(
                        Device.newBuilder()
                            .setDeviceId(d.getDevice().getDeviceId())
                            .setPublicKey(d.getDevice().getPublicKey()))
                    .setSignedOut(d.getSignedOut()))
            .build();
    Semaphore slots = new Semaphore(CONCURRENCY);
    List<Future<?>> running = new ArrayList<>();
    for (Uuid other : contacts(o.userId())) {
      slots.acquire();
      running.add(
          tasks.submit(
              () -> {
                try {
                  userLog.append(other, o.userId(), o.outboxId(), keyOnly, null);
                  return null;
                } finally {
                  slots.release();
                }
              }));
    }
    Exception failure = null;
    for (Future<?> f : running) {
      try {
        f.get();
      } catch (ExecutionException e) {
        failure = e.getCause() instanceof Exception ex ? ex : new RuntimeException(e.getCause());
      }
    }
    if (failure != null) {
      throw failure;
    }
  }

  /** The user's direct partners and the current members of the user's groups, but the user. */
  private Set<Uuid> contacts(Uuid userId) throws SQLException {
    Set<Uuid> contacts = new LinkedHashSet<>();
    byte[] after = new byte[Uuid.LENGTH];
    while (true) {
      List<Uuid> groups = new ArrayList<>();
      int read = 0;
      try (Connection c = userLog.db().getConnection();
          PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT conversation_id, peer_user_id, kind, member FROM user_conversation"
                      + " WHERE user_id = ? AND conversation_id > ? ORDER BY"
                      + " conversation_id LIMIT ?",
                  userId,
                  after,
                  PAGE);
          ResultSet rs = ps.executeQuery()) {
        while (rs.next()) {
          read++;
          after = rs.getBytes(1);
          byte[] peer = rs.getBytes(2);
          if (peer != null) {
            contacts.add(Uuid.of(peer));
          } else if (Kinds.GROUP.equals(rs.getString(3)) && rs.getBoolean(4)) {
            groups.add(Uuid.of(after));
          }
        }
      }
      if (!groups.isEmpty()) {
        contacts.addAll(members(groups));
      }
      if (read < PAGE) {
        break;
      }
    }
    contacts.remove(userId);
    return contacts;
  }

  /** The groups' current members, from the conversation keyspace. */
  private Set<Uuid> members(List<Uuid> conversationIds) throws SQLException {
    Set<Uuid> members = new LinkedHashSet<>();
    try (Connection c = conversationDb.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT members FROM conversation WHERE conversation_id IN ("
                    + String.join(", ", Collections.nCopies(conversationIds.size(), "?"))
                    + ")",
                conversationIds.toArray());
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        members.addAll(Uuid.split(rs.getBytes(1)));
      }
    }
    return members;
  }
}
