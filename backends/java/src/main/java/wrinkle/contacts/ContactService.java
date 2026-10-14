package wrinkle.contacts;

import com.google.protobuf.ByteString;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Map;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.accounts.AccountService;
import wrinkle.v1.AddContactRequest;
import wrinkle.v1.Contact;
import wrinkle.v1.ContactState;
import wrinkle.v1.User;

/**
 * The contact requests, against the user keyspace. A change writes only the requesting user's own
 * side, with its outbox row in the same transaction under the user row's lock; the fan-out carries
 * it to the other user.
 */
public final class ContactService {
  static final int MAX_PER_STATE = 1000;

  private final DataSource db;
  private final AccountService accounts;
  private final Contacts contacts;

  public ContactService(DataSource db, AccountService accounts, Contacts contacts) {
    this.db = db;
    this.accounts = accounts;
    this.contacts = contacts;
  }

  // The list's queries, one per state, each on the user's shard: the user's own rows with the
  // copies of the other side, and the copies of requests the user has no row of their own for.
  private static final String OWN_COLUMNS =
      "SELECT c.other_user_id, c.state, c.updated_at, p.state, p.updated_at FROM contact c LEFT"
          + " JOIN contact_peer p ON p.user_id = c.user_id AND p.other_user_id = c.other_user_id";
  private static final List<String> LIST_QUERIES =
      List.of(
          OWN_COLUMNS
              + " WHERE c.user_id = ? AND c.state = 'request' AND p.state = 'request' ORDER BY"
              + " GREATEST(c.updated_at, p.updated_at) DESC LIMIT ?",
          OWN_COLUMNS
              + " WHERE c.user_id = ? AND c.state = 'request' AND (p.state IS NULL OR p.state <>"
              + " 'request') ORDER BY GREATEST(c.updated_at, COALESCE(p.updated_at,"
              + " c.updated_at)) DESC LIMIT ?",
          OWN_COLUMNS
              + " WHERE c.user_id = ? AND c.state = 'block' ORDER BY GREATEST(c.updated_at,"
              + " COALESCE(p.updated_at, c.updated_at)) DESC LIMIT ?",
          "SELECT p.other_user_id, c.state, c.updated_at, p.state, p.updated_at FROM contact_peer"
              + " p LEFT JOIN contact c ON c.user_id = p.user_id AND c.other_user_id ="
              + " p.other_user_id WHERE p.user_id = ? AND p.state = 'request' AND c.user_id IS"
              + " NULL ORDER BY p.updated_at DESC LIMIT ?");

  /**
   * The users the user has any state with, newest change first, at most MAX_PER_STATE of each
   * state; deleted accounts left out.
   */
  public List<Contact> list(Uuid userId) throws SQLException {
    List<Entry> all = new ArrayList<>();
    try (Connection c = db.getConnection()) {
      for (String query : LIST_QUERIES) {
        try (PreparedStatement ps = Sql.prepare(c, query, userId, MAX_PER_STATE);
            ResultSet rs = ps.executeQuery()) {
          while (rs.next()) {
            all.add(
                new Entry(
                    Uuid.of(rs.getBytes(1)),
                    row(rs.getString(2), rs.getObject(3, LocalDateTime.class)),
                    row(rs.getString(4), rs.getObject(5, LocalDateTime.class))));
          }
        }
      }
    }
    Map<Uuid, User> profiles = accounts.profiles(all.stream().map(Entry::userId).toList());
    List<Contact> out = new ArrayList<>(all.size());
    for (Entry e : all) {
      User u = profiles.get(e.userId());
      // A user that does not exist comes back as only the id, without a name.
      if (u.getName().isEmpty() || u.getDeleted()) {
        continue;
      }
      ContactState state =
          Contacts.view(
              e.own() == null ? null : e.own().state(), e.peer() == null ? null : e.peer().state());
      out.add(Contacts.contact(u, state, Contacts.later(e.own(), e.peer())));
    }
    out.sort(
        Comparator.comparing((Contact x) -> Timestamps.fromProto(x.getUpdatedAt())).reversed());
    return out;
  }

  private record Entry(Uuid userId, Contacts.Row own, Contacts.Row peer) {}

  private static Contacts.Row row(String state, LocalDateTime updatedAt) {
    return state == null ? null : new Contacts.Row(state, updatedAt);
  }

