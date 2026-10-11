package wrinkle;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.SQLException;

/** Statements with positional arguments bound in order. */
public final class Sql {
  private Sql() {}

  /** A statement ready to execute; the caller closes it. */
  public static PreparedStatement prepare(Connection c, String sql, Object... args)
      throws SQLException {
    PreparedStatement ps = c.prepareStatement(sql);
    try {
      for (int i = 0; i < args.length; i++) {
        ps.setObject(i + 1, args[i]);
      }
    } catch (SQLException | RuntimeException e) {
      ps.close();
      throw e;
    }
    return ps;
  }

  /** Executes an INSERT, UPDATE or DELETE, returning the number of rows it changed. */
  public static int update(Connection c, String sql, Object... args) throws SQLException {
    try (PreparedStatement ps = prepare(c, sql, args)) {
      return ps.executeUpdate();
    }
  }
}
