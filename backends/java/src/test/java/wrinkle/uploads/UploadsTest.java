package wrinkle.uploads;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import wrinkle.Uuid;

class UploadsTest {
  @Test
  void acceptsMediaTypesOfTypeSlashSubtype() {
    assertTrue(UploadService.validMediaType("application/pdf"));
    assertTrue(UploadService.validMediaType("application/vnd.oasis.opendocument.text"));
    assertTrue(UploadService.validMediaType("a".repeat(50) + "/" + "b".repeat(49)));
    assertFalse(UploadService.validMediaType(""));
    assertFalse(UploadService.validMediaType("text"));
    assertFalse(UploadService.validMediaType("text/"));
    assertFalse(UploadService.validMediaType("/plain"));
    assertFalse(UploadService.validMediaType("text/plain; charset=utf-8"));
    assertFalse(UploadService.validMediaType("a/b/c"));
    assertFalse(UploadService.validMediaType("tëxt/plain"));
    assertFalse(UploadService.validMediaType("a".repeat(50) + "/" + "b".repeat(50)));
  }

  @Test
  void sweepsOnlyTheRowsItRemoved() throws Exception {
    Uuid user = Uuid.random();
    UploadSweeper.Upload gone = new UploadSweeper.Upload(user, Uuid.random());
    UploadSweeper.Upload taken = new UploadSweeper.Upload(user, Uuid.random());
    UploadSweeper.Upload missing = new UploadSweeper.Upload(user, Uuid.random());
    UploadSweeper.Upload failing = new UploadSweeper.Upload(user, Uuid.random());
    Map<Uuid, Integer> statuses =
        new HashMap<>(Map.of(gone.blobId(), 200, missing.blobId(), 404, failing.blobId(), 403));
    List<Uuid> deleted = new ArrayList<>();
    UploadSweeper.Store store =
        new UploadSweeper.Store() {
          @Override
          public List<UploadSweeper.Upload> unused(LocalDateTime createdBefore, int limit) {
            return List.of(gone, taken, missing, failing);
          }

          @Override
          public boolean remove(UploadSweeper.Upload u) {
            // Used again since it was selected.
            return !u.equals(taken);
          }
        };
    UploadSweeper sweeper =
        new UploadSweeper(
            store,
            blobId -> {
              deleted.add(blobId);
              return statuses.get(blobId);
            });
    assertEquals(2, sweeper.sweep(LocalDateTime.now()));
    assertEquals(List.of(gone.blobId(), missing.blobId(), failing.blobId()), deleted);
  }
}
