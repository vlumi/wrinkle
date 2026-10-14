package wrinkle.fanout;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Future;
import java.util.concurrent.Semaphore;
import wrinkle.Sql;
import wrinkle.Uuid;
import wrinkle.backend.v1.SignedOut;
import wrinkle.v1.User;

/**
 * Delivers a profile change to the user's own log, then to each direct conversation partner's, with
 * the partner's list entry updated in the same transaction. A deleted account's change goes to the
 * partners alike; then its devices are signed out and its data removed.
 */
final class ProfileFanout {
  // How many conversation partners are read at a time.
  private static final int PAGE = 500;
  // How many partners of one user are delivered to at a time.
  private static final int CONCURRENCY = 16;
  // Rows removed per statement when purging a deleted account.
  private static final int PURGE_PAGE = 1000;

  private final UserLog userLog;
  private final ExecutorService tasks;

  ProfileFanout(UserLog userLog, ExecutorService tasks) {
    this.userLog = userLog;
    this.tasks = tasks;
  }

  private record Partner(Uuid conversationId, Uuid peerId) {}

  void deliver(UserOutboxRecord o) throws Exception {
    User user = o.event().getProfile().getUser();
    userLog.append(o.userId(), o.userId(), o.outboxId(), o.event(), null);
    Uuid after = null;
    while (true) {
      List<Partner> page = partners(o.userId(), after);
      Semaphore slots = new Semaphore(CONCURRENCY);
      List<Future<?>> running = new ArrayList<>();
      for (Partner p : page) {
        slots.acquire();
        running.add(
            tasks.submit(
                () -> {
                  try {
                    userLog.append(
                        p.peerId(),
                        o.userId(),
                        o.outboxId(),
                        o.event(),
                        (c, e) ->
                            Sql.update(
                                c,
                                "UPDATE user_conversation SET peer_name = ?, peer_icon = ?,"
                                    + " peer_avatar = ?, peer_deleted = ? WHERE user_id = ? AND"
                                    + " conversation_id = ?",
                                user.getName(),
                                user.getIcon(),
                                user.getAvatar().isEmpty() ? null : user.getAvatar().toByteArray(),
                                user.getDeleted(),
                                p.peerId(),
                                p.conversationId()));
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
      if (page.size() < PAGE) {
        break;
      }
      after = page.getLast().conversationId();
    }
    if (user.getDeleted()) {
      userLog.pushSignedOut(o.userId(), SignedOut.newBuilder().setAllDevices(true).build());
      purge(o.userId());
    }
  }

  /**
   * Removes a deleted account's log, conversation list, devices, palette and its own contact rows,
   * and leaves its uploads to the sweeper, but those made stickers, which stay. Other users' rows
   * toward it stay. The user row stays, so nothing is appended for it again. Repeating it is
   * harmless.
   */
  private void purge(Uuid userId) throws SQLException {
    try (Connection c = userLog.db().getConnection()) {
      for (String table :
          List.of(
              "user_event",
              "user_conversation",
              "device",
              "palette_sticker",
              "contact",
              "contact_peer")) {
        while (Sql.update(
                c, "DELETE FROM " + table + " WHERE user_id = ? LIMIT ?", userId, PURGE_PAGE)
            == PURGE_PAGE) {
          // Until a batch comes up short.
        }
      }
      // A sticker upload is used only once it is a sticker.
      Sql.update(
          c, "UPDATE upload SET used = FALSE WHERE user_id = ? AND purpose <> 'sticker'", userId);
    }
  }

  /** A page of the user's direct conversations after the given one, in id order. */
  private List<Partner> partners(Uuid userId, Uuid after) throws SQLException {
    try (Connection c = userLog.db().getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT conversation_id, peer_user_id FROM user_conversation WHERE user_id = ? AND"
                    + " conversation_id > ? AND peer_user_id IS NOT NULL ORDER BY conversation_id"
                    + " LIMIT ?",
                userId,
                after == null ? new byte[Uuid.LENGTH] : after.bytes(),
                PAGE);
        ResultSet rs = ps.executeQuery()) {
      List<Partner> page = new ArrayList<>();
      while (rs.next()) {
        page.add(new Partner(Uuid.of(rs.getBytes(1)), Uuid.of(rs.getBytes(2))));
      }
      return page;
    }
  }
}
