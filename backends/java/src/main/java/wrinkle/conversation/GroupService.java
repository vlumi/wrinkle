package wrinkle.conversation;

import java.nio.charset.StandardCharsets;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Kinds;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.accounts.AccountService;
import wrinkle.contacts.Contacts;
import wrinkle.conversation.Rows.Header;
import wrinkle.v1.AdminChanged;
import wrinkle.v1.Conversation;
import wrinkle.v1.ConversationKind;
import wrinkle.v1.CreateGroupRequest;
import wrinkle.v1.ErrorCode;
import wrinkle.v1.ErrorReason;
import wrinkle.v1.GroupCreated;
import wrinkle.v1.Member;
import wrinkle.v1.MemberLeft;
import wrinkle.v1.MemberRemoved;
import wrinkle.v1.MembersAdded;
import wrinkle.v1.Message;
import wrinkle.v1.Notice;
import wrinkle.v1.TopicChanged;
import wrinkle.v1.User;

/**
 * Groups: creating them and changing their members, admins and topic. Every change is a notice, a
 * message in the group's sequence, stored with its outbox row in the transaction that locks the
 * conversation row, so changes and messages reach every member in one order.
 */
public final class GroupService {
  private static final int MAX_TOPIC_BYTES = 200;
  private static final int MAX_CLIENT_GROUP_ID = 64;
  private static final int MAX_MEMBER_NAMES = 255;
  // How many of a deleted account's list entries are read at a time.
  private static final int LEAVE_PAGE = 100;

  private final DataSource db;
  private final DataSource userDb;
  private final AccountService accounts;
  private final Contacts contacts;
  private final Cold cold;

  public GroupService(
      DataSource conversationDb,
      DataSource userDb,
      AccountService accounts,
      Contacts contacts,
      Cold cold) {
    this.db = conversationDb;
    this.userDb = userDb;
    this.accounts = accounts;
    this.contacts = contacts;
    this.cold = cold;
  }

  /** Whether a topic is acceptable: as a name, but up to 200 bytes. */
  static boolean validTopic(String topic) {
    if (topic.isEmpty() || topic.getBytes(StandardCharsets.UTF_8).length > MAX_TOPIC_BYTES) {
      return false;
    }
    if (topic.codePoints().anyMatch(cp -> Character.getType(cp) == Character.CONTROL)) {
      return false;
    }
    return !AccountService.whiteSpace(topic.codePointAt(0))
        && !AccountService.whiteSpace(topic.codePointBefore(topic.length()));
  }

  private static void checkTopic(String topic) {
    if (!validTopic(topic)) {
      throw ApiError.invalidArgument(
          "topic must be 1 to "
              + MAX_TOPIC_BYTES
              + " bytes of UTF-8, without control characters or white space at either end");
    }
  }

  /**
   * The users the names belong to, without the requester and repeats, in the order named, each with
   * the name first given for them. INVALID_ARGUMENT for a name that is not valid, NOT_FOUND for one
   * nobody has.
   */
  private Map<Uuid, String> resolve(Uuid userId, List<String> names) throws SQLException {
    if (names.size() > MAX_MEMBER_NAMES) {
      throw ApiError.invalidArgument("at most " + MAX_MEMBER_NAMES + " member names");
    }
    for (String name : names) {
      if (!AccountService.validName(name)) {
        throw ApiError.invalidArgument("member name \"" + name + "\" is not a valid name");
      }
    }
    Map<Uuid, String> ids = new LinkedHashMap<>();
    for (String name : names) {
      Uuid id = accounts.resolve(name);
      if (id == null) {
        throw ApiError.notFound("no user named \"" + name + "\"");
      }
      if (!id.equals(userId)) {
        ids.putIfAbsent(id, name);
      }
    }
    return ids;
  }

  /** FAILED_PRECONDITION naming the first of the users, in order, who is not the user's contact. */
  private void requireContacts(Uuid userId, Map<Uuid, String> users) throws SQLException {
    Set<Uuid> missing = contacts.notContacts(userId, List.copyOf(users.keySet()));
    for (Map.Entry<Uuid, String> e : users.entrySet()) {
      if (missing.contains(e.getKey())) {
        throw ApiError.failedPrecondition(
            ErrorReason.ERROR_REASON_NOT_A_CONTACT, "not a contact: " + e.getValue());
      }
    }
  }

