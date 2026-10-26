package wrinkle.uploads;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.Duration;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import javax.sql.DataSource;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;

/**
 * Deletes blobs nothing refers to: uploads never used or released, and those a send took for a
 * message it failed to store. The upload row goes first, so a blob is only deleted once nobody can
 * start using it, then the bytes; of several sweepers, one deletes it.
 */
public final class UploadSweeper {
  private static final Logger log = LoggerFactory.getLogger(UploadSweeper.class);
  private static final Duration FIRST_RUN = Duration.ofMinutes(1);
  private static final int SWEEP_PAGE = 500;

  /**
   * How long a slot may go unused, or an attachment wait for its message's delivery, before its
   * blob is deleted, and how often the sweeper looks; hotWindow is how long messages stay in MySQL,
   * where the sweeper finds a pending attachment's message.
   */
  public record Settings(Duration unusedFor, Duration interval, Duration hotWindow) {}

  record Upload(Uuid userId, Uuid blobId) {}

  /**
   * An upload a send took, pending since then; conversationId and clientMessageId are the message
   * it was taken for, or null when that was not recorded.
   */
  record Pending(
      Uuid userId, Uuid blobId, Uuid conversationId, String clientMessageId, LocalDateTime since) {}

  enum Verdict {
    KEEP,
    DROP
  }

  /** Where the upload rows and the messages are. */
  interface Store {
    /** Unused uploads made before the cutoff. */
    List<Upload> unused(LocalDateTime cutoff, int limit) throws SQLException;

    /** Uploads still waiting since before the cutoff for their message's delivery. */
    List<Pending> pending(LocalDateTime cutoff, int limit) throws SQLException;

    /** Whether the conversation keyspace has the message the upload was taken for. */
    boolean sent(Pending p) throws SQLException;

    /** Deletes the row if it is still unused; true if it did. */
    boolean removeUnused(Upload u) throws SQLException;

    /** Deletes the row if it is still pending since the same time; true if it did. */
    boolean removePending(Pending p) throws SQLException;

    /** Ends the row's wait if it is still pending since the same time. */
    void settle(Pending p) throws SQLException;
  }

  /** Where the bytes are. */
  interface Remover {
    /** Deletes the blob and returns the HTTP status. */
    int delete(Uuid blobId);
  }

  private final Store store;
  private final Remover remover;
  private final Settings settings;
  private final ScheduledExecutorService timer =
      Executors.newSingleThreadScheduledExecutor(
          Thread.ofPlatform().name("upload-sweeper").daemon().factory());

  UploadSweeper(Store store, Remover remover, Settings settings) {
    this.store = store;
    this.remover = remover;
    this.settings = settings;
  }

  public UploadSweeper(DataSource db, DataSource conversations, Blobs blobs, Settings settings) {
    this(new DbStore(db, conversations), blobs::delete, settings);
  }

  /** Sweeps a minute after starting, or the interval when shorter, then every interval. */
  public void start() {
    Duration first = FIRST_RUN.compareTo(settings.interval()) < 0 ? FIRST_RUN : settings.interval();
    timer.scheduleWithFixedDelay(
        this::run, first.toMillis(), settings.interval().toMillis(), TimeUnit.MILLISECONDS);
  }

  public void stop() {
    timer.shutdownNow();
  }

  private void run() {
    try {
      int n = sweep(Timestamps.now());
      if (n > 0) {
        log.info("swept {} unused uploads", n);
      }
    } catch (SQLException | RuntimeException e) {
      log.warn("upload sweep failed", e);
    }
  }

  /**
   * Deletes a page of the uploads unused since before the cutoff, settles a page of those pending
   * since before it, deleting those of failed sends, and returns how many blobs went.
   */
  int sweep(LocalDateTime now) throws SQLException {
    LocalDateTime cutoff = now.minus(settings.unusedFor());
    int deleted = 0;
    for (Upload u : store.unused(cutoff, SWEEP_PAGE)) {
      // A profile or a send that started using it, or another sweeper, may have got there first.
      if (store.removeUnused(u) && deleteBlob(u.blobId())) {
        deleted++;
      }
    }
    for (Pending p : store.pending(cutoff, SWEEP_PAGE)) {
      switch (verdict(store.sent(p), p.since(), now, settings.hotWindow())) {
        case KEEP -> store.settle(p);
        case DROP -> {
          if (store.removePending(p) && deleteBlob(p.blobId())) {
            deleted++;
          }
        }
      }
    }
    return deleted;
  }

