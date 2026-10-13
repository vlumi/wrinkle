package wrinkle.uploads;

import com.google.protobuf.ByteString;
import java.nio.charset.StandardCharsets;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.Instant;
import java.time.LocalDateTime;
import java.time.ZoneOffset;
import java.util.Collections;
import java.util.List;
import java.util.Set;
import java.util.regex.Pattern;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.v1.CreateUploadRequest;
import wrinkle.v1.CreateUploadResponse;

/** Upload slots for blobs, and the checks that a blob was uploaded by the user who refers to it. */
public final class UploadService {
  private static final long EXPIRES_SECONDS = 900;
  private static final int MAX_MEDIA_TYPE_BYTES = 100;
  // type/subtype, ASCII without spaces or controls.
  private static final Pattern MEDIA_TYPE = Pattern.compile("[!-.0-~]+/[!-.0-~]+");

  private record Limits(String purpose, Set<String> mediaTypes, long maxSize) {}

  private static final Limits AVATAR =
      new Limits("avatar", Set.of("image/jpeg", "image/png", "image/webp"), 512 * 1024);
  private static final Limits STICKER =
      new Limits("sticker", Set.of("image/png", "image/webp", "image/gif"), 256 * 1024);
  // Any media type.
  private static final Limits ATTACHMENT = new Limits("attachment", null, 25L * 1024 * 1024);

  private final DataSource db;
  private final Blobs blobs;

  public UploadService(DataSource db, Blobs blobs) {
    this.db = db;
    this.blobs = blobs;
  }

