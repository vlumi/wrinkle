package wrinkle;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.SQLException;

/** Statements with positional arguments bound in order; ids bind as BINARY(16). */
public final class Sql {
  private Sql() {}

  // MySQL error for a unique key violation.
  private static final int DUPLICATE_KEY = 1062;

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