  /**
   * Decides an attachment upload still pending after the cutoff. Its message, if the send stored
   * one, was sent after the upload was taken, so while that is well within the hot window the
   * message would be in MySQL: not finding it there means the send failed. Older, the message may
   * have moved to the cold tier, so the upload is kept.
   */
  static Verdict verdict(boolean sent, LocalDateTime since, LocalDateTime now, Duration hot) {
    if (!sent && since.isAfter(now.minus(hot.dividedBy(2)))) {
      return Verdict.DROP;
    }
    return Verdict.KEEP;
  }

  private boolean deleteBlob(Uuid blobId) {
    int status = remover.delete(blobId);
    if (status == 200 || status == 204 || status == 404) {
      return true;
    }
    // The row is gone; the blob stays behind unreferenced.
    log.warn("deleting blob {} returned {}", blobId, status);
    return false;
  }

  private record DbStore(DataSource db, DataSource conversations) implements Store {
    @Override
    public List<Upload> unused(LocalDateTime cutoff, int limit) throws SQLException {
      try (Connection c = db.getConnection();
          PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT user_id, blob_id FROM upload WHERE used = FALSE AND created_at < ?"
                      + " LIMIT ?",
                  cutoff,
                  limit);
          ResultSet rs = ps.executeQuery()) {
        List<Upload> list = new ArrayList<>();
        while (rs.next()) {
          list.add(new Upload(Uuid.of(rs.getBytes(1)), Uuid.of(rs.getBytes(2))));
        }
        return list;
      }
    }

    @Override
    public List<Pending> pending(LocalDateTime cutoff, int limit) throws SQLException {
      try (Connection c = db.getConnection();
          PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT user_id, blob_id, conversation_id, client_message_id, pending_since FROM"
                      + " upload WHERE pending_since < ? LIMIT ?",
                  cutoff,
                  limit);
          ResultSet rs = ps.executeQuery()) {
        List<Pending> list = new ArrayList<>();
        while (rs.next()) {
          byte[] conversationId = rs.getBytes(3);
          list.add(
              new Pending(
                  Uuid.of(rs.getBytes(1)),
                  Uuid.of(rs.getBytes(2)),
                  conversationId == null ? null : Uuid.parse(conversationId),
                  rs.getString(4),
                  rs.getObject(5, LocalDateTime.class)));
        }
        return list;
      }
    }

    @Override
    public boolean sent(Pending p) throws SQLException {
      // Uploads taken before the message was recorded on them count as sent.
      if (p.conversationId() == null || p.clientMessageId() == null) {
        return true;
      }
      try (Connection c = conversations.getConnection();
          PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT 1 FROM message WHERE conversation_id = ? AND sender_user_id = ? AND"
                      + " client_message_id = ?",
                  p.conversationId(),
                  p.userId(),
                  p.clientMessageId());
          ResultSet rs = ps.executeQuery()) {
        return rs.next();
      }
    }

    @Override
    public boolean removeUnused(Upload u) throws SQLException {
      try (Connection c = db.getConnection()) {
        return Sql.update(
                c,
                "DELETE FROM upload WHERE user_id = ? AND blob_id = ? AND used = FALSE",
                u.userId(),
                u.blobId())
            == 1;
      }
    }

    @Override
    public boolean removePending(Pending p) throws SQLException {
      try (Connection c = db.getConnection()) {
        return Sql.update(
                c,
                "DELETE FROM upload WHERE user_id = ? AND blob_id = ? AND pending_since = ?",
                p.userId(),
                p.blobId(),
                p.since())
            == 1;
      }
    }

    @Override
    public void settle(Pending p) throws SQLException {
      try (Connection c = db.getConnection()) {
        Sql.update(
            c,
            "UPDATE upload SET pending_since = NULL WHERE user_id = ? AND blob_id = ? AND"
                + " pending_since = ?",
            p.userId(),
            p.blobId(),
            p.since());
      }
    }
  }
}
