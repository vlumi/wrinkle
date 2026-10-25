package wrinkle.conversation;

import com.google.protobuf.InvalidProtocolBufferException;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.Duration;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.function.BooleanSupplier;
import javax.sql.DataSource;
import wrinkle.ShardPools;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.v1.Reaction;
import wrinkle.v1.Sealed;

/**
 * Moves messages older than the hot window to the cold tier, conversation by conversation, oldest
 * first on each shard. Any number may run: each move happens under the conversation's row lock.
 */
public final class ColdMover {
  private static final int MOVE_PAGE = 1000;
  // Bounds the messages one transaction moves, and so how long it holds the conversation's row.
  private static final int MOVE_BATCH = 200;
  private static final LocalDateTime BEGINNING = LocalDateTime.of(1, 1, 1, 0, 0);

  private record Aged(Uuid conversationId, LocalDateTime sentAt) {}

  private final DataSource db;
  private final ShardPools shards;
  private final Cold cold;
  private final Duration window;

  /**
   * Finds the oldest messages of each shard of the keyspace through a connection to that shard
   * alone.
   */
  public ColdMover(String addr, String keyspace, DataSource db, Cold cold, Duration window) {
    this.db = db;
    this.cold = cold;
    this.window = window;
    this.shards = new ShardPools(addr, keyspace, db);
  }

  /** The keyspace's shards, each a pool to that shard alone. */
  public List<ShardPools.Shard> shards() {
    return shards.all();
  }

  public void close() {
    shards.close();
  }

  /** Moves the shard's messages older than the hot window, until stopped, and returns how many. */
  public int moveShard(ShardPools.Shard shard, BooleanSupplier stopped) throws SQLException {
    return moveShard(shard.db(), Timestamps.now().minus(window), stopped);
  }

  /**
   * Takes the shard's conversations in the order of their oldest messages, a page at a time, each
   * once. A conversation stops at its first message within the window, so one sent a little out of
   * time order waits for the next round; the pages go on after the last one's time.
   */
  private int moveShard(DataSource shard, LocalDateTime cutoff, BooleanSupplier stopped)
      throws SQLException {
    int total = 0;
    Set<Uuid> done = new HashSet<>();
    LocalDateTime after = BEGINNING;
    while (true) {
      List<Aged> page = new ArrayList<>();
      try (Connection c = shard.getConnection();
          PreparedStatement ps =
              Sql.prepare(
                  c,
                  "SELECT conversation_id, sent_at FROM message WHERE sent_at < ? AND sent_at >= ?"
                      + " ORDER BY sent_at LIMIT ?",
                  cutoff,
                  after,
                  MOVE_PAGE);
          ResultSet rs = ps.executeQuery()) {
        while (rs.next()) {
          page.add(new Aged(Uuid.of(rs.getBytes(1)), rs.getObject(2, LocalDateTime.class)));
        }
      }
      boolean fresh = false;
      for (Aged a : page) {
        if (stopped.getAsBoolean()) {
          return total;
        }
        if (done.add(a.conversationId())) {
          fresh = true;
          total += moveConversation(a.conversationId(), cutoff, stopped);
        }
      }
      if (page.size() < MOVE_PAGE || !fresh) {
        return total;
      }
      after = page.getLast().sentAt();
    }
  }

  /** Moves the conversation's messages older than the cutoff, in seq order, a batch at a time. */
  private int moveConversation(Uuid conversationId, LocalDateTime cutoff, BooleanSupplier stopped)
      throws SQLException {
    int total = 0;
    while (true) {
      int n = moveBatch(conversationId, cutoff);
      total += n;
      if (n < MOVE_BATCH || stopped.getAsBoolean()) {
        return total;
      }
    }
  }

  /**
   * Writes the next batch to the cold tier with the conversation's row locked, then raises the
   * watermark over it and deletes it from MySQL in the same transaction. A failure after the puts
   * leaves copies of rows still in MySQL, which the next move puts again.
   */
  private int moveBatch(Uuid conversationId, LocalDateTime cutoff) throws SQLException {
    return Sql.inTransaction(
        db,
        c -> {
          long coldSeq;
          try (PreparedStatement ps =
                  Sql.prepare(
                      c,
                      "SELECT cold_seq FROM conversation WHERE conversation_id = ? FOR UPDATE",
                      conversationId);
              ResultSet rs = ps.executeQuery()) {
            if (!rs.next()) {
              throw new SQLException("conversation " + conversationId + " missing");
            }
            coldSeq = rs.getLong(1);
          }
          List<Cold.Row> moved = new ArrayList<>();
          try (PreparedStatement ps =
                  Sql.prepare(
                      c,
                      "SELECT seq, sender_user_id, client_message_id, body, sent_at, outbox_id,"
                          + " blob_ids FROM message WHERE conversation_id = ? AND seq > ? ORDER BY"
                          + " seq LIMIT ?",
                      conversationId,
                      coldSeq,
                      MOVE_BATCH);
              ResultSet rs = ps.executeQuery()) {
            while (rs.next()) {
              LocalDateTime sentAt = rs.getObject(5, LocalDateTime.class);
              if (!sentAt.isBefore(cutoff)) {
                break;
              }
              moved.add(
                  new Cold.Row(
                      rs.getLong(1),
                      Uuid.of(rs.getBytes(2)),
                      rs.getString(3),
                      rs.getBytes(4),
                      sentAt,
                      rs.getLong(6),
                      rs.getBytes(7),
                      new byte[0]));
            }
          }
          if (moved.isEmpty()) {
            return 0;
          }
          long upTo = moved.getLast().seq();
          moved = withReactions(c, conversationId, coldSeq, upTo, moved);
          cold.put(conversationId, moved);
          Sql.update(
              c,
              "UPDATE conversation SET cold_seq = ? WHERE conversation_id = ?",
              upTo,
              conversationId);
          Sql.update(
              c,
              "DELETE FROM reaction WHERE conversation_id = ? AND seq > ? AND seq <= ?",
              conversationId,
              coldSeq,
              upTo);
          Sql.update(
              c,
              "DELETE FROM message WHERE conversation_id = ? AND seq > ? AND seq <= ?",
              conversationId,
              coldSeq,
              upTo);
          return moved.size();
        });
  }

  /** The messages, sorted by seq and spanning (from, to], with their reactions in their rows. */
  private static List<Cold.Row> withReactions(
      Connection c, Uuid conversationId, long from, long to, List<Cold.Row> messages)
      throws SQLException {
    Map<Long, List<Reaction>> bySeq = new HashMap<>();
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT seq, user_id, body, updated_at FROM reaction WHERE conversation_id = ? AND"
                    + " seq > ? AND seq <= ? ORDER BY seq, updated_at, user_id",
                conversationId,
                from,
                to);
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        Sealed sealed;
        try {
          sealed = Sealed.parseFrom(rs.getBytes(3));
        } catch (InvalidProtocolBufferException e) {
          throw new IllegalStateException("corrupt reaction body", e);
        }
        bySeq
            .computeIfAbsent(rs.getLong(1), k -> new ArrayList<>())
            .add(
                Reaction.newBuilder()
                    .setUserId(Uuid.of(rs.getBytes(2)).toByteString())
                    .setSealed(sealed)
                    .setUpdatedAt(Timestamps.toProto(rs.getObject(4, LocalDateTime.class)))
                    .build());
      }
    }
    List<Cold.Row> out = new ArrayList<>(messages.size());
    for (Cold.Row m : messages) {
      out.add(m.withReactions(Cold.encodeReactions(bySeq.getOrDefault(m.seq(), List.of()))));
    }
    return out;
  }
}
