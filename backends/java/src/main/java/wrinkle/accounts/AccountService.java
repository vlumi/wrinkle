package wrinkle.accounts;

import com.google.protobuf.ByteString;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.Collection;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import javax.sql.DataSource;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.ApiError;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.uploads.UploadService;
import wrinkle.v1.ChangePasswordRequest;
import wrinkle.v1.ConnectRequest;
import wrinkle.v1.ConnectResponse;
import wrinkle.v1.DeleteAccountRequest;
import wrinkle.v1.DeviceCredentials;
import wrinkle.v1.Event;
import wrinkle.v1.ProfileEvent;
import wrinkle.v1.RegisterRequest;
import wrinkle.v1.SignInRequest;
import wrinkle.v1.UpdateProfileRequest;
import wrinkle.v1.User;

/**
 * The account service: registration, sign-in with a password, device tokens, profiles and the name
 * claims that keep names unique, against the user keyspace.
 */
public final class AccountService {
  private static final Logger log = LoggerFactory.getLogger(AccountService.class);
  private static final int MAX_NAME_BYTES = 64;
  private static final int MIN_PASSWORD_BYTES = 8;
  private static final int MAX_PASSWORD_BYTES = 256;
  private static final String WRONG_CREDENTIALS = "wrong name or password";

  // The standard icons; empty is none.
  private static final Set<String> ICONS =
      Set.of(
          "cat", "dog", "fox", "owl", "bear", "frog", "panda", "rabbit", "tiger", "penguin",
          "octopus", "unicorn");

  private final DataSource db;
  private final NameClaims names;
  private final UploadService uploads;

  public AccountService(DataSource db, UploadService uploads) {
    this.db = db;
    this.names = new NameClaims(db);
    this.uploads = uploads;
  }

  private static int utf8Length(String s) {
    return s.getBytes(StandardCharsets.UTF_8).length;
  }

  /**
   * Whether a user name is acceptable: 1 to 64 bytes of UTF-8, no control characters, no white
   * space at either end.
   */
  public static boolean validName(String name) {
    if (name.isEmpty() || utf8Length(name) > MAX_NAME_BYTES) {
      return false;
    }
    if (name.codePoints().anyMatch(cp -> Character.getType(cp) == Character.CONTROL)) {
      return false;
    }
    return !whiteSpace(name.codePointAt(0)) && !whiteSpace(name.codePointBefore(name.length()));
  }

  /** The Unicode White_Space property. */
  public static boolean whiteSpace(int cp) {
    return (cp >= 0x09 && cp <= 0x0d)
        || cp == 0x20
        || cp == 0x85
        || cp == 0xa0
        || cp == 0x1680
        || (cp >= 0x2000 && cp <= 0x200a)
        || cp == 0x2028
        || cp == 0x2029
        || cp == 0x202f
        || cp == 0x205f
        || cp == 0x3000;
  }

  static boolean validPassword(String password) {
    int n = utf8Length(password);
    return n >= MIN_PASSWORD_BYTES && n <= MAX_PASSWORD_BYTES;
  }

  static boolean validIcon(String icon) {
    return icon.isEmpty() || ICONS.contains(icon);
  }

  private static void checkName(String name) {
    if (!validName(name)) {
      throw ApiError.invalidArgument(
          "name must be 1 to "
              + MAX_NAME_BYTES
              + " bytes of UTF-8, without control characters or white space at either end");
    }
  }

  private static void checkPassword(String password) {
    if (!validPassword(password)) {
      throw ApiError.invalidArgument(
          "password must be " + MIN_PASSWORD_BYTES + " to " + MAX_PASSWORD_BYTES + " bytes");
    }
  }

  private static void checkDeviceName(String name) {
    Devices.checkName(name, "device_name");
  }

  /** A profile from its columns; a NULL avatar is none. */
  public static User user(Uuid userId, String name, String icon, byte[] avatar, boolean deleted) {
    User.Builder u = User.newBuilder().setUserId(userId.toByteString()).setName(name).setIcon(icon);
    if (avatar != null) {
      u.setAvatar(ByteString.copyFrom(avatar));
    }
    return u.setDeleted(deleted).build();
  }

