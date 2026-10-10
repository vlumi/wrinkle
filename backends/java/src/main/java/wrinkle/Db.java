package wrinkle;

import com.zaxxer.hikari.HikariConfig;
import com.zaxxer.hikari.HikariDataSource;
import javax.sql.DataSource;

/** Connection pools to a Vitess keyspace through vtgate. */
public final class Db {
  private Db() {}

  public static DataSource open(String addr, String keyspace, int maxConns) {
    HikariConfig cfg = new HikariConfig();
    // Client-side prepared statements, so vtgate sees plain SQL.
    cfg.setJdbcUrl(
        "jdbc:mysql://"
            + addr
            + "/"
            + keyspace
            + "?useServerPrepStmts=false&connectionTimeZone=UTC"
            + "&forceConnectionTimeZoneToSession=true&sslMode=DISABLED"
            + "&characterEncoding=utf8&allowPublicKeyRetrieval=true");
    cfg.setUsername("root");
    cfg.setPassword("");
    cfg.setMaximumPoolSize(maxConns);
    cfg.setPoolName(keyspace);
    cfg.setAutoCommit(true);
    cfg.setConnectionTimeout(5_000);
    return new HikariDataSource(cfg);
  }
}