  private static ApiError tooMany() {
    return ApiError.failedPrecondition(
        ErrorReason.ERROR_REASON_GROUP_FULL,
        "a group has at most " + Rows.MAX_MEMBERS + " members");
  }

  private static Header groupHeader(Connection c, Uuid conversationId, Uuid userId)
      throws SQLException {
    return groupHeader(c, conversationId, userId, true);
  }

  private static Header groupHeader(Connection c, Uuid conversationId, Uuid userId, boolean locking)
      throws SQLException {
    Header h = Rows.header(c, conversationId, locking);
    if (!h.group()) {
      throw ApiError.failedPrecondition(
          ErrorReason.ERROR_REASON_NOT_A_GROUP,
          "conversation " + conversationId + " is not a group");
    }
    Rows.requireMember(h, conversationId, userId);
    return h;
  }

  private record MemberRow(boolean admin, long joinedSeq, boolean current) {}

  private static MemberRow memberRow(Connection c, Uuid conversationId, Uuid userId)
      throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT admin, joined_seq, left_seq IS NULL FROM conversation_member WHERE"
                    + " conversation_id = ? AND user_id = ?",
                conversationId,
                userId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? new MemberRow(rs.getBoolean(1), rs.getLong(2), rs.getBoolean(3)) : null;
    }
  }

  private static void requireAdmin(Connection c, Uuid conversationId, Uuid userId)
      throws SQLException {
    MemberRow row = memberRow(c, conversationId, userId);
    if (row == null || !row.admin()) {
      throw ApiError.permissionDenied("only admins may do that");
    }
  }

  /** Current admins other than the user. */
  private static int otherAdmins(Connection c, Uuid conversationId, Uuid userId)
      throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT COUNT(*) FROM conversation_member WHERE conversation_id = ? AND left_seq IS"
                    + " NULL AND admin AND user_id <> ?",
                conversationId,
                userId);
        ResultSet rs = ps.executeQuery()) {
      rs.next();
      return rs.getInt(1);
    }
  }

  /**
   * Stores a notice as the next message and moves the conversation row to its new members and
   * topic; the recipients get its event.
   */
  private static void notice(
      Connection c,
      Uuid conversationId,
      Header h,
      Uuid sender,
      Notice notice,
      Set<Uuid> members,
      String topic,
      Set<Uuid> recipients)
      throws SQLException {
    long seq = h.lastSeq() + 1;
    LocalDateTime now = Timestamps.now();
    Sql.update(
        c,
        "UPDATE conversation SET members = ?, topic = ?, last_seq = ? WHERE conversation_id = ?",
        Uuid.join(members),
        topic,
        seq,
        conversationId);
    Message msg =
        Message.newBuilder()
            .setConversationId(conversationId.toByteString())
            .setSeq(seq)
            .setSenderUserId(sender.toByteString())
            .setClientMessageId(Rows.noticeClientId())
            .setNotice(notice)
            .setSentAt(Timestamps.toProto(now))
            .build();
    Rows.storeMessage(c, msg, recipients, Kinds.GROUP, topic, members.size());
  }

  /**
   * Creates a group of the requester's contacts with the requester as its admin, or returns the one
   * created by an earlier request with the same client group id. The claim, on the creator's shard,
   * comes first; a claim whose conversation row is missing gets it here.
   */
  public Conversation create(Uuid userId, CreateGroupRequest req) throws SQLException {
    String topic = req.getTopic();
    checkTopic(topic);
    String clientGroupId = req.getClientGroupId();
    int idBytes = clientGroupId.getBytes(StandardCharsets.UTF_8).length;
    if (idBytes == 0 || idBytes > MAX_CLIENT_GROUP_ID) {
      throw ApiError.invalidArgument(
          "client_group_id must be 1 to " + MAX_CLIENT_GROUP_ID + " bytes");
    }
    Map<Uuid, String> named = resolve(userId, req.getMemberNamesList());
    List<Uuid> invitees = List.copyOf(named.keySet());
    if (invitees.size() + 1 > Rows.MAX_MEMBERS) {
      throw tooMany();
    }
    // A retry of a group already created returns it, whatever the contacts are now.
    if (!created(userId, clientGroupId)) {
      requireContacts(userId, named);
    }
    Uuid conversationId = claim(userId, clientGroupId);
    Map<Uuid, User> profiles = accounts.profiles(invitees);
    Sql.inTransaction(
        db,
        c -> {
          try (PreparedStatement ps =
                  Sql.prepare(
                      c,
                      "SELECT 1 FROM conversation WHERE conversation_id = ? FOR UPDATE",
                      conversationId);
              ResultSet rs = ps.executeQuery()) {
            if (rs.next()) {
              // A retry: created already.
              return null;
            }
          }
          Set<Uuid> members = new TreeSet<>(invitees);
          members.add(userId);
          LocalDateTime now = Timestamps.now();
          Sql.update(
              c,
              "INSERT INTO conversation (conversation_id, kind, topic, members, last_seq,"
                  + " created_at) VALUES (?, ?, ?, ?, 0, ?)",
              conversationId,
              Kinds.GROUP,
              topic,
              Uuid.join(members),
              now);
          List<Object> args = new ArrayList<>();
          for (Uuid member : members) {
            args.addAll(List.of(conversationId, member, member.equals(userId), now));
          }
          Sql.update(
              c,
              "INSERT INTO conversation_member (conversation_id, user_id, admin, joined_seq,"
                  + " left_seq, joined_at) VALUES "
                  + String.join(", ", Collections.nCopies(members.size(), "(?, ?, ?, 1, NULL, ?)")),
              args.toArray());
          GroupCreated.Builder created = GroupCreated.newBuilder().setTopic(topic);
          for (Uuid invitee : invitees) {
            created.addMembers(profiles.get(invitee));
          }
          notice(
              c,
              conversationId,
              new Header(Kinds.GROUP, topic, List.copyOf(members), 0, 0, false, 0),
              userId,
              Notice.newBuilder().setGroupCreated(created).build(),
              members,
              topic,
              members);
          return null;
        });
    Conversation conversation = describe(conversationId, userId);
    try (Connection c = userDb.getConnection()) {
      // The creator's list shows the group at once, before fan-out reaches it.
      Sql.update(
          c,
          "INSERT IGNORE INTO user_conversation (user_id, conversation_id, kind, topic,"
              + " member_count, member, admin, updated_at) VALUES (?, ?, ?, ?, ?, TRUE, TRUE, ?)",
          userId,
          conversationId,
          Kinds.GROUP,
          conversation.getTopic(),
          conversation.getMemberCount(),
          Timestamps.now());
    }
    return conversation;
  }

  /** Whether the creator's client group id names a group already created. */
  private boolean created(Uuid userId, String clientGroupId) throws SQLException {
    try (Connection c = db.getConnection()) {
      Uuid id = claimed(c, userId, clientGroupId);
      if (id == null) {
        return false;
      }
      try (PreparedStatement ps =
              Sql.prepare(c, "SELECT 1 FROM conversation WHERE conversation_id = ?", id);
          ResultSet rs = ps.executeQuery()) {
        return rs.next();
      }
    }
  }

  /** The conversation id claimed for the creator's client group id, claiming a new one if none. */
  private Uuid claim(Uuid userId, String clientGroupId) throws SQLException {
    try (Connection c = db.getConnection()) {
      Uuid id = claimed(c, userId, clientGroupId);
      if (id != null) {
        return id;
      }
      id = Uuid.random();
      try {
        Sql.update(
            c,
            "INSERT INTO group_claim (creator_user_id, client_group_id, conversation_id,"
                + " created_at) VALUES (?, ?, ?, ?)",
            userId,
            clientGroupId,
            id,
            Timestamps.now());
        return id;
      } catch (SQLException e) {
        if (!Sql.isDuplicate(e)) {
          throw e;
        }
        id = claimed(c, userId, clientGroupId);
        if (id == null) {
          throw new SQLException("group claim missing after a duplicate key", e);
        }
        return id;
      }
    }
  }

  private static Uuid claimed(Connection c, Uuid userId, String clientGroupId) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT conversation_id FROM group_claim WHERE creator_user_id = ? AND"
                    + " client_group_id = ?",
                userId,
                clientGroupId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? Uuid.of(rs.getBytes(1)) : null;
    }
  }

  /** The group as the user sees it, from the conversation keyspace. */
  private Conversation describe(Uuid conversationId, Uuid userId) throws SQLException {
    try (Connection c = db.getConnection()) {
      Header h = Rows.header(c, conversationId, false);
      Conversation.Builder conv =
          Conversation.newBuilder()
              .setConversationId(conversationId.toByteString())
              .setKind(ConversationKind.CONVERSATION_KIND_GROUP)
              .setTopic(h.topic())
              .setMemberCount(h.members().size())
              .setLastSeq(h.lastSeq());
      MemberRow row = memberRow(c, conversationId, userId);
      if (row != null) {
        conv.setMember(row.current()).setAdmin(row.current() && row.admin());
      }
      if (h.lastSeq() > 0) {
        try (PreparedStatement ps =
                Sql.prepare(
                    c,
                    "SELECT "
                        + Rows.MESSAGE_COLUMNS
                        + " FROM message WHERE conversation_id = ? AND seq = ?",
                    conversationId,
                    h.lastSeq());
            ResultSet rs = ps.executeQuery()) {
          if (rs.next()) {
            conv.setLastMessage(Rows.message(conversationId, rs));
          } else {
            // Moved to the cold tier, which has it before MySQL lets go. Without the cold tier,
            // the summary goes without it.
            Cold.Row last = null;
            try {
              last = cold.get(conversationId, h.lastSeq());
            } catch (ApiError e) {
              // Logged by the cold tier.
            }
            if (last != null) {
              conv.setLastMessage(last.message(conversationId).toBuilder().clearReactions());
            }
          }
        }
      }
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT seq FROM read_mark WHERE conversation_id = ? AND user_id = ?",
                  conversationId,
                  userId);
          ResultSet rs = ps.executeQuery()) {
        if (rs.next()) {
          conv.setReadSeq(rs.getLong(1));
        }
      }
      return conv.build();
    }
  }

  /**
   * Adds the named users, who must be the user's contacts; those already members are ignored. Any
   * member may.
   */
  public void addMembers(Uuid userId, Uuid conversationId, List<String> names) throws SQLException {
    Map<Uuid, String> named = resolve(userId, names);
    // Those already members are skipped before the contact check; any who left meanwhile are
    // checked under the lock.
    Map<Uuid, String> checked = new LinkedHashMap<>(named);
    try (Connection c = db.getConnection()) {
      Header h = groupHeader(c, conversationId, userId, false);
      checked.keySet().removeIf(h::includes);
    }
    requireContacts(userId, checked);
    List<Uuid> resolved = List.copyOf(named.keySet());
    Map<Uuid, User> profiles = accounts.profiles(resolved);
    Sql.inTransaction(
        db,
        c -> {
          Header h = groupHeader(c, conversationId, userId);
          List<Uuid> added = new ArrayList<>();
          Map<Uuid, String> unchecked = new LinkedHashMap<>();
          for (Uuid id : resolved) {
            if (!h.includes(id)) {
              added.add(id);
              if (!checked.containsKey(id)) {
                unchecked.put(id, named.get(id));
              }
            }
          }
          if (added.isEmpty()) {
            return null;
          }
          requireContacts(userId, unchecked);
          if (h.members().size() + added.size() > Rows.MAX_MEMBERS) {
            throw tooMany();
          }
          long seq = h.lastSeq() + 1;
          LocalDateTime now = Timestamps.now();
          MembersAdded.Builder notice = MembersAdded.newBuilder();
          for (Uuid id : added) {
            Sql.update(
                c,
                "INSERT INTO conversation_member (conversation_id, user_id, admin, joined_seq,"
                    + " left_seq, joined_at) VALUES (?, ?, FALSE, ?, NULL, ?) ON DUPLICATE KEY"
                    + " UPDATE admin = FALSE, joined_seq = ?, left_seq = NULL, joined_at = ?",
                conversationId,
                id,
                seq,
                now,
                seq,
                now);
            notice.addUsers(profiles.get(id));
          }
          Set<Uuid> members = new TreeSet<>(h.members());
          members.addAll(added);
          Rows.requireRotation(c, conversationId);
          notice(
              c,
              conversationId,
              h,
              userId,
              Notice.newBuilder().setMembersAdded(notice).build(),
              members,
              h.topic(),
              members);
          return null;
        });
  }

  /** Removes a member; only admins may, and not themselves. */
  public void removeMember(Uuid userId, Uuid conversationId, Uuid target) throws SQLException {
    User profile = accounts.profiles(List.of(target)).get(target);
    Sql.inTransaction(
        db,
        c -> {
          Header h = groupHeader(c, conversationId, userId);
          requireAdmin(c, conversationId, userId);
          if (target.equals(userId)) {
            throw ApiError.invalidArgument("removing oneself; use LeaveGroup");
          }
          if (!h.includes(target)) {
            throw ApiError.notFound("user " + target + " is not a member");
          }
          markLeft(c, conversationId, target, h.lastSeq() + 1);
          Rows.requireRotation(c, conversationId);
          Set<Uuid> members = new TreeSet<>(h.members());
          members.remove(target);
          notice(
              c,
              conversationId,
              h,
              userId,
              Notice.newBuilder()
                  .setMemberRemoved(MemberRemoved.newBuilder().setUser(profile))
                  .build(),
              members,
              h.topic(),
              new TreeSet<>(h.members()));
          return null;
        });
  }

  private static void markLeft(Connection c, Uuid conversationId, Uuid userId, long seq)
      throws SQLException {
    Sql.update(
        c,
        "UPDATE conversation_member SET left_seq = ?, admin = FALSE WHERE conversation_id = ? AND"
            + " user_id = ?",
        seq,
        conversationId,
        userId);
  }

  /**
   * Leaves the group. When the last admin leaves, the member who joined first becomes one, so a
   * group with members always has an admin.
   */
  public void leave(Uuid userId, Uuid conversationId) throws SQLException {
    Sql.inTransaction(
        db,
        c -> {
          Header h = groupHeader(c, conversationId, userId);
          MemberRow row = memberRow(c, conversationId, userId);
          long seq = h.lastSeq() + 1;
          MemberLeft.Builder left = MemberLeft.newBuilder();
          if (row != null
              && row.admin()
              && h.members().size() > 1
              && otherAdmins(c, conversationId, userId) == 0) {
            Uuid promoted = earliestOther(c, conversationId, userId);
            if (promoted != null) {
              Sql.update(
                  c,
                  "UPDATE conversation_member SET admin = TRUE WHERE conversation_id = ? AND"
                      + " user_id = ?",
                  conversationId,
                  promoted);
              left.setPromoted(accounts.profiles(List.of(promoted)).get(promoted));
            }
          }
          markLeft(c, conversationId, userId, seq);
          Rows.requireRotation(c, conversationId);
          Set<Uuid> members = new TreeSet<>(h.members());
          members.remove(userId);
          notice(
              c,
              conversationId,
              h,
              userId,
              Notice.newBuilder().setMemberLeft(left).build(),
              members,
              h.topic(),
              new TreeSet<>(h.members()));
          return null;
        });
  }

  /** The current member other than the user who joined first. */
  private static Uuid earliestOther(Connection c, Uuid conversationId, Uuid userId)
      throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT user_id FROM conversation_member WHERE conversation_id = ? AND left_seq IS"
                    + " NULL AND user_id <> ? ORDER BY joined_seq, joined_at, user_id LIMIT 1",
                conversationId,
                userId);
        ResultSet rs = ps.executeQuery()) {
      return rs.next() ? Uuid.of(rs.getBytes(1)) : null;
    }
  }

  /** Changes the topic; any member may. The same topic again changes nothing. */
  public void setTopic(Uuid userId, Uuid conversationId, String topic) throws SQLException {
    checkTopic(topic);
    Sql.inTransaction(
        db,
        c -> {
          Header h = groupHeader(c, conversationId, userId);
          if (h.topic().equals(topic)) {
            return null;
          }
          Set<Uuid> members = new TreeSet<>(h.members());
          notice(
              c,
              conversationId,
              h,
              userId,
              Notice.newBuilder()
                  .setTopicChanged(TopicChanged.newBuilder().setTopic(topic))
                  .build(),
              members,
              topic,
              members);
          return null;
        });
  }

  /** Makes a member an admin or no longer one; only admins may, and the last keeps it. */
  public void setAdmin(Uuid userId, Uuid conversationId, Uuid target, boolean admin)
      throws SQLException {
    User profile = accounts.profiles(List.of(target)).get(target);
    Sql.inTransaction(
        db,
        c -> {
          Header h = groupHeader(c, conversationId, userId);
          requireAdmin(c, conversationId, userId);
          if (!h.includes(target)) {
            throw ApiError.notFound("user " + target + " is not a member");
          }
          MemberRow row = memberRow(c, conversationId, target);
          if (row == null) {
            throw new SQLException("member row of " + target + " missing in " + conversationId);
          }
          if (row.admin() == admin) {
            return null;
          }
          if (!admin && otherAdmins(c, conversationId, target) == 0) {
            throw ApiError.failedPrecondition(
                ErrorReason.ERROR_REASON_LAST_ADMIN, "a group keeps at least one admin");
          }
          Sql.update(
              c,
              "UPDATE conversation_member SET admin = ? WHERE conversation_id = ? AND user_id = ?",
              admin,
              conversationId,
              target);
          Set<Uuid> members = new TreeSet<>(h.members());
          notice(
              c,
              conversationId,
              h,
              userId,
              Notice.newBuilder()
                  .setAdminChanged(AdminChanged.newBuilder().setUser(profile).setAdmin(admin))
                  .build(),
              members,
              h.topic(),
              members);
          return null;
        });
  }

  private record MemberEntry(Uuid userId, boolean admin, long joinedSeq) {}

  /** The current members, those who joined first first. */
  public List<Member> members(Uuid userId, Uuid conversationId) throws SQLException {
    List<MemberEntry> entries = new ArrayList<>();
    Map<Uuid, Long> reads = new HashMap<>();
    try (Connection c = db.getConnection()) {
      Header h = Rows.header(c, conversationId, false);
      if (!h.group()) {
        throw ApiError.failedPrecondition(
            ErrorReason.ERROR_REASON_NOT_A_GROUP,
            "conversation " + conversationId + " is not a group");
      }
      Rows.requireMember(h, conversationId, userId);
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT user_id, admin, joined_seq FROM conversation_member WHERE"
                      + " conversation_id = ? AND left_seq IS NULL ORDER BY joined_seq, joined_at,"
                      + " user_id",
                  conversationId);
          ResultSet rs = ps.executeQuery()) {
        while (rs.next()) {
          entries.add(new MemberEntry(Uuid.of(rs.getBytes(1)), rs.getBoolean(2), rs.getLong(3)));
        }
      }
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT user_id, seq FROM read_mark WHERE conversation_id = ?",
                  conversationId);
          ResultSet rs = ps.executeQuery()) {
        while (rs.next()) {
          reads.put(Uuid.of(rs.getBytes(1)), rs.getLong(2));
        }
      }
    }
    Map<Uuid, User> profiles =
        accounts.profiles(entries.stream().map(MemberEntry::userId).toList());
    List<Member> out = new ArrayList<>(entries.size());
    for (MemberEntry e : entries) {
      out.add(
          Member.newBuilder()
              .setUser(profiles.get(e.userId()))
              .setAdmin(e.admin())
              .setJoinedSeq(e.joinedSeq())
              .setReadSeq(reads.getOrDefault(e.userId(), 0L))
              .build());
    }
    return out;
  }

  /**
   * Leaves every group the user's list shows them in, as LeaveGroup would, so admins are promoted
   * and the members told. Used before an account is deleted.
   */
  public void leaveAll(Uuid userId) throws SQLException {
    Uuid after = null;
    while (true) {
      List<Uuid> page = new ArrayList<>();
      try (Connection c = userDb.getConnection();
          PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT conversation_id FROM user_conversation WHERE user_id = ? AND kind = ?"
                      + " AND member AND conversation_id > ? ORDER BY conversation_id LIMIT ?",
                  userId,
                  Kinds.GROUP,
                  after == null ? new byte[Uuid.LENGTH] : after.bytes(),
                  LEAVE_PAGE);
          ResultSet rs = ps.executeQuery()) {
        while (rs.next()) {
          page.add(Uuid.of(rs.getBytes(1)));
        }
      }
      for (Uuid conversationId : page) {
        try {
          leave(userId, conversationId);
        } catch (ApiError e) {
          // Left already, the list not showing it yet, is fine.
          if (e.code() != ErrorCode.ERROR_CODE_PERMISSION_DENIED
              && e.code() != ErrorCode.ERROR_CODE_NOT_FOUND) {
            throw e;
          }
        }
      }
      if (page.size() < LEAVE_PAGE) {
        return;
      }
      after = page.getLast();
    }
  }
}
