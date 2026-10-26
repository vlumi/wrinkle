package wrinkle.fanout;

import com.google.protobuf.ByteString;
import java.sql.Connection;
import java.sql.SQLException;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import javax.sql.DataSource;
import wrinkle.Sql;
import wrinkle.Uuid;
import wrinkle.v1.Message;

/**
 * Ends the wait of a delivered message's attachment uploads, which the sweeper would otherwise take
 * for a failed send's, and releases a deleted message's, for the sweeper to delete. Both are
 * idempotent, so a record delivered again changes nothing.
 */
final class UploadSettler {
  private final DataSource db;

  UploadSettler(DataSource db) {
    this.db = db;
  }

  void settle(OutboxRecord o) throws SQLException {
    Message msg = o.event().getMessage().getMessage();
    if (msg.getBlobIdsCount() > 0) {
      Uuid sender = Uuid.parse(msg.getSenderUserId());
      if (sender == null) {
        return;
      }
      List<Uuid> blobIds = new ArrayList<>(msg.getBlobIdsCount());
      for (ByteString b : msg.getBlobIdsList()) {
        Uuid id = Uuid.parse(b);
        if (id != null) {
          blobIds.add(id);
        }
      }
      update("UPDATE upload SET pending_since = NULL", sender, blobIds);
      return;
    }
    if (!o.releasedUploads().isEmpty()) {
      update(
          "UPDATE upload SET used = FALSE, pending_since = NULL",
          o.releasedBy(),
          o.releasedUploads());
    }
  }

  private void update(String update, Uuid userId, List<Uuid> blobIds) throws SQLException {
    if (blobIds.isEmpty()) {
      return;
    }
    Object[] args = new Object[blobIds.size() + 1];
    args[0] = userId;
    for (int i = 0; i < blobIds.size(); i++) {
      args[i + 1] = blobIds.get(i);
    }
    try (Connection c = db.getConnection()) {
      Sql.update(
          c,
          update
              + " WHERE user_id = ? AND purpose = 'attachment' AND blob_id IN ("
              + String.join(", ", Collections.nCopies(blobIds.size(), "?"))
              + ")",
          args);
    }
  }
}
