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
import wrinkle.v1.User;

/**
 * Delivers a profile change to the user's own log, then to each conversation partner's, with the
 * partner's list entry updated in the same transaction.
 */
final class ProfileFanout {
  // How many conversation partners are read at a time.
  private static final int PAGE = 500;
  // How many partners of one user are delivered to at a time.
  private static final int CONCURRENCY = 16;

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
                                "UPDATE user_conversation SET peer_name = ?, peer_icon = ? WHERE"
                                    + " user_id = ? AND conversation_id = ?",
                                user.getName(),
                                user.getIcon(),
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
        return;
      }
      after = page.getLast().conversationId();
    }
  }

  /** A page of the user's conversations after the given one, in id order. */
  private List<Partner> partners(Uuid userId, Uuid after) throws SQLException {
    try (Connection c = userLog.db().getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT conversation_id, peer_user_id FROM user_conversation WHERE user_id = ? AND"
                    + " conversation_id > ? ORDER BY conversation_id LIMIT ?",
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