  /**
   * Asks the user, by name or id, to be a contact, or accepts their request. Returns the other user
   * as the requesting user now sees them.
   */
  public Contact add(Uuid userId, AddContactRequest req) throws SQLException {
    Uuid otherId =
        switch (req.getUserCase()) {
          case NAME ->
              AccountService.validName(req.getName()) ? accounts.resolve(req.getName()) : null;
          case USER_ID -> userId(req.getUserId());
          default -> throw ApiError.invalidArgument("name or user_id is required");
        };
    if (otherId == null) {
      throw ApiError.notFound("no such user");
    }
    if (otherId.equals(userId)) {
      throw ApiError.invalidArgument("you can't add yourself");
    }
    User other = accounts.profile(otherId);
    if (other == null || other.getDeleted()) {
      throw ApiError.notFound("no such user");
    }
    Contacts.Pair p = contacts.pair(userId, otherId);
    if (Contacts.BLOCK.equals(p.own())) {
      throw ApiError.failedPrecondition("unblock this user first");
    }
    if (Contacts.BLOCK.equals(p.theirs())) {
      throw ApiError.permissionDenied("can't add this user");
    }
    if (!Contacts.REQUEST.equals(p.own())) {
      Sql.inTransaction(
          db,
          c -> {
            if (!Contacts.lockUser(c, userId)) {
              throw ApiError.unauthenticated("the account is deleted");
            }
            Contacts.Row own = Contacts.own(c, userId, otherId, true);
            if (own != null && Contacts.BLOCK.equals(own.state())) {
              throw ApiError.failedPrecondition("unblock this user first");
            }
            if (own != null) {
              return null;
            }
            LocalDateTime now = Timestamps.now();
            Sql.update(
                c,
                "INSERT INTO contact (user_id, other_user_id, state, updated_at) VALUES (?, ?, ?,"
                    + " ?)",
                userId,
                otherId,
                Contacts.REQUEST,
                now);
            Contacts.changed(c, userId, otherId, ContactState.CONTACT_STATE_OUTGOING, now);
            return null;
          });
    }
    try (Connection c = db.getConnection()) {
      return Contacts.current(c, userId, other);
    }
  }

  /**
   * Ends any contact or request with the user, for both, through the fan-out. A block stays; an
   * unknown user or oneself changes nothing.
   */
  public void remove(Uuid userId, ByteString otherBytes) throws SQLException {
    Uuid otherId = userId(otherBytes);
    if (otherId.equals(userId) || accounts.profile(otherId) == null) {
      return;
    }
    Sql.inTransaction(
        db,
        c -> {
          if (!Contacts.lockUser(c, userId)) {
            return null;
          }
          Contacts.Row own = Contacts.own(c, userId, otherId, true);
          LocalDateTime now = Timestamps.now();
          if (own != null && Contacts.BLOCK.equals(own.state())) {
            // The block stays, and the fan-out ends any request of the other user's again.
            Contacts.changed(c, userId, otherId, ContactState.CONTACT_STATE_BLOCKED, now);
            return null;
          }
          Sql.update(
              c,
              "DELETE FROM contact WHERE user_id = ? AND other_user_id = ? AND state = ?",
              userId,
              otherId,
              Contacts.REQUEST);
          // Also without a row of one's own: declining ends the other user's request.
          Contacts.changed(c, userId, otherId, ContactState.CONTACT_STATE_UNSPECIFIED, now);
          return null;
        });
  }

  /** Blocks the user, ending any contact or request between the two through the fan-out. */
  public void block(Uuid userId, ByteString otherBytes) throws SQLException {
    Uuid otherId = userId(otherBytes);
    if (otherId.equals(userId)) {
      throw ApiError.invalidArgument("you can't block yourself");
    }
    if (accounts.profile(otherId) == null) {
      throw ApiError.notFound("no such user");
    }
    Sql.inTransaction(
        db,
        c -> {
          if (!Contacts.lockUser(c, userId)) {
            return null;
          }
          Contacts.Row own = Contacts.own(c, userId, otherId, true);
          if (own != null && Contacts.BLOCK.equals(own.state())) {
            return null;
          }
          LocalDateTime now = Timestamps.now();
          Sql.update(
              c,
              "INSERT INTO contact (user_id, other_user_id, state, updated_at) VALUES (?, ?, ?, ?)"
                  + " ON DUPLICATE KEY UPDATE state = ?, updated_at = ?",
              userId,
              otherId,
              Contacts.BLOCK,
              now,
              Contacts.BLOCK,
              now);
          Contacts.changed(c, userId, otherId, ContactState.CONTACT_STATE_BLOCKED, now);
          return null;
        });
  }

  /** Lifts the user's block of the other user; no contact comes back. */
  public void unblock(Uuid userId, ByteString otherBytes) throws SQLException {
    Uuid otherId = userId(otherBytes);
    Sql.inTransaction(
        db,
        c -> {
          if (!Contacts.lockUser(c, userId)) {
            return null;
          }
          if (Sql.update(
                  c,
                  "DELETE FROM contact WHERE user_id = ? AND other_user_id = ? AND state = ?",
                  userId,
                  otherId,
                  Contacts.BLOCK)
              > 0) {
            Contacts.changed(
                c, userId, otherId, ContactState.CONTACT_STATE_UNSPECIFIED, Timestamps.now());
          }
          return null;
        });
  }

  private static Uuid userId(ByteString b) {
    Uuid id = Uuid.parse(b);
    if (id == null) {
      throw ApiError.invalidArgument("user_id must be 16 bytes");
    }
    return id;
  }
}
