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
 * Deletes blobs nothing refers to a day after their upload slot was made, or after a send took them
 * for a message that was never delivered: the upload row first, so a blob is only deleted once
 * nobody can start using it, then the bytes.
 */
public final class UploadSweeper {
  private static final Logger log = LoggerFactory.getLogger(UploadSweeper.class);
  private static final Duration FIRST_RUN = Duration.ofMinutes(1);
  private static final Duration INTERVAL = Duration.ofMinutes(10);
  // How long a slot may go unused, or an attachment wait for its message's delivery.
  private static final Duration UNUSED_FOR = Duration.ofHours(24);
  private static final int SWEEP_PAGE = 500;

  record Upload(Uuid userId, Uuid blobId) {}

  /** Where the upload rows are. */
  interface Store {
    /** Unused uploads made before the cutoff. */
    List<Upload> unused(LocalDateTime cutoff, int limit) throws SQLException;

    /** Uploads still waiting since before the cutoff for their message's delivery. */
    List<Upload> pending(LocalDateTime cutoff, int limit) throws SQLException;

    /** Deletes the row if it is still unused or pending since before the cutoff; true if it did. */
    boolean remove(Upload u, LocalDateTime cutoff) throws SQLException;
  }

  /** Where the bytes are. */
  interface Remover {
    /** Deletes the blob and returns the HTTP status. */
    int delete(Uuid blobId);
  }

  private final Store store;
  private final Remover remover;
  private final ScheduledExecutorService timer =
      Executors.newSingleThreadScheduledExecutor(
          Thread.ofPlatform().name("upload-sweeper").daemon().factory());

  UploadSweeper(Store store, Remover remover) {
    this.store = store;
    this.remover = remover;
  }

  public UploadSweeper(DataSource db, Blobs blobs) {
    this(new DbStore(db), blobs::delete);
  }

  public void start() {
    timer.scheduleWithFixedDelay(
        this::run, FIRST_RUN.toMillis(), INTERVAL.toMillis(), TimeUnit.MILLISECONDS);
  }

  public void stop() {
    timer.shutdownNow();
  }

  private void run() {
    try {
      sweep(Timestamps.now().minus(UNUSED_FOR));
    } catch (SQLException | RuntimeException e) {
      log.warn("upload sweep failed", e);
    }
  }

  /**
   * One batch of the unused uploads made before the cutoff and one of those pending since before
   * it; returns how many blobs went.
   */
  int sweep(LocalDateTime cutoff) throws SQLException {
    return sweep(store.unused(cutoff, SWEEP_PAGE), cutoff)
        + sweep(store.pending(cutoff, SWEEP_PAGE), cutoff);
  }

  private int sweep(List<Upload> page, LocalDateTime cutoff) throws SQLException {
    int deleted = 0;
    for (Upload u : page) {
      // Another sweeper, a profile that started using it or a delivery meanwhile may have got
      // there first.
      if (!store.remove(u, cutoff)) {
        continue;
      }
      int status = remover.delete(u.blobId());
      if (status == 200 || status == 204 || status == 404) {
        deleted++;
      } else {
        log.warn("deleting blob {} returned {}", u.blobId(), status);
      }
    }
    return deleted;
  }

  private record DbStore(DataSource db) implements Store {
    @Override
    public List<Upload> unused(LocalDateTime cutoff, int limit) throws SQLException {
      return page(
          "SELECT user_id, blob_id FROM upload WHERE used = FALSE AND created_at < ? LIMIT ?",
          cutoff,
          limit);
    }

    @Override
    public List<Upload> pending(LocalDateTime cutoff, int limit) throws SQLException {
      return page(
          "SELECT user_id, blob_id FROM upload WHERE pending_since < ? LIMIT ?", cutoff, limit);
    }

    private List<Upload> page(String query, LocalDateTime cutoff, int limit) throws SQLException {
      try (Connection c = db.getConnection();
          PreparedStatement ps = Sql.prepare(c, query, cutoff, limit);
          ResultSet rs = ps.executeQuery()) {
        List<Upload> list = new ArrayList<>();
        while (rs.next()) {
          list.add(new Upload(Uuid.of(rs.getBytes(1)), Uuid.of(rs.getBytes(2))));
        }
        return list;
      }
    }

    @Override
    public boolean remove(Upload u, LocalDateTime cutoff) throws SQLException {
      try (Connection c = db.getConnection()) {
        return Sql.update(
                c,
                "DELETE FROM upload WHERE user_id = ? AND blob_id = ? AND (used = FALSE OR"
                    + " pending_since < ?)",
                u.userId(),
                u.blobId(),
                cutoff)
            == 1;
      }
    }
  }
}
