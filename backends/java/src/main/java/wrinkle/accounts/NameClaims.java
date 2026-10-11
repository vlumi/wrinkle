package wrinkle.accounts;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;

/**
 * The claims in user_name that keep names unique. A claim is valid while its owner's row still has
 * that name. Names are compared by MySQL, under the claim table's collation, never in Java.
 */
final class NameClaims {
  private static final int ATTEMPTS = 3;

  private final DataSource db;

  NameClaims(DataSource db) {
    this.db = db;
  }

  /** Whether the user's row has the name, ignoring case. */
  boolean owns(Connection c, Uuid userId, String name) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT 1 FROM user WHERE user_id = ? AND name = ? COLLATE utf8mb4_0900_as_ci",
                userId,
                name);
        ResultSet rs = ps.executeQuery()) {
      return rs.next();
    }
  }

  /** The user holding the claim on the name, or null. */
  Uuid owner(Connection c, String name) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(c, "SELECT user_id FROM user_name WHERE name = ?", name);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? Uuid.of(rs.getBytes(1)) : null;
    }
  }

  /**
   * Takes the name for the user. A claim the user already holds is fine; a valid claim of another
   * user fails with ALREADY_EXISTS; a stale one is taken over.
   */
  void claim(String name, Uuid userId) throws SQLException {
    try (Connection c = db.getConnection()) {
      for (int attempt = 0; attempt < ATTEMPTS; attempt++) {
        try {
          Sql.update(
              c,
              "INSERT INTO user_name (name, user_id, created_at) VALUES (?, ?, ?)",
              name,
              userId,
              Timestamps.now());
          return;
        } catch (SQLException e) {
          if (!Sql.isDuplicate(e)) {
            throw e;
          }
        }
        Uuid owner = owner(c, name);
        if (owner == null) {
          continue;
        }
        if (owner.equals(userId)) {
          return;
        }
        if (owns(c, owner, name)) {
          throw ApiError.alreadyExists("the name is taken");
        }
        Sql.update(c, "DELETE FROM user_name WHERE name = ? AND user_id = ?", name, owner);
      }
    }
    throw ApiError.unavailable("the name is contended; try again");
  }

  /** The user with the name, ignoring case, or null when no user has it. */
  Uuid resolve(String name) throws SQLException {
    try (Connection c = db.getConnection()) {
      Uuid owner = owner(c, name);
      return owner != null && owns(c, owner, name) ? owner : null;
    }
  }

  /**
   * Gives up the claim on the old name after a rename. Names differing only in case share one
   * claim, which stays as it is: claims compare ignoring case, and Vitess cannot change a vindex
   * column in place.
   */
  void release(Uuid userId, String oldName, String newName) throws SQLException {
    try (Connection c = db.getConnection()) {
      boolean same;
      try (PreparedStatement ps =
              Sql.prepare(c, "SELECT ? = ? COLLATE utf8mb4_0900_as_ci", oldName, newName);
          ResultSet rs = ps.executeQuery()) {
        same = rs.next() && rs.getBoolean(1);
      }
      if (!same) {
        Sql.update(c, "DELETE FROM user_name WHERE name = ? AND user_id = ?", oldName, userId);
      }
    }
  }
}
