package wrinkle;

import com.zaxxer.hikari.HikariDataSource;
import java.util.ArrayList;
import java.util.List;
import javax.sql.DataSource;

/**
 * Pools of one connection to each shard of a keyspace, so a query sees that shard alone; without
 * vtgate, the keyspace's own pool.
 */
public final class ShardPools {
  /** A pool of one shard, by its name; "-" for a keyspace that is not sharded. */
  public record Shard(String name, DataSource db) {}

  private final DataSource db;
  private final List<Shard> shards = new ArrayList<>();

  /** Opens a pool per shard of the keyspace that db reaches. */
  public ShardPools(String addr, String keyspace, DataSource db) {
    this.db = db;
    for (String name : Shards.load(db, keyspace).names()) {
      shards.add(new Shard(name, Db.open(addr, keyspace + ":" + name, 1)));
    }
    if (shards.isEmpty()) {
      shards.add(new Shard("-", db));
    }
  }

  /** One pool per shard. */
  public List<Shard> all() {
    return shards;
  }

  public void close() {
    for (Shard shard : shards) {
      if (shard.db() != db && shard.db() instanceof HikariDataSource h) {
        h.close();
      }
    }
  }
}
