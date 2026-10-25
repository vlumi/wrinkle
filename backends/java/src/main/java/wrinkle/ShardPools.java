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
  private final DataSource db;
  private final List<DataSource> pools = new ArrayList<>();

  /** Opens a pool per shard of the keyspace that db reaches. */
  public ShardPools(String addr, String keyspace, DataSource db) {
    this.db = db;
    for (String name : Shards.load(db, keyspace).names()) {
      pools.add(Db.open(addr, keyspace + ":" + name, 1));
    }
    if (pools.isEmpty()) {
      pools.add(db);
    }
  }

  /** One pool per shard. */
  public List<DataSource> all() {
    return pools;
  }

  public void close() {
    for (DataSource shard : pools) {
      if (shard != db && shard instanceof HikariDataSource h) {
        h.close();
      }
    }
  }
}
