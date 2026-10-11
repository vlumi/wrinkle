package wrinkle.accounts;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.util.Set;
import javax.sql.DataSource;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.ApiError;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.v1.ChangePasswordRequest;
import wrinkle.v1.ConnectRequest;
import wrinkle.v1.ConnectResponse;
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
  private static final int MAX_DEVICE_NAME_BYTES = 64;
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

  public AccountService(DataSource db) {
    this.db = db;
    this.names = new NameClaims(db);
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
  static boolean whiteSpace(int cp) {
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
    if (utf8Length(name) > MAX_DEVICE_NAME_BYTES) {
      throw ApiError.invalidArgument(
          "device_name must be UTF-8 of at most " + MAX_DEVICE_NAME_BYTES + " bytes");
    }
  }

  /** The user's profile, or null when there is no such user. */
  public User profile(Uuid userId) throws SQLException {
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(c, "SELECT name, icon FROM user WHERE user_id = ?", userId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        return null;
      }
      return User.newBuilder()
          .setUserId(userId.toByteString())
          .setName(rs.getString(1))
          .setIcon(rs.getString(2))
          .build();
    }
  }

  /** The user with the name, ignoring case, or null when no user has it. */
  public Uuid resolve(String name) throws SQLException {
    return names.resolve(name);
  }

  /**
   * Creates an account and a device for it. The user row comes before the name claim, so an
   * interruption leaves at worst a user nobody can reach, never a claim without its user.
   */
  public DeviceCredentials register(RegisterRequest req) throws Exception {
    checkName(req.getName());
    checkPassword(req.getPassword());
    checkDeviceName(req.getDeviceName());
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
  public DeviceCredentials signIn(SignInRequest req) throws Exception {
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
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          "INSERT INTO device (user_id, device_id, name, token_hash, acked_position, created_at,"
              + " last_seen_at) VALUES (?, ?, ?, ?, 0, ?, ?)",
          userId,
          token.deviceId(),
          name,
          token.hash(),
          now,
          now);
    }
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
                  c, "SELECT name, icon, log_head FROM user WHERE user_id = ?", token.userId());
          ResultSet rs = ps.executeQuery()) {
        if (!rs.next()) {
          throw invalidToken();
        }
        resp.setUser(
                User.newBuilder()
                    .setUserId(token.userId().toByteString())
                    .setName(rs.getString(1))
                    .setIcon(rs.getString(2)))
            .setLogHead(rs.getLong(3));
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
   * Replaces the password and signs out every other device of the user. Their connections already
   * open keep running until they reconnect.
   */
  public void changePassword(Uuid userId, long deviceId, ChangePasswordRequest req)
      throws Exception {
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
    String current = req.getCurrentPassword();
    if (!validPassword(current)) {
      current = "";
      hash = Passwords.dummy();
    }
    if (!Passwords.verify(current, hash) || !validPassword(req.getCurrentPassword())) {
      throw ApiError.unauthenticated("wrong password");
    }
    checkPassword(req.getNewPassword());
    String newHash = Passwords.hash(req.getNewPassword());
    try (Connection c = db.getConnection()) {
      Sql.update(c, "UPDATE user SET password_hash = ? WHERE user_id = ?", newHash, userId);
      Sql.update(
          c,
          "UPDATE device SET token_hash = NULL, push_service = NULL, push_token = NULL WHERE"
              + " user_id = ? AND device_id <> ?",
          userId,
          deviceId);
    }
  }

  /** Invalidates the device's token and removes its push registration. */
  public void signOut(Uuid userId, long deviceId) throws SQLException {
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          "UPDATE device SET token_hash = NULL, push_service = NULL, push_token = NULL WHERE"
              + " user_id = ? AND device_id = ?",
          userId,
          deviceId);
    }
  }

  /**
   * Changes the user's name, icon or both. A new name is claimed first; the change and its outbox
   * row are one transaction on the user's shard; the old claim is released last. An interruption
   * leaves at worst a stale claim, which the next claimant takes over.
   */
  public User updateProfile(Uuid userId, UpdateProfileRequest req) throws SQLException {
    if (!req.hasName() && !req.hasIcon()) {
      return existingProfile(userId);
    }
    if (req.hasName()) {
      checkName(req.getName());
    }
    if (req.hasIcon() && !validIcon(req.getIcon())) {
      throw ApiError.invalidArgument("unknown icon \"" + req.getIcon() + "\"");
    }
    User current = existingProfile(userId);
    if (req.hasName() && !req.getName().equals(current.getName())) {
      names.claim(req.getName(), userId);
    }
    Change change = storeProfile(userId, req);
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
  private Change storeProfile(Uuid userId, UpdateProfileRequest req) throws SQLException {
    try (Connection c = db.getConnection()) {
      c.setAutoCommit(false);
      try {
        Change change = storeProfile(c, userId, req);
        c.commit();
        return change;
      } catch (SQLException | RuntimeException e) {
        c.rollback();
        throw e;
      }
    }
  }

  private static Change storeProfile(Connection c, Uuid userId, UpdateProfileRequest req)
      throws SQLException {
    User.Builder user = User.newBuilder().setUserId(userId.toByteString());
    long outboxId;
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT name, icon, last_outbox_id FROM user WHERE user_id = ? FOR UPDATE",
                userId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw new SQLException("user " + userId + " missing");
      }
      user.setName(rs.getString(1)).setIcon(rs.getString(2));
      outboxId = rs.getLong(3);
    }
    String oldName = user.getName();
    if (req.hasName()) {
      user.setName(req.getName());
    }
    if (req.hasIcon()) {
      user.setIcon(req.getIcon());
    }
    outboxId++;
    LocalDateTime now = Timestamps.now();
    Sql.update(
        c,
        "UPDATE user SET name = ?, icon = ?, last_outbox_id = ? WHERE user_id = ?",
        user.getName(),
        user.getIcon(),
        outboxId,
        userId);
    byte[] payload =
        Event.newBuilder()
            .setOccurredAt(Timestamps.toProto(now))
            .setProfile(ProfileEvent.newBuilder().setUser(user))
            .build()
            .toByteArray();
    Sql.update(
        c,
        "INSERT INTO user_outbox (user_id, outbox_id, kind, payload, created_at) VALUES (?, ?,"
            + " 'profile', ?, ?)",
        userId,
        outboxId,
        payload,
        now);
    // As with the conversation outbox: the binlog keeps the insert.
    Sql.update(c, "DELETE FROM user_outbox WHERE user_id = ? AND outbox_id = ?", userId, outboxId);
    return new Change(oldName, user.build());
  }
}
