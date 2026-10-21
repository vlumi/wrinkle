package wrinkle.fanout;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.CountDownLatch;
import javax.sql.DataSource;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.MessageBody;
import wrinkle.Shards;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.v1.Event;
import wrinkle.v1.Message;
import wrinkle.v1.User;

/**
 * Collects the appends of messages being delivered at the same time and stores them in one
 * transaction per shard of the user keyspace: while one shard's transaction runs, the next batch
 * for it gathers, so batches grow with the load.
 */
final class AppendBatcher {
  private static final Logger log = LoggerFactory.getLogger(AppendBatcher.class);
  // Bounds the appends in one batch transaction.
  static final int MAX_BATCH = 256;

  /** Stores a batch, all or nothing. */
  interface Store {
    void store(List<Pending> batch) throws Exception;
  }

  /** One recipient's copy of a message waiting for its batch. */
  static final class Pending {
    final Uuid userId;
    final OutboxRecord o;
    // In a direct conversation, the peer, and their profile for an entry the message may create.
    final Uuid peerId;
    final User peer;
    // What was stored, null when nothing was (a deleted or unknown user); failed when the batch
    // was not stored. Read after done.
    Event event;
    byte[] payload;
    boolean failed;
    final CountDownLatch done = new CountDownLatch(1);

    Pending(Uuid userId, OutboxRecord o, Uuid peerId, User peer) {
      this.userId = userId;
      this.o = o;
      this.peerId = peerId;
      this.peer = peer;
    }
  }

  private static final class ShardQueue {
    List<Pending> pending = new ArrayList<>();
    boolean running;
  }

  private final Shards shards;
  private final Store store;
  private final Map<String, ShardQueue> queues = new HashMap<>();

  AppendBatcher(Shards shards, Store store) {
    this.shards = shards;
    this.store = store;
  }

  /** Stores batches in the user keyspace behind the pool. */
  AppendBatcher(Shards shards, DataSource db) {
    this(shards, batch -> storeBatch(db, batch));
  }

  /** True when a row's appends can share transactions: a message that is not a notice. */
  static boolean batchable(OutboxRecord o) {
    return o.event().hasMessage()
        && o.event().getMessage().hasMessage()
        && !o.event().getMessage().getMessage().hasNotice();
  }

  /** Queues an append on its user's shard. */
  void add(Pending p) {
    String shard = shards.of(p.userId);
    synchronized (queues) {
      ShardQueue q = queues.computeIfAbsent(shard, s -> new ShardQueue());
      q.pending.add(p);
      if (!q.running) {
        q.running = true;
        Thread.ofVirtual().name("fanout-batch-" + shard).start(() -> run(q));
      }
    }
  }

  /** Stores the shard's queued appends, a batch at a time, until none is left. */
  private void run(ShardQueue q) {
    while (true) {
      List<Pending> batch;
      synchronized (queues) {
        if (q.pending.isEmpty()) {
          q.running = false;
          return;
        }
        int n = Math.min(q.pending.size(), MAX_BATCH);
        batch = new ArrayList<>(q.pending.subList(0, n));
        q.pending = new ArrayList<>(q.pending.subList(n, q.pending.size()));
      }
      boolean failed = false;
      try {
        store.store(batch);
      } catch (Exception e) {
        failed = true;
        log.debug("batch of {} failed, appending one at a time: {}", batch.size(), e.toString());
      }
      for (Pending p : batch) {
        if (failed) {
          p.event = null;
          p.payload = null;
          p.failed = true;
        }
        p.done.countDown();
      }
    }
  }

  /**
   * Numbers each pending copy after its user's head, moving the heads; several copies for one user
   * get consecutive positions, in batch order. Users without a head (deleted or unknown) get
   * nothing. Returns the copies to store, with their events and payloads set.
   */
  static List<Pending> number(List<Pending> batch, Map<Uuid, Long> heads) {
    List<Pending> stored = new ArrayList<>();
    for (Pending p : batch) {
      Long head = heads.get(p.userId);
      if (head == null) {
        continue;
      }
      long cursor = head + 1;
      heads.put(p.userId, cursor);
      p.event = p.o.event().toBuilder().setCursor(cursor).build();
      p.payload = p.event.toByteArray();
      stored.add(p);
    }
    return stored;
  }

  /**
   * Appends each pending message to its recipient's log and list, in one transaction: it locks the
   * users' rows in id order, so batches never wait on each other in a cycle, numbers the events
   * after each log's head, and writes the events, the heads and the list entries a statement each.
   */
  private static void storeBatch(DataSource db, List<Pending> batch) throws SQLException {
    Sql.inTransaction(
        db,
        c -> {
          List<Pending> stored = number(batch, lockHeads(c, batch));
          if (stored.isEmpty()) {
            return null;
          }
          insertEvents(c, stored);
          updateHeads(c, stored);
          upsertEntries(c, stored);
          return null;
        });
  }

  /** The heads of the batch's users that exist and are not deleted, locked for the transaction. */
  private static Map<Uuid, Long> lockHeads(Connection c, List<Pending> batch) throws SQLException {
    LinkedHashSet<Uuid> users = new LinkedHashSet<>();
    for (Pending p : batch) {
      users.add(p.userId);
    }
    Map<Uuid, Long> heads = new HashMap<>();
    try (PreparedStatement ps =
            Sql.prepare(
                c,
                "SELECT user_id, log_head, deleted_at FROM user WHERE user_id IN ("
                    + marks(users.size())
                    + ") ORDER BY user_id FOR UPDATE",
                users.toArray());
        ResultSet rs = ps.executeQuery()) {
      while (rs.next()) {
        if (rs.getObject(3) == null) {
          heads.put(Uuid.of(rs.getBytes(1)), rs.getLong(2));
        }
      }
    }
    return heads;
  }

