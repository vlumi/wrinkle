package wrinkle;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.SQLException;
import javax.sql.DataSource;

/**
 * Statements with positional arguments bound in order; ids bind as BINARY(16). Transactions on a
 * pool's connections, which are in autocommit mode otherwise.
 */
public final class Sql {
  private Sql() {}

  // MySQL error for a unique key violation.
  private static final int DUPLICATE_KEY = 1062;

  /** Work done on one connection in a transaction. */
  @FunctionalInterface
  public interface Work<T> {
    T run(Connection c) throws SQLException;
  }

  /** Runs the work in one transaction, committed when it returns and rolled back on any failure. */
  public static <T> T inTransaction(DataSource db, Work<T> work) throws SQLException {
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

  /** A statement ready to execute; the caller closes it. */
  public static PreparedStatement prepare(Connection c, String sql, Object... args)
      throws SQLException {
    PreparedStatement ps = c.prepareStatement(sql);
    try {
      for (int i = 0; i < args.length; i++) {
        Object a = args[i];
        if (a instanceof Uuid u) {
          ps.setBytes(i + 1, u.bytes());
        } else {
          ps.setObject(i + 1, a);
        }
      }
    } catch (SQLException | RuntimeException e) {
      ps.close();
      throw e;
    }
    return ps;
  }

  /** True for a unique key violation. */
  public static boolean isDuplicate(SQLException e) {
    for (Throwable t = e; t != null; t = t.getCause()) {
      if (t instanceof SQLException s && s.getErrorCode() == DUPLICATE_KEY) {
        return true;
      }
    }
    return false;
  }

  /** Executes an INSERT, UPDATE or DELETE, returning the number of rows it changed. */
  public static int update(Connection c, String sql, Object... args) throws SQLException {
    try (PreparedStatement ps = prepare(c, sql, args)) {
      return ps.executeUpdate();
    }
  }
}