  /** The user's profile, or null when there is no such user. */
  public User profile(Uuid userId) throws SQLException {
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT name, icon, avatar, deleted_at IS NOT NULL FROM user WHERE user_id = ?",
                userId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        return null;
      }
      return user(userId, rs.getString(1), rs.getString(2), rs.getBytes(3), rs.getBoolean(4));
    }
  }

  /**
   * The users' profiles, by id, in one query; a user that does not exist is just the id. Deleted
   * accounts show as deleted.
   */
  public Map<Uuid, User> profiles(Collection<Uuid> userIds) throws SQLException {
    Map<Uuid, User> out = new HashMap<>();
    if (userIds.isEmpty()) {
      return out;
    }
    List<Uuid> ids = List.copyOf(new LinkedHashSet<>(userIds));
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT user_id, name, icon, avatar, deleted_at IS NOT NULL FROM user WHERE user_id"
                    + " IN ("
                    + String.join(", ", Collections.nCopies(ids.size(), "?"))
                    + ")",
                ids.toArray());
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        Uuid id = Uuid.of(rs.getBytes(1));
        out.put(id, user(id, rs.getString(2), rs.getString(3), rs.getBytes(4), rs.getBoolean(5)));
      }
    }
    for (Uuid id : ids) {
      out.computeIfAbsent(id, k -> User.newBuilder().setUserId(k.toByteString()).build());
    }
    return out;
  }

  /** The user with the name, ignoring case, or null when no user has it. */
  public Uuid resolve(String name) throws SQLException {
    return names.resolve(name);
  }

  /**
   * Creates an account and a device for it. The user row comes before the name claim, so an
   * interruption leaves at worst a user nobody can reach, never a claim without its user.
   */
  public DeviceCredentials register(RegisterRequest req) throws SQLException {
    checkName(req.getName());
    checkPassword(req.getPassword());
    checkDeviceName(req.getDeviceName());
    // A taken name fails before the hash's cost; the claim below decides.
    if (names.resolve(req.getName()) != null) {
      throw ApiError.alreadyExists("the name is taken");
    }
    String hash = Passwords.hash(req.getPassword());
    Uuid userId = Uuid.random();
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          "INSERT INTO user (user_id, name, icon, password_hash, log_head, last_outbox_id,"
              + " created_at) VALUES (?, ?, '', ?, 0, 0, ?)",
          userId,
          req.getName(),
          hash,
          Timestamps.now());
    }
    try {
      names.claim(req.getName(), userId);
    } catch (SQLException | RuntimeException e) {
      try (Connection c = db.getConnection()) {
        Sql.update(c, "DELETE FROM user WHERE user_id = ?", userId);
      } catch (SQLException d) {
        e.addSuppressed(d);
      }
      throw e;
    }
    return newDevice(userId, req.getDeviceName());
  }

  /**
   * Checks the password and creates a device. A wrong name and a wrong password fail alike, in
   * about the same time.
   */
  public DeviceCredentials signIn(SignInRequest req) throws SQLException {
    checkDeviceName(req.getDeviceName());
    Credentials found = passwordOf(req.getName());
    String password = req.getPassword();
    String hash = found == null ? null : found.hash();
    if (hash == null || !validPassword(password)) {
      hash = Passwords.dummy();
      password = "";
    }
    boolean ok = Passwords.verify(password, hash);
    if (!ok || found == null || !validPassword(req.getPassword())) {
      throw ApiError.unauthenticated(WRONG_CREDENTIALS);
    }
    return newDevice(found.userId(), req.getDeviceName());
  }

  private record Credentials(Uuid userId, String hash) {}

  /** The user holding a valid claim on the name and their password hash, or null. */
  private Credentials passwordOf(String name) throws SQLException {
    if (!validName(name)) {
      return null;
    }
    try (Connection c = db.getConnection()) {
      Uuid owner = names.owner(c, name);
      if (owner == null) {
        return null;
      }
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT password_hash FROM user WHERE user_id = ? AND name = ? COLLATE"
                      + " utf8mb4_0900_as_ci",
                  owner,
                  name);
          ResultSet rs = ps.executeQuery()) {
        return rs.next() ? new Credentials(owner, rs.getString(1)) : null;
      }
    }
  }

  private DeviceCredentials newDevice(Uuid userId, String name) throws SQLException {
    DeviceToken token = DeviceToken.create(userId);
    LocalDateTime now = Timestamps.now();
    Sql.inTransaction(
        db,
        c -> {
          Sql.update(
              c,
              "INSERT INTO device (user_id, device_id, name, token_hash, acked_position,"
                  + " created_at, last_seen_at) VALUES (?, ?, ?, ?, 0, ?, ?)",
              userId,
              token.deviceId(),
              name,
              token.hash(),
              now,
              now);
          UserOutbox.device(c, userId, token.deviceId(), false);
          return null;
        });
    return DeviceCredentials.newBuilder()
        .setUserId(userId.toByteString())
        .setDeviceId(token.deviceId())
        .setDeviceToken(token.toString())
        .build();
  }

  private static ApiError invalidToken() {
    return ApiError.unauthenticated("invalid device token");
  }

  /** Checks the device token and returns the user, the device's acked cursor and the log head. */
  public ConnectResponse connect(ConnectRequest req) throws SQLException {
    DeviceToken token = DeviceToken.parse(req.getDeviceToken());
    if (token == null) {
      throw invalidToken();
    }
    ConnectResponse.Builder resp = ConnectResponse.newBuilder().setDeviceId(token.deviceId());
    try (Connection c = db.getConnection()) {
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT token_hash, acked_position FROM device WHERE user_id = ? AND device_id"
                      + " = ?",
                  token.userId(),
                  token.deviceId());
          ResultSet rs = ps.executeQuery()) {
        if (!rs.next()) {
          throw invalidToken();
        }
        byte[] stored = rs.getBytes(1);
        if (stored == null || !MessageDigest.isEqual(stored, token.hash())) {
          throw invalidToken();
        }
        resp.setDeviceCursor(rs.getLong(2));
      }
      try (PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT name, icon, avatar, log_head, deleted_at IS NOT NULL FROM user WHERE"
                      + " user_id = ?",
                  token.userId());
          ResultSet rs = ps.executeQuery()) {
        if (!rs.next() || rs.getBoolean(5)) {
          throw invalidToken();
        }
        resp.setUser(user(token.userId(), rs.getString(1), rs.getString(2), rs.getBytes(3), false))
            .setLogHead(rs.getLong(4));
      }
      Sql.update(
          c,
          "UPDATE device SET last_seen_at = ? WHERE user_id = ? AND device_id = ?",
          Timestamps.now(),
          token.userId(),
          token.deviceId());
    }
    return resp.build();
  }

  /**
   * Fails with UNAUTHENTICATED unless the password is the user's, in about the same time; returns
   * the hash it was checked against.
   */
  private String verifyPassword(Uuid userId, String password) throws SQLException {
    String hash;
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(c, "SELECT password_hash FROM user WHERE user_id = ?", userId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw new SQLException("user " + userId + " missing");
      }
      hash = rs.getString(1);
    }
    // A deleted account has no hash.
    boolean usable = validPassword(password) && !hash.isEmpty();
    if (!Passwords.verify(usable ? password : "", usable ? hash : Passwords.dummy()) || !usable) {
      throw ApiError.unauthenticated("wrong password");
    }
    return hash;
  }

  /**
   * Locks the user row; UNAUTHENTICATED if the password changed, or the account was deleted, since
   * it was verified.
   */
  private static void lockUnchanged(Connection c, Uuid userId, String verified)
      throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(c, "SELECT password_hash FROM user WHERE user_id = ? FOR UPDATE", userId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw new SQLException("user " + userId + " missing");
      }
      if (!rs.getString(1).equals(verified)) {
        throw ApiError.unauthenticated("wrong password");
      }
    }
  }

  /**
   * Replaces the password and signs out every other device of the user, whose connections the
   * gateways close when the sign-out is pushed.
   */
  public void changePassword(Uuid userId, long deviceId, ChangePasswordRequest req)
      throws SQLException {
    String verified = verifyPassword(userId, req.getCurrentPassword());
    checkPassword(req.getNewPassword());
    String newHash = Passwords.hash(req.getNewPassword());
    Sql.inTransaction(
        db,
        c -> {
          lockUnchanged(c, userId, verified);
          Sql.update(c, "UPDATE user SET password_hash = ? WHERE user_id = ?", newHash, userId);
          for (long other : signedIn(c, userId)) {
            if (other != deviceId) {
              Devices.signOut(c, userId, other);
            }
          }
          return null;
        });
  }

  /** The user's signed-in devices, locked for the transaction. */
  private static List<Long> signedIn(Connection c, Uuid userId) throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT device_id FROM device WHERE user_id = ? AND token_hash IS NOT NULL FOR"
                    + " UPDATE",
                userId);
        ResultSet rs = ps.executeQuery()) {
      List<Long> ids = new ArrayList<>();
      while (rs.next()) {
        ids.add(rs.getLong(1));
      }
      return ids;
    }
  }

  /** What deleting an account does first, once the password is checked: leaving its groups. */
  @FunctionalInterface
  public interface BeforeDelete {
    void run() throws SQLException;
  }

  /**
   * Deletes the account after checking its password. The user row stays as a tombstone with the
   * profile cleared; every device is signed out; fan-out tells the partners, closes the connections
   * and removes the user's data. The name is free once the transaction commits, since a claim is
   * valid only while the user's name matches it.
   */
  public void deleteAccount(Uuid userId, DeleteAccountRequest req, BeforeDelete beforeDelete)
      throws SQLException {
    String verified = verifyPassword(userId, req.getPassword());
    beforeDelete.run();
    String oldName =
        Sql.inTransaction(
            db,
            c -> {
              lockUnchanged(c, userId, verified);
              String name;
              try (PreparedStatement ps =
                      Sql.prepare(c, "SELECT name FROM user WHERE user_id = ? FOR UPDATE", userId);
                  ResultSet rs = ps.executeQuery()) {
                if (!rs.next()) {
                  throw new SQLException("user " + userId + " missing");
                }
                name = rs.getString(1);
              }
              LocalDateTime now = Timestamps.now();
              Sql.update(
                  c,
                  "UPDATE user SET name = '', icon = '', avatar = NULL, password_hash = '',"
                      + " deleted_at = ? WHERE user_id = ?",
                  now,
                  userId);
              Sql.update(
                  c,
                  "UPDATE device SET token_hash = NULL, push_service = NULL, push_token = NULL"
                      + " WHERE user_id = ?",
                  userId);
              UserOutbox.write(
                  c,
                  userId,
                  "profile",
                  Event.newBuilder()
                      .setOccurredAt(Timestamps.toProto(now))
                      .setProfile(
                          ProfileEvent.newBuilder()
                              .setUser(
                                  User.newBuilder()
                                      .setUserId(userId.toByteString())
                                      .setDeleted(true)))
                      .build());
              return name;
            });
    if (!oldName.isEmpty()) {
      try (Connection c = db.getConnection()) {
        Sql.update(c, "DELETE FROM user_name WHERE name = ? AND user_id = ?", oldName, userId);
      } catch (SQLException e) {
        // Stale anyway: the user's name no longer matches it.
        log.warn("releasing the name of deleted user {} failed: {}", userId, e.toString());
      }
    }
  }

  /**
   * Changes the user's name, icon, avatar or several. A new name is claimed first; the change and
   * its outbox row are one transaction on the user's shard; the old claim is released last. An
   * interruption leaves at worst a stale claim, which the next claimant takes over.
   */
  public User updateProfile(Uuid userId, UpdateProfileRequest req) throws SQLException {
    if (!req.hasName() && !req.hasIcon() && !req.hasAvatar()) {
      return existingProfile(userId);
    }
    if (req.hasName()) {
      checkName(req.getName());
    }
    if (req.hasIcon() && !validIcon(req.getIcon())) {
      throw ApiError.invalidArgument("unknown icon \"" + req.getIcon() + "\"");
    }
    Uuid avatar = null;
    if (req.hasAvatar() && !req.getAvatar().isEmpty()) {
      avatar = uploads.checkAvatar(userId, req.getAvatar());
    }
    User current = existingProfile(userId);
    if (current.getDeleted()) {
      throw ApiError.unauthenticated("account deleted");
    }
    if (req.hasName() && !req.getName().equals(current.getName())) {
      names.claim(req.getName(), userId);
    }
    Change change = storeProfile(userId, req, avatar);
    User user = change.user();
    if (!user.getName().equals(change.oldName())) {
      // The change is committed; a claim left behind is stale and is taken over by the next
      // claimant.
      try {
        names.release(userId, change.oldName(), user.getName());
      } catch (SQLException e) {
        log.warn("releasing the old name of user {} failed: {}", userId, e.toString());
      }
    }
    return user;
  }

  private User existingProfile(Uuid userId) throws SQLException {
    User user = profile(userId);
    if (user == null) {
      throw new SQLException("user " + userId + " missing");
    }
    return user;
  }

  private record Change(String oldName, User user) {}

  /**
   * Applies the change and writes its outbox row in one transaction, returning the name before and
   * the profile after.
   */
  private Change storeProfile(Uuid userId, UpdateProfileRequest req, Uuid avatar)
      throws SQLException {
    return Sql.inTransaction(db, c -> storeProfile(c, userId, req, avatar));
  }

  /**
   * The avatar's upload becomes used, and the one it replaces unused again, which the sweeper then
   * deletes.
   */
  private static Change storeProfile(
      Connection c, Uuid userId, UpdateProfileRequest req, Uuid avatar) throws SQLException {
    User.Builder user = User.newBuilder().setUserId(userId.toByteString());
    Uuid oldAvatar;
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT name, icon, avatar, deleted_at IS NOT NULL FROM user WHERE user_id = ? FOR"
                    + " UPDATE",
                userId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw new SQLException("user " + userId + " missing");
      }
      if (rs.getBoolean(4)) {
        throw ApiError.unauthenticated("account deleted");
      }
      user.setName(rs.getString(1)).setIcon(rs.getString(2));
      oldAvatar = Uuid.parse(rs.getBytes(3));
    }
    String oldName = user.getName();
    if (req.hasName()) {
      user.setName(req.getName());
    }
    if (req.hasIcon()) {
      user.setIcon(req.getIcon());
    }
    Uuid newAvatar = req.hasAvatar() ? avatar : oldAvatar;
    if (newAvatar != null) {
      user.setAvatar(newAvatar.toByteString());
    }
    Sql.update(
        c,
        "UPDATE user SET name = ?, icon = ?, avatar = ? WHERE user_id = ?",
        user.getName(),
        user.getIcon(),
        newAvatar == null ? null : newAvatar.bytes(),
        userId);
    if (req.hasAvatar()) {
      if (newAvatar != null) {
        UploadService.lockAvatar(c, userId, newAvatar);
        Sql.update(
            c,
            "UPDATE upload SET used = TRUE WHERE user_id = ? AND blob_id = ?",
            userId,
            newAvatar);
      }
      if (oldAvatar != null && !oldAvatar.equals(newAvatar)) {
        Sql.update(
            c,
            "UPDATE upload SET used = FALSE WHERE user_id = ? AND blob_id = ?",
            userId,
            oldAvatar);
      }
    }
    UserOutbox.write(
        c,
        userId,
        "profile",
        Event.newBuilder()
            .setOccurredAt(Timestamps.toProto(Timestamps.now()))
            .setProfile(ProfileEvent.newBuilder().setUser(user))
            .build());
    return new Change(oldName, user.build());
  }
}
