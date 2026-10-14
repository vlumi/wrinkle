package wrinkle.fanout;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.accounts.AccountService;
import wrinkle.contacts.Contacts;
import wrinkle.v1.Contact;
import wrinkle.v1.ContactEvent;
import wrinkle.v1.Event;
import wrinkle.v1.User;

/**
 * Carries a change of one user's side toward another to the other user: copies it to their
 * contact_peer, ends their request when the side ended or became a block, through their own outbox,
 * and tells both users how they now stand.
 */
final class ContactFanout {
  private final UserLog userLog;

  ContactFanout(UserLog userLog) {
    this.userLog = userLog;
  }

  void deliver(UserOutboxRecord o) throws SQLException {
    Uuid userId = o.userId();
    Contact change = o.event().getContact().getContact();
    Uuid otherId = Uuid.parse(change.getUser().getUserId());
    if (otherId == null || otherId.equals(userId)) {
      return;
    }
    String side = Contacts.side(change.getState());
    LocalDateTime occurredAt = Timestamps.fromProto(o.event().getOccurredAt());
    User user = profile(userId);
    User other = profile(otherId);
    if (other != null && !other.getDeleted()) {
      boolean latest = copy(otherId, userId, side, o.outboxId(), occurredAt);
      if (latest && !Contacts.REQUEST.equals(side)) {
        Contacts.endRequest(userLog.db(), otherId, userId);
      }
    }
    if (other != null) {
      append(userId, userId, other, o);
    }
    if (user != null) {
      append(otherId, userId, user, o);
    }
  }

  /**
   * Copies the user's side to the other user's contact_peer unless a later change is already there;
   * true when the stored copy is this change's.
   */
  private boolean copy(
      Uuid ownerId, Uuid fromId, String side, long outboxId, LocalDateTime occurredAt)
      throws SQLException {
    try (Connection c = userLog.db().getConnection()) {
      // Assignments run in order, so source_outbox_id moves last.
      Sql.update(
          c,
          "INSERT INTO contact_peer (user_id, other_user_id, state, source_outbox_id, updated_at)"
              + " VALUES (?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE"
              + " state = IF(? > source_outbox_id, ?, state),"
              + " updated_at = IF(? > source_outbox_id, ?, updated_at),"
              + " source_outbox_id = GREATEST(source_outbox_id, ?)",
          ownerId,
          fromId,
          side,
          outboxId,
          occurredAt,
          outboxId,
          side,
          outboxId,
          occurredAt,
          outboxId);
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT source_outbox_id FROM contact_peer WHERE user_id = ? AND other_user_id ="
                      + " ?",
                  ownerId,
                  fromId);
          ResultSet rs = ps.executeQuery()) {
        return rs.next() && rs.getLong(1) == outboxId;
      }
    }
  }

  /**
   * Appends the other user as the recipient now sees them to the recipient's log, read under the
   * log's lock, so the last event appended has the latest state.
   */
  private void append(Uuid recipientId, Uuid sourceId, User about, UserOutboxRecord o)
      throws SQLException {
    userLog.append(
        recipientId,
        sourceId,
        o.outboxId(),
        c ->
            Event.newBuilder()
                .setOccurredAt(o.event().getOccurredAt())
                .setContact(
                    ContactEvent.newBuilder().setContact(Contacts.current(c, recipientId, about)))
                .build(),
        null);
  }

  /** The user's profile, or null when there is no such user. */
  private User profile(Uuid userId) throws SQLException {
    try (Connection c = userLog.db().getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT name, icon, avatar, deleted_at IS NOT NULL FROM user WHERE user_id = ?",
                userId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        return null;
      }
      return AccountService.user(
          userId, rs.getString(1), rs.getString(2), rs.getBytes(3), rs.getBoolean(4));
    }
  }
}