  private static void insertEvents(Connection c, List<Pending> stored) throws SQLException {
    List<Object> args = new ArrayList<>(stored.size() * 6);
    for (Pending p : stored) {
      args.add(p.userId);
      args.add(p.event.getCursor());
      args.add(p.o.conversationId());
      args.add(p.o.outboxId());
      args.add(p.payload);
      args.add(Timestamps.fromProto(p.event.getOccurredAt()));
    }
    Sql.update(
        c,
        "INSERT INTO user_event (user_id, position, source_id, outbox_id, payload, occurred_at)"
            + " VALUES "
            + rows(stored.size(), 6),
        args.toArray());
  }

  /** Moves each user's head to their last copy's position. */
  private static void updateHeads(Connection c, List<Pending> stored) throws SQLException {
    Map<Uuid, Long> heads = new TreeMap<>();
    for (Pending p : stored) {
      heads.put(p.userId, p.event.getCursor());
    }
    StringBuilder cases = new StringBuilder();
    List<Object> args = new ArrayList<>(heads.size() * 3);
    for (Map.Entry<Uuid, Long> h : heads.entrySet()) {
      cases.append(" WHEN ? THEN ?");
      args.add(h.getKey());
      args.add(h.getValue());
    }
    args.addAll(heads.keySet());
    Sql.update(
        c,
        "UPDATE user SET log_head = CASE user_id"
            + cases
            + " END WHERE user_id IN ("
            + marks(heads.size())
            + ")",
        args.toArray());
  }

  /** The list entries' upserts, as the single path makes them for a message without a notice. */
  private static void upsertEntries(Connection c, List<Pending> stored) throws SQLException {
    List<Object> args = new ArrayList<>(stored.size() * 20);
    for (Pending p : stored) {
      args.addAll(entryRow(p));
    }
    // Assignments run in order, so the IFs compare with last_seq already moved forward.
    Sql.update(
        c,
        "INSERT INTO user_conversation (user_id, conversation_id, kind, peer_user_id, peer_name,"
            + " peer_icon, peer_avatar, peer_deleted, topic, member_count, member, admin,"
            + " last_seq, last_sender_user_id, last_client_message_id, last_body, last_sent_at,"
            + " read_seq, peer_read_seq, updated_at) VALUES "
            + rows(stored.size(), 20)
            + " ON DUPLICATE KEY UPDATE"
            + " last_seq = GREATEST(last_seq, VALUES(last_seq)),"
            + " topic = IF(VALUES(last_seq) >= last_seq, VALUES(topic), topic),"
            + " member_count = IF(VALUES(last_seq) >= last_seq, VALUES(member_count),"
            + " member_count),"
            + " last_sender_user_id = IF(VALUES(last_seq) >= last_seq,"
            + " VALUES(last_sender_user_id), last_sender_user_id),"
            + " last_client_message_id = IF(VALUES(last_seq) >= last_seq,"
            + " VALUES(last_client_message_id), last_client_message_id),"
            + " last_body = IF(VALUES(last_seq) >= last_seq, VALUES(last_body), last_body),"
            + " last_sent_at = IF(VALUES(last_seq) >= last_seq, VALUES(last_sent_at),"
            + " last_sent_at),"
            + " read_seq = GREATEST(read_seq, VALUES(read_seq)),"
            + " updated_at = GREATEST(updated_at, VALUES(updated_at))",
        args.toArray());
  }

  /** The values of the recipient's list entry for the message, in the upsert's column order. */
  static List<Object> entryRow(Pending p) {
    Message msg = p.event.getMessage().getMessage();
    User peer = p.peer;
    List<Object> row = new ArrayList<>(20);
    row.add(p.userId);
    row.add(p.o.conversationId());
    row.add(p.o.conversationKind());
    row.add(p.peerId);
    row.add(peer == null ? "" : peer.getName());
    row.add(peer == null ? "" : peer.getIcon());
    row.add(peer == null || peer.getAvatar().isEmpty() ? null : peer.getAvatar().toByteArray());
    row.add(peer != null && peer.getDeleted());
    row.add(p.o.topic());
    row.add(p.o.memberCount());
    row.add(true);
    row.add(false);
    row.add(msg.getSeq());
    row.add(msg.getSenderUserId().toByteArray());
    row.add(msg.getClientMessageId());
    row.add(MessageBody.encode(msg));
    row.add(Timestamps.fromProto(msg.getSentAt()));
    row.add(p.userId.matches(msg.getSenderUserId()) ? msg.getSeq() : 0L);
    row.add(0L);
    row.add(Timestamps.fromProto(p.event.getOccurredAt()));
    return row;
  }

  private static String marks(int n) {
    return String.join(", ", Collections.nCopies(n, "?"));
  }

  /** n rows of a multi-row VALUES list of the given width. */
  static String rows(int n, int width) {
    return String.join(", ", Collections.nCopies(n, "(" + marks(width) + ")"));
  }
}
