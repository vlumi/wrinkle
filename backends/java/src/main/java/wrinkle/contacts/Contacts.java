package wrinkle.contacts;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.accounts.UserOutbox;
import wrinkle.v1.Contact;
import wrinkle.v1.ContactEvent;
import wrinkle.v1.ContactState;
import wrinkle.v1.Event;
import wrinkle.v1.User;

/**
 * Each user's own side toward other users, in contact on their shard, and the other side as the
 * fan-out copied it, in contact_peer. Checks read the users' own rows only.
 */
public final class Contacts {
  /** The state column's values; none only in contact_peer. */
  public static final String REQUEST = "request";

  public static final String BLOCK = "block";
  public static final String NONE = "none";

  private final DataSource db;

  public Contacts(DataSource userDb) {
    this.db = userDb;
  }

  /**
   * Refuses a direct conversation between the user and the peer when either blocked the other: the
   * user's own block first.
   */
  public void checkMessaging(Uuid userId, Uuid peerId) throws SQLException {
    Pair p = pair(userId, peerId);
    if (BLOCK.equals(p.own())) {
      throw ApiError.failedPrecondition("you blocked this user");
    }
    if (BLOCK.equals(p.theirs())) {
      throw ApiError.permissionDenied("can't message this user");
    }
  }

  /** The users among the others who are not contacts of the user, each side asked. */
  public Set<Uuid> notContacts(Uuid userId, List<Uuid> others) throws SQLException {
    Set<Uuid> missing = new HashSet<>(others);
    if (others.isEmpty()) {
      return missing;
    }
    String in = String.join(", ", Collections.nCopies(others.size(), "?"));
    Set<Uuid> asked = new HashSet<>();
    Set<Uuid> askedBack = new HashSet<>();
    try (Connection c = db.getConnection()) {
      List<Object> args = new ArrayList<>();
      args.add(userId);
      args.addAll(others);
      args.add(REQUEST);
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT other_user_id FROM contact WHERE user_id = ? AND other_user_id IN ("
                      + in
                      + ") AND state = ?",
                  args.toArray());
          ResultSet rs = ps.executeQuery()) {
        while (rs.next()) {
          asked.add(Uuid.of(rs.getBytes(1)));
        }
      }
      args = new ArrayList<>(others);
      args.add(userId);
      args.add(REQUEST);
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT user_id FROM contact WHERE user_id IN ("
                      + in
                      + ") AND other_user_id = ? AND state = ?",
                  args.toArray());
          ResultSet rs = ps.executeQuery()) {
        while (rs.next()) {
          askedBack.add(Uuid.of(rs.getBytes(1)));
        }
      }
    }
    asked.retainAll(askedBack);
    missing.removeAll(asked);
    return missing;
  }

  /** Both users' own sides toward each other, null where there is none. */
  record Pair(String own, String theirs) {}

  /** The user's own side toward the other and the other's toward the user, in one query. */
  Pair pair(Uuid userId, Uuid otherId) throws SQLException {
    String own = null;
    String theirs = null;
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT user_id, state FROM contact WHERE user_id IN (?, ?) AND other_user_id IN"
                    + " (?, ?)",
                userId,
                otherId,
                userId,
                otherId);
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        if (userId.equals(Uuid.of(rs.getBytes(1)))) {
          own = rs.getString(2);
        } else {
          theirs = rs.getString(2);
        }
      }
    }
    return new Pair(own, theirs);
  }

  /** The other user as the user sees them, from the user's own side and the copied other side. */
  public static ContactState view(String own, String peer) {
    if (BLOCK.equals(own)) {
      return ContactState.CONTACT_STATE_BLOCKED;
    }
    if (REQUEST.equals(own)) {
      return REQUEST.equals(peer)
          ? ContactState.CONTACT_STATE_CONTACT
          : ContactState.CONTACT_STATE_OUTGOING;
    }
    if (own == null && REQUEST.equals(peer)) {
      return ContactState.CONTACT_STATE_INCOMING;
    }
    return ContactState.CONTACT_STATE_UNSPECIFIED;
  }

  /** The state column a change's payload names: the user's own side after it. */
  public static String side(ContactState state) {
    return switch (state) {
      case CONTACT_STATE_OUTGOING -> REQUEST;
      case CONTACT_STATE_BLOCKED -> BLOCK;
      default -> NONE;
    };
  }

  /** A row's state and when it was last changed. */
  record Row(String state, LocalDateTime updatedAt) {}

  /** The user's own row toward the other, locked when locking; null for none. */
  static Row own(Connection c, Uuid userId, Uuid otherId, boolean locking) throws SQLException {
    return row(c, "contact", userId, otherId, locking);
  }

  /** The other side as copied to the user's shard; null for none. */
  static Row peer(Connection c, Uuid userId, Uuid otherId) throws SQLException {
    return row(c, "contact_peer", userId, otherId, false);
  }

  private static Row row(Connection c, String table, Uuid userId, Uuid otherId, boolean locking)
      throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT state, updated_at FROM "
                    + table
                    + " WHERE user_id = ? AND other_user_id = ?"
                    + (locking ? " FOR UPDATE" : ""),
                userId,
                otherId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? new Row(rs.getString(1), rs.getObject(2, LocalDateTime.class)) : null;
    }
  }

  /**
   * The other user as the user now sees them, on the user's shard; updated_at is the later of the
   * two rows', unset when there are none.
   */
  public static Contact current(Connection c, Uuid userId, User other) throws SQLException {
    Uuid otherId = Uuid.parse(other.getUserId());
    Row own = own(c, userId, otherId, false);
    Row peer = peer(c, userId, otherId);
    return contact(
        other,
        view(own == null ? null : own.state(), peer == null ? null : peer.state()),
        later(own, peer));
  }

  static LocalDateTime later(Row a, Row b) {
    LocalDateTime x = a == null ? null : a.updatedAt();
    LocalDateTime y = b == null ? null : b.updatedAt();
    if (x == null) {
      return y;
    }
    return y == null || x.isAfter(y) ? x : y;
  }

  static Contact contact(User user, ContactState state, LocalDateTime updatedAt) {
    Contact.Builder b = Contact.newBuilder().setUser(user).setState(state);
    if (updatedAt != null) {
      b.setUpdatedAt(Timestamps.toProto(updatedAt));
    }
    return b.build();
  }

  /**
   * Locks the user row for the rest of the transaction, serializing the user's changes; false if
   * the account is gone.
   */
  static boolean lockUser(Connection c, Uuid userId) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT 1 FROM user WHERE user_id = ? AND deleted_at IS NULL FOR UPDATE",
                userId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next();
    }
  }

  /** Writes the user's own side toward the other after a change to the user's outbox. */
  static void changed(
      Connection c, Uuid userId, Uuid otherId, ContactState state, LocalDateTime now)
      throws SQLException {
    UserOutbox.write(
        c,
        userId,
        "contact",
        Event.newBuilder()
            .setOccurredAt(Timestamps.toProto(now))
            .setContact(
                ContactEvent.newBuilder()
                    .setContact(
                        Contact.newBuilder()
                            .setUser(User.newBuilder().setUserId(otherId.toByteString()))
                            .setState(state)))
            .build());
  }

  /**
   * Ends the user's request toward the other, as RemoveContact by the user would, in one
   * transaction on the user's shard; nothing when there is no request.
   */
  public static void endRequest(DataSource db, Uuid userId, Uuid otherId) throws SQLException {
    Sql.inTransaction(
        db,
        c -> {
          if (!lockUser(c, userId)) {
            return null;
          }
          LocalDateTime now = Timestamps.now();
          if (Sql.update(
                  c,
                  "DELETE FROM contact WHERE user_id = ? AND other_user_id = ? AND state = ?",
                  userId,
                  otherId,
                  REQUEST)
              > 0) {
            changed(c, userId, otherId, ContactState.CONTACT_STATE_UNSPECIFIED, now);
          }
          return null;
        });
  }
}
