package wrinkle.uploads;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.time.Duration;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import wrinkle.Uuid;

class UploadsTest {
  @Test
  void decidesAPendingUploadByItsMessage() {
    LocalDateTime now = LocalDateTime.of(2026, 10, 26, 12, 0);
    Duration hot = Duration.ofDays(30);
    assertEquals(
        UploadSweeper.Verdict.KEEP,
        UploadSweeper.verdict(true, now.minusDays(2), now, hot),
        "delivered late");
    assertEquals(
        UploadSweeper.Verdict.DROP,
        UploadSweeper.verdict(false, now.minusDays(2), now, hot),
        "failed send");
    // Its message may be in the cold tier by now.
    assertEquals(
        UploadSweeper.Verdict.KEEP,
        UploadSweeper.verdict(false, now.minusDays(20), now, hot),
        "older than half the hot window");
    assertEquals(
        UploadSweeper.Verdict.KEEP,
        UploadSweeper.verdict(true, now.minusDays(40), now, hot),
        "sent, long ago");
  }

  @Test
  void sweepsOnlyTheRowsItRemoved() throws Exception {
    Uuid user = Uuid.random();
    UploadSweeper.Upload gone = new UploadSweeper.Upload(user, Uuid.random());
    UploadSweeper.Upload taken = new UploadSweeper.Upload(user, Uuid.random());
    UploadSweeper.Upload missing = new UploadSweeper.Upload(user, Uuid.random());
    UploadSweeper.Upload failing = new UploadSweeper.Upload(user, Uuid.random());
    LocalDateTime now = LocalDateTime.of(2026, 10, 26, 12, 0);
    UploadSweeper.Pending failed =
        new UploadSweeper.Pending(user, Uuid.random(), Uuid.random(), "m1", now.minusDays(2));
    UploadSweeper.Pending delivered =
        new UploadSweeper.Pending(user, Uuid.random(), Uuid.random(), "m2", now.minusDays(2));
    Map<Uuid, Integer> statuses =
        new HashMap<>(
            Map.of(
                gone.blobId(),
                200,
                missing.blobId(),
                404,
                failing.blobId(),
                403,
                failed.blobId(),
                204));
    List<Uuid> deleted = new ArrayList<>();
    List<UploadSweeper.Pending> settled = new ArrayList<>();
    UploadSweeper.Store store =
        new UploadSweeper.Store() {
          @Override
          public List<UploadSweeper.Upload> unused(LocalDateTime cutoff, int limit) {
            return List.of(gone, taken, missing, failing);
          }

          @Override
          public List<UploadSweeper.Pending> pending(LocalDateTime cutoff, int limit) {
            return List.of(failed, delivered);
          }

          @Override
          public boolean sent(UploadSweeper.Pending p) {
            return p.equals(delivered);
          }

          @Override
          public boolean removeUnused(UploadSweeper.Upload u) {
            // Used again since it was selected.
            return !u.equals(taken);
          }

          @Override
          public boolean removePending(UploadSweeper.Pending p) {
            return true;
          }

          @Override
          public void settle(UploadSweeper.Pending p) {
            settled.add(p);
          }
        };
    UploadSweeper sweeper =
        new UploadSweeper(
            store,
            blobId -> {
              deleted.add(blobId);
              return statuses.get(blobId);
            },
            new UploadSweeper.Settings(
                Duration.ofDays(1), Duration.ofMinutes(10), Duration.ofDays(30)));
    assertEquals(3, sweeper.sweep(now));
    assertEquals(
        List.of(gone.blobId(), missing.blobId(), failing.blobId(), failed.blobId()), deleted);
    assertEquals(List.of(delivered), settled);
  }
}
