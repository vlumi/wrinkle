package wrinkle.devicesync;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.Duration;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.function.BooleanSupplier;
import javax.sql.DataSource;
import wrinkle.ShardPools;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;

/**
 * Drops events older than the retention from the user logs, a page at a time on each shard, oldest
 * first. Any number may run: raising a tail and dropping what is under it can be done twice.
 */
public final class LogTrimmer {
  static final int TRIM_PAGE = 1000;

  record Event(Uuid userId, long position) {}

  private final DataSource db;
  private final ShardPools shards;
  private final Duration retention;

  /**
   * Finds the oldest events of each shard of the keyspace through a connection to that shard alone,
   * so a page never spans shards; without vtgate the keyspace is one shard.
   */
  public LogTrimmer(String addr, String keyspace, DataSource db, Duration retention) {
    this.db = db;
    this.retention = retention;
    this.shards = new ShardPools(addr, keyspace, db);
  }

  /** The keyspace's shards, each a pool to that shard alone. */
  public List<ShardPools.Shard> shards() {
    return shards.all();
  }

  public void close() {
    shards.close();
  }

  /** Drops the shard's events older than the retention, until stopped, and returns how many. */
  public int trimShard(ShardPools.Shard shard, BooleanSupplier stopped) throws SQLException {
    LocalDateTime cutoff = Timestamps.now().minus(retention);
    int total = 0;
    int n;
    do {
      n = trimPage(shard.db(), cutoff);
      total += n;
    } while (n == TRIM_PAGE && !stopped.getAsBoolean());
    return total;
  }

  /** Drops the shard's oldest events, up to a page of them; returns how many. */
  private int trimPage(DataSource shard, LocalDateTime cutoff) throws SQLException {
    List<Event> page = new ArrayList<>();
    try (Connection c = shard.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT user_id, position FROM user_event WHERE occurred_at < ? ORDER BY"
                    + " occurred_at LIMIT ?",
                cutoff,
                TRIM_PAGE);
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        page.add(new Event(Uuid.of(rs.getBytes(1)), rs.getLong(2)));
      }
    }
    for (Map.Entry<Uuid, Long> tail : tails(page).entrySet()) {
      trimUser(tail.getKey(), tail.getValue());
    }
    return page.size();
  }

  /**
   * Each user's newest position in the page, in the order the users first appear. A user's log is
   * cut below it, so it stays contiguous even where an event's time is a little out of order.
   */
  static Map<Uuid, Long> tails(List<Event> page) {
    Map<Uuid, Long> tails = new LinkedHashMap<>();
    for (Event e : page) {
      tails.merge(e.userId(), e.position(), Math::max);
    }
    return tails;
  }

  /**
   * Raises the user's tail and drops the events up to it in one transaction, so a sync that misses
   * them sees the new tail.
   */
  private void trimUser(Uuid userId, long tail) throws SQLException {
    Sql.inTransaction(
        db,
        c -> {
          Sql.update(
              c,
              "UPDATE user SET log_tail = GREATEST(log_tail, ?) WHERE user_id = ?",
              tail,
              userId);
          Sql.update(c, "DELETE FROM user_event WHERE user_id = ? AND position <= ?", userId, tail);
          return null;
        });
  }
}