  /** Records a new upload slot and returns where to PUT the bytes. */
  public CreateUploadResponse create(Uuid userId, CreateUploadRequest req) throws SQLException {
    Limits limits =
        switch (req.getPurpose()) {
          case BLOB_PURPOSE_AVATAR -> AVATAR;
          case BLOB_PURPOSE_STICKER -> STICKER;
          case BLOB_PURPOSE_ATTACHMENT -> ATTACHMENT;
          default -> throw ApiError.invalidArgument("unknown purpose " + req.getPurpose());
        };
    String mediaType = req.getMediaType();
    if (limits.mediaTypes() == null
        ? !validMediaType(mediaType)
        : !limits.mediaTypes().contains(mediaType)) {
      throw ApiError.invalidArgument(
          "media type \"" + mediaType + "\" not allowed for " + limits.purpose());
    }
    // size is unsigned; a huge one reads as negative.
    if (req.getSize() < 1 || req.getSize() > limits.maxSize()) {
      throw ApiError.invalidArgument(
          "size must be 1 to " + limits.maxSize() + " bytes for " + limits.purpose());
    }
    Uuid blobId = Uuid.random();
    LocalDateTime now = Timestamps.now();
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          "INSERT INTO upload (user_id, blob_id, purpose, media_type, size, used, created_at)"
              + " VALUES (?, ?, ?, ?, ?, FALSE, ?)",
          userId,
          blobId,
          limits.purpose(),
          mediaType,
          req.getSize(),
          now);
    }
    Instant t = now.toInstant(ZoneOffset.UTC);
    return CreateUploadResponse.newBuilder()
        .setBlobId(blobId.toByteString())
        .setUploadPath(blobs.presignPut(blobId, mediaType, req.getSize(), t, EXPIRES_SECONDS))
        .setExpiresAt(Timestamps.toProto(now.plusSeconds(EXPIRES_SECONDS)))
        .build();
  }

  static boolean validMediaType(String s) {
    return !s.isEmpty()
        && s.getBytes(StandardCharsets.UTF_8).length <= MAX_MEDIA_TYPE_BYTES
        && MEDIA_TYPE.matcher(s).matches();
  }

  /**
   * The avatar a profile may refer to: an avatar upload of the user's, already uploaded. Fails with
   * INVALID_ARGUMENT for a malformed id, FAILED_PRECONDITION otherwise.
   */
  public Uuid checkAvatar(Uuid userId, ByteString avatar) throws SQLException {
    Uuid blobId = Uuid.parse(avatar);
    if (blobId == null) {
      throw ApiError.invalidArgument("avatar must be 16 bytes or empty");
    }
    try (Connection c = db.getConnection()) {
      checkAvatarRow(c, userId, blobId, "");
    }
    if (!blobs.exists(blobId)) {
      throw ApiError.failedPrecondition("not uploaded");
    }
    return blobId;
  }

  /**
   * Locks the avatar's upload row in the profile transaction, so the sweeper cannot delete it
   * meanwhile; FAILED_PRECONDITION if it is gone.
   */
  public static void lockAvatar(Connection c, Uuid userId, Uuid blobId) throws SQLException {
    checkAvatarRow(c, userId, blobId, " FOR UPDATE");
  }

  private static void checkAvatarRow(Connection c, Uuid userId, Uuid blobId, String lock)
      throws SQLException {
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT 1 FROM upload WHERE user_id = ? AND blob_id = ? AND purpose = 'avatar'"
                    + lock,
                userId,
                blobId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw ApiError.failedPrecondition("not an avatar upload of yours");
      }
    }
  }

  /**
   * The media type of the user's sticker upload, already uploaded. Fails with FAILED_PRECONDITION
   * otherwise.
   */
  public String checkSticker(Uuid userId, Uuid blobId) throws SQLException {
    String mediaType;
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT media_type FROM upload WHERE user_id = ? AND blob_id = ? AND purpose ="
                    + " 'sticker'",
                userId,
                blobId);
        ResultSet rs = ps.executeQuery()) {
      if (!rs.next()) {
        throw ApiError.failedPrecondition("not a sticker upload of yours");
      }
      mediaType = rs.getString(1);
    }
    if (!blobs.exists(blobId)) {
      throw ApiError.failedPrecondition("not uploaded");
    }
    return mediaType;
  }

  /**
   * Marks the sticker upload used, for good, so the sweeper leaves it; FAILED_PRECONDITION if the
   * sweeper took it first.
   */
  public void useSticker(Uuid userId, Uuid blobId) throws SQLException {
    Sql.inTransaction(
        db,
        c -> {
          try (PreparedStatement ps =
                  Sql.prepare(
                      c,
                      "SELECT 1 FROM upload WHERE user_id = ? AND blob_id = ? AND purpose ="
                          + " 'sticker' FOR UPDATE",
                      userId,
                      blobId);
              ResultSet rs = ps.executeQuery()) {
            if (!rs.next()) {
              throw ApiError.failedPrecondition("not a sticker upload of yours");
            }
          }
          Sql.update(
              c, "UPDATE upload SET used = TRUE WHERE user_id = ? AND blob_id = ?", userId, blobId);
          return null;
        });
  }

  /**
   * Marks the user's attachment uploads used, once each is found to be theirs, uploaded and not
   * attached to another message; fails with FAILED_PRECONDITION otherwise.
   */
  public void useAttachments(Uuid userId, List<Uuid> blobIds) throws SQLException {
    if (blobIds.isEmpty()) {
      return;
    }
    String query =
        "SELECT used FROM upload WHERE user_id = ? AND purpose = 'attachment' AND blob_id IN "
            + inList(blobIds.size());
    Object[] args = idArgs(userId, blobIds);
    try (Connection c = db.getConnection()) {
      requireAll(c, query, args, blobIds.size());
    }
    for (Uuid blobId : blobIds) {
      if (!blobs.exists(blobId)) {
        throw ApiError.failedPrecondition("not uploaded");
      }
    }
    Sql.inTransaction(
        db,
        c -> {
          // Locking the rows again keeps the sweeper from taking them meanwhile. A used one is
          // another message's, and deleting either would release it.
          if (requireAll(c, query + " FOR UPDATE", args, blobIds.size())) {
            throw ApiError.failedPrecondition("already attached");
          }
          Sql.update(
              c,
              "UPDATE upload SET used = TRUE WHERE user_id = ? AND blob_id IN "
                  + inList(blobIds.size()),
              args);
          return null;
        });
  }

  /** Whether any of the uploads the query finds is used; FAILED_PRECONDITION unless all exist. */
  private static boolean requireAll(Connection c, String query, Object[] args, int want)
      throws SQLException {
    int found = 0;
    boolean used = false;
    try (PreparedStatement ps = Sql.prepare(c, query, args);
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        found++;
        used |= rs.getBoolean(1);
      }
    }
    if (found != want) {
      throw ApiError.failedPrecondition("not an attachment upload of yours");
    }
    return used;
  }

  /**
   * Marks the user's attachment uploads unused again, once the message referring to them is gone,
   * so the sweeper deletes them.
   */
  public void releaseAttachments(Uuid userId, List<Uuid> blobIds) throws SQLException {
    if (blobIds.isEmpty()) {
      return;
    }
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          "UPDATE upload SET used = FALSE WHERE user_id = ? AND purpose = 'attachment' AND"
              + " blob_id IN "
              + inList(blobIds.size()),
          idArgs(userId, blobIds));
    }
  }

  private static String inList(int n) {
    return "(" + String.join(", ", Collections.nCopies(n, "?")) + ")";
  }

  private static Object[] idArgs(Uuid userId, List<Uuid> blobIds) {
    Object[] args = new Object[blobIds.size() + 1];
    args[0] = userId;
    for (int i = 0; i < blobIds.size(); i++) {
      args[i + 1] = blobIds.get(i);
    }
    return args;
  }
}
