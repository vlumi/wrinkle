package wrinkle.stickers;

import com.google.protobuf.ByteString;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import javax.sql.DataSource;
import wrinkle.ApiError;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.accounts.Devices;
import wrinkle.accounts.UserOutbox;
import wrinkle.uploads.UploadService;
import wrinkle.v1.Event;
import wrinkle.v1.PaletteEvent;
import wrinkle.v1.Sticker;

/**
 * Stickers, public and permanent, sharded by id; and each user's palette of saved stickers, on the
 * user's shard. A palette change and its outbox row are one transaction under the user row's lock.
 */
public final class StickerService {
  static final int MAX_PALETTE = 200;
  static final int MAX_GET = 100;

  private final DataSource db;
  private final UploadService uploads;

  public StickerService(DataSource db, UploadService uploads) {
    this.db = db;
    this.uploads = uploads;
  }

  /**
   * Makes the user's uploaded sticker blob a sticker, and saves it to the user's palette unless
   * that is full. The same blob again returns the sticker as it is.
   */
  public Sticker create(Uuid userId, ByteString blob) throws Exception {
    Uuid blobId = Uuid.parse(blob);
    if (blobId == null) {
      throw ApiError.invalidArgument("blob_id must be 16 bytes");
    }
    String mediaType = uploads.checkSticker(userId, blobId);
    Sticker existing = get(blobId);
    if (existing != null) {
      return existing;
    }
    // Used first, so the sweeper never deletes a sticker's blob.
    uploads.useSticker(userId, blobId);
    // As stored, so the response matches later reads.
    LocalDateTime now = Timestamps.now().truncatedTo(ChronoUnit.MICROS);
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          "INSERT INTO sticker (sticker_id, creator_user_id, media_type, created_at) VALUES (?, ?,"
              + " ?, ?)",
          blobId,
          userId,
          mediaType,
          now);
    } catch (SQLException e) {
      if (!Sql.isDuplicate(e)) {
        throw e;
      }
      // Made concurrently.
      return get(blobId);
    }
    save(userId, blobId, false);
    return sticker(blobId, userId, mediaType, now);
  }

  /** The known stickers among the ids, in the order asked. */
  public List<Sticker> get(List<ByteString> ids) throws SQLException {
    if (ids.isEmpty() || ids.size() > MAX_GET) {
      throw ApiError.invalidArgument("sticker_ids must have 1 to " + MAX_GET + " ids");
    }
    List<Uuid> parsed = new ArrayList<>(ids.size());
    for (ByteString b : ids) {
      Uuid id = Uuid.parse(b);
      if (id == null) {
        throw ApiError.invalidArgument("sticker_ids must be 16 bytes each");
      }
      parsed.add(id);
    }
    return read(parsed);
  }

  /** The user's palette in order. */
  public List<Sticker> palette(Uuid userId) throws SQLException {
    List<Uuid> ids = new ArrayList<>();
    try (Connection c = db.getConnection()) {
      ids.addAll(order(c, userId, ""));
    }
    return read(ids);
  }

  /**
   * Adds the sticker at the end of the user's palette; one already saved stays. A full palette
   * fails with FAILED_PRECONDITION.
   */
  public void save(Uuid userId, ByteString stickerId) throws SQLException {
    Uuid id = stickerId(stickerId, "sticker_id");
    if (get(id) == null) {
      throw ApiError.notFound("no such sticker");
    }
    save(userId, id, true);
  }

  /** As save, but a full palette is left as it is unless failWhenFull. */
  private void save(Uuid userId, Uuid stickerId, boolean failWhenFull) throws SQLException {
    Devices.inTransaction(
        db,
        c -> {
          List<Uuid> order = order(c, userId, " FOR UPDATE");
          if (order == null || order.contains(stickerId)) {
            return null;
          }
          if (order.size() >= MAX_PALETTE) {
            if (failWhenFull) {
              throw ApiError.failedPrecondition(
                  "the palette is full, at most " + MAX_PALETTE + " stickers");
            }
            return null;
          }
          LocalDateTime now = Timestamps.now();
          long position;
          try (PreparedStatement ps =
                  Sql.prepare(
                      c,
                      "SELECT COALESCE(MAX(position) + 1, 0) FROM palette_sticker WHERE user_id = ?",
                      userId);
              ResultSet rs = ps.executeQuery()) {
            rs.next();
            position = rs.getLong(1);
          }
          Sql.update(
              c,
              "INSERT INTO palette_sticker (user_id, sticker_id, position, saved_at) VALUES (?, ?,"
                  + " ?, ?)",
              userId,
              stickerId,
              position,
              now);
          order.add(stickerId);
          changed(c, userId, order, now);
          return null;
        });
  }

  /** Takes the sticker off the user's palette, if it is there. */
  public void remove(Uuid userId, ByteString stickerId) throws SQLException {
    Uuid id = stickerId(stickerId, "sticker_id");
    Devices.inTransaction(
        db,
        c -> {
          List<Uuid> order = order(c, userId, " FOR UPDATE");
          if (order == null || !order.remove(id)) {
            return null;
          }
          Sql.update(
              c, "DELETE FROM palette_sticker WHERE user_id = ? AND sticker_id = ?", userId, id);
          changed(c, userId, order, Timestamps.now());
          return null;
        });
  }

  /** Moves a saved sticker before another, or to the end when before is empty. */
  public void move(Uuid userId, ByteString stickerId, ByteString before) throws SQLException {
    Uuid id = stickerId(stickerId, "sticker_id");
    Uuid beforeId = before.isEmpty() ? null : stickerId(before, "before_sticker_id");
    Devices.inTransaction(
        db,
        c -> {
          List<Uuid> order = order(c, userId, " FOR UPDATE");
          if (order == null || !order.contains(id)) {
            throw ApiError.notFound("sticker not in the palette");
          }
          if (beforeId != null && !order.contains(beforeId)) {
            throw ApiError.notFound("before sticker not in the palette");
          }
          List<Uuid> moved = moved(order, id, beforeId);
          if (moved.equals(order)) {
            return null;
          }
          // Positions are renumbered 0..n-1, written only where they change.
          Map<Uuid, Integer> was = new HashMap<>();
          List<Integer> positions = positions(c, userId);
          for (int i = 0; i < order.size(); i++) {
            was.put(order.get(i), positions.get(i));
          }
          for (int i = 0; i < moved.size(); i++) {
            if (was.get(moved.get(i)) != i) {
              Sql.update(
                  c,
                  "UPDATE palette_sticker SET position = ? WHERE user_id = ? AND sticker_id = ?",
                  i,
                  userId,
                  moved.get(i));
            }
          }
          changed(c, userId, moved, Timestamps.now());
          return null;
        });
  }

  /** The order with the sticker moved before another, or to the end when before is null. */
  static List<Uuid> moved(List<Uuid> order, Uuid id, Uuid before) {
    List<Uuid> moved = new ArrayList<>(order);
    if (id.equals(before)) {
      return moved;
    }
    moved.remove(id);
    moved.add(before == null ? moved.size() : moved.indexOf(before), id);
    return moved;
  }

  private static Uuid stickerId(ByteString b, String field) {
    Uuid id = Uuid.parse(b);
    if (id == null) {
      throw ApiError.invalidArgument(field + " must be 16 bytes");
    }
    return id;
  }

  /**
   * The user's palette in order. With a lock, the user row is locked first, serializing the user's
   * palette changes; null then if the account is gone.
   */
  private static List<Uuid> order(Connection c, Uuid userId, String lock) throws SQLException {
    if (!lock.isEmpty()) {
      try (PreparedStatement ps =
              Sql.prepare(
                  c, "SELECT 1 FROM user WHERE user_id = ? AND deleted_at IS NULL" + lock, userId);
          ResultSet rs = ps.executeQuery()) {
        if (!rs.next()) {
          return null;
        }
      }
    }
    List<Uuid> ids = new ArrayList<>();
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT sticker_id FROM palette_sticker WHERE user_id = ? ORDER BY position,"
                    + " saved_at",
                userId);
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        ids.add(Uuid.of(rs.getBytes(1)));
      }
    }
    return ids;
  }

  private static List<Integer> positions(Connection c, Uuid userId) throws SQLException {
    List<Integer> positions = new ArrayList<>();
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT position FROM palette_sticker WHERE user_id = ? ORDER BY position,"
                    + " saved_at",
                userId);
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        positions.add(rs.getInt(1));
      }
    }
    return positions;
  }

  /** Writes the palette as it now is to the user's outbox. */
  private static void changed(Connection c, Uuid userId, List<Uuid> order, LocalDateTime now)
      throws SQLException {
    PaletteEvent.Builder palette = PaletteEvent.newBuilder();
    for (Uuid id : order) {
      palette.addStickerIds(id.toByteString());
    }
    UserOutbox.write(
        c,
        userId,
        "palette",
        Event.newBuilder().setOccurredAt(Timestamps.toProto(now)).setPalette(palette).build());
  }

  private Sticker get(Uuid id) throws SQLException {
    List<Sticker> found = read(List.of(id));
    return found.isEmpty() ? null : found.getFirst();
  }

  /** The known stickers among the ids, in their order. */
  private List<Sticker> read(List<Uuid> ids) throws SQLException {
    if (ids.isEmpty()) {
      return List.of();
    }
    Map<Uuid, Sticker> found = new HashMap<>();
    try (Connection c = db.getConnection();
        PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT sticker_id, creator_user_id, media_type, created_at FROM sticker WHERE"
                    + " sticker_id IN ("
                    + String.join(", ", Collections.nCopies(ids.size(), "?"))
                    + ")",
                ids.toArray());
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        Uuid id = Uuid.of(rs.getBytes(1));
        found.put(
            id,
            sticker(
                id,
                Uuid.of(rs.getBytes(2)),
                rs.getString(3),
                rs.getObject(4, LocalDateTime.class)));
      }
    }
    List<Sticker> stickers = new ArrayList<>(ids.size());
    for (Uuid id : ids) {
      Sticker s = found.get(id);
      if (s != null) {
        stickers.add(s);
      }
    }
    return stickers;
  }

  private static Sticker sticker(Uuid id, Uuid creator, String mediaType, LocalDateTime createdAt) {
    return Sticker.newBuilder()
        .setStickerId(id.toByteString())
        .setCreatorUserId(creator.toByteString())
        .setMediaType(mediaType)
        .setCreatedAt(Timestamps.toProto(createdAt))
        .build();
  }
}
