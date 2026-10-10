package wrinkle.fanout;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.SQLIntegrityConstraintViolationException;
import java.time.Duration;
import java.time.LocalDateTime;
import java.time.ZoneOffset;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.Future;
import java.util.concurrent.atomic.AtomicBoolean;
import javax.sql.DataSource;
import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.common.TopicPartition;
import org.apache.kafka.common.errors.WakeupException;
import org.apache.kafka.common.serialization.ByteArrayDeserializer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.Metrics;
import wrinkle.conversation.ConversationService;
import wrinkle.presence.Presence;
import wrinkle.v1.Event;
import wrinkle.v1.Message;

/**
 * Turns committed outbox rows, read from Kafka, into entries in each participant's event log, and
 * pushes them to connected devices. Partitions are handled in parallel, records within a partition
 * in order, so a conversation's events stay ordered.
 */
public final class FanoutWorker {
  public static final String TOPIC = "wrinkle.conversation.outbox";
  private static final Logger log = LoggerFactory.getLogger(FanoutWorker.class);
  private static final ObjectMapper JSON = new ObjectMapper();

  private final KafkaConsumer<byte[], byte[]> consumer;
  private final DataSource db;
  private final Presence presence;
  private final Map<Long, String> names = new ConcurrentHashMap<>();
  private final AtomicBoolean stopped = new AtomicBoolean();

  public FanoutWorker(String brokers, String group, DataSource db, Presence presence) {
    Properties p = new Properties();
    p.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, brokers);
    p.put(ConsumerConfig.GROUP_ID_CONFIG, group);
    p.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "false");
    p.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
    p.put(ConsumerConfig.SESSION_TIMEOUT_MS_CONFIG, "10000");
    p.put(ConsumerConfig.HEARTBEAT_INTERVAL_MS_CONFIG, "3000");
    p.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, ByteArrayDeserializer.class.getName());
    p.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, ByteArrayDeserializer.class.getName());
    this.consumer = new KafkaConsumer<>(p);
    this.db = db;
    this.presence = presence;
  }

  public void stop() {
    stopped.set(true);
    consumer.wakeup();
  }

  /** Consumes until stopped. A batch is committed only after every record in it is delivered. */
  public void run() {
    consumer.subscribe(List.of(TOPIC));
    try {
      while (!stopped.get()) {
        ConsumerRecords<byte[], byte[]> records = consumer.poll(Duration.ofSeconds(1));
        if (records.isEmpty()) {
          continue;
        }
        List<Future<?>> tasks = new ArrayList<>();
        for (TopicPartition tp : records.partitions()) {
          List<ConsumerRecord<byte[], byte[]>> partition = records.records(tp);
          tasks.add(
              Metrics.virtualThreads()
                  .submit(
                      () -> {
                        for (ConsumerRecord<byte[], byte[]> rec : partition) {
                          handleWithRetry(rec);
                        }
                        return null;
                      }));
        }
        boolean failed = false;
        for (Future<?> t : tasks) {
          try {
            t.get();
          } catch (ExecutionException e) {
            failed = true;
            log.error("giving up on a batch", e.getCause());
          } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return;
          }
        }
        if (failed) {
          // Leave the offsets where they are; the records come back on the next poll
          // after a seek.
          for (TopicPartition tp : records.partitions()) {
            consumer.seek(tp, records.records(tp).getFirst().offset());
          }
          Thread.sleep(1000);
          continue;
        }
        consumer.commitSync();
      }
    } catch (WakeupException e) {
      // stop() was called.
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
    } finally {
      consumer.close();
    }
  }

  private void handleWithRetry(ConsumerRecord<byte[], byte[]> rec) throws Exception {
    Exception last = null;
    for (int attempt = 1; attempt <= 5; attempt++) {
      try {
        handle(rec);
        return;
      } catch (SQLException | RuntimeException e) {
        last = e;
        log.warn("delivery failed, attempt {}: {}", attempt, e.toString());
        Thread.sleep(200L * attempt);
      }
    }
    throw last;
  }

  /** The outbox row as Debezium emits it: unsigned bigints as decimal strings, bytes as base64. */
  private record OutboxRow(
      long conversationId, long outboxId, long userA, long userB, String kind, Event event) {}

  private static OutboxRow parse(byte[] value) throws IOException {
    JsonNode n = JSON.readTree(value);
    return new OutboxRow(
        Long.parseLong(n.get("conversation_id").asText()),
        Long.parseLong(n.get("outbox_id").asText()),
        Long.parseLong(n.get("user_a").asText()),
        Long.parseLong(n.get("user_b").asText()),
        n.get("kind").asText(),
        Event.parseFrom(n.get("payload").binaryValue()));
  }

  private void handle(ConsumerRecord<byte[], byte[]> rec) throws Exception {
    if (rec.value() == null || rec.value().length == 0) {
      return;
    }
    Metrics.FANOUT_RECORDS.inc();
    OutboxRow o;
    try {
      o = parse(rec.value());
    } catch (IOException | RuntimeException e) {
      // A malformed row never gets better; log it and move on.
      log.error("skipping outbox record at offset {}: {}", rec.offset(), e.toString());
      return;
    }
    if (o.event().getBodyCase() == Event.BodyCase.BODY_NOT_SET) {
      log.error("skipping outbox record without a body, kind {}", o.kind());
      return;
    }
    Future<?> a = Metrics.virtualThreads().submit(() -> deliver(o.userA(), o));
    Future<?> b = Metrics.virtualThreads().submit(() -> deliver(o.userB(), o));
    Exception failure = null;
    for (Future<?> f : List.of(a, b)) {
      try {
        f.get();
      } catch (ExecutionException e) {
        failure = e.getCause() instanceof Exception ex ? ex : new RuntimeException(e.getCause());
      }
    }
    if (failure != null) {
      throw failure;
    }
  }

  /**
   * Appends the event to one user's log, in one transaction with the conversation list update, then
   * pushes it.
   */
  private Void deliver(long userId, OutboxRow o) throws SQLException {
    long peerId = userId == o.userA() ? o.userB() : o.userA();
    String peerName = o.event().hasMessage() ? peerName(peerId) : null;

    try (Connection c = db.getConnection()) {
      c.setAutoCommit(false);
      try {
        long head;
        try (PreparedStatement ps =
            c.prepareStatement("SELECT log_head FROM user WHERE user_id = ? FOR UPDATE")) {
          ps.setLong(1, userId);
          try (ResultSet rs = ps.executeQuery()) {
            if (!rs.next()) {
              log.warn("no such user {}, dropping event", userId);
              c.rollback();
              return null;
            }
            head = rs.getLong(1);
          }
        }
        Event event = o.event().toBuilder().setCursor(head + 1).build();
        byte[] payload = event.toByteArray();
        LocalDateTime occurredAt = ConversationService.fromProto(event.getOccurredAt());
        // The unique key on the source outbox row makes a redelivery fail here.
        try (PreparedStatement ps =
            c.prepareStatement(
                "INSERT INTO user_event (user_id, position, conversation_id, outbox_id, payload,"
                    + " occurred_at) VALUES (?, ?, ?, ?, ?, ?)")) {
          ps.setLong(1, userId);
          ps.setLong(2, event.getCursor());
          ps.setLong(3, o.conversationId());
          ps.setLong(4, o.outboxId());
          ps.setBytes(5, payload);
          ps.setObject(6, occurredAt);
          ps.executeUpdate();
        } catch (SQLIntegrityConstraintViolationException e) {
          c.rollback();
          return null;
        }
        try (PreparedStatement ps =
            c.prepareStatement("UPDATE user SET log_head = ? WHERE user_id = ?")) {
          ps.setLong(1, event.getCursor());
          ps.setLong(2, userId);
          ps.executeUpdate();
        }
        switch (event.getBodyCase()) {
          case MESSAGE -> upsertConversation(c, userId, peerId, peerName, o, event, occurredAt);
          case READ -> {
            String column = event.getRead().getUserId() == userId ? "read_seq" : "peer_read_seq";
            try (PreparedStatement ps =
                c.prepareStatement(
                    "UPDATE user_conversation SET "
                        + column
                        + " = GREATEST("
                        + column
                        + ", ?) WHERE user_id = ? AND conversation_id = ?")) {
              ps.setLong(1, event.getRead().getSeq());
              ps.setLong(2, userId);
              ps.setLong(3, o.conversationId());
              ps.executeUpdate();
            }
          }
          default -> {}
        }
        c.commit();
        Metrics.FANOUT_DELIVERIES.inc();
        Metrics.FANOUT_LAG.observe(
            Duration.between(occurredAt, LocalDateTime.now(ZoneOffset.UTC)).toNanos() / 1e9);
        presence.publish(userId, payload);
        return null;
      } catch (SQLException | RuntimeException e) {
        c.rollback();
        throw e;
      }
    }
  }

  private static void upsertConversation(
      Connection c,
      long userId,
      long peerId,
      String peerName,
      OutboxRow o,
      Event event,
      LocalDateTime occurredAt)
      throws SQLException {
    Message msg = event.getMessage().getMessage();
    LocalDateTime sentAt = ConversationService.fromProto(msg.getSentAt());
    try (PreparedStatement ps =
        c.prepareStatement(
            "INSERT INTO user_conversation (user_id, conversation_id, peer_user_id, peer_name,"
                + " last_seq, last_sender_user_id, last_client_message_id, last_body,"
                + " last_sent_at, read_seq, peer_read_seq, updated_at) VALUES (?, ?, ?, ?, ?, ?,"
                + " ?, ?, ?, 0, 0, ?) ON DUPLICATE KEY UPDATE last_seq = GREATEST(last_seq, ?),"
                + " last_sender_user_id = IF(? >= last_seq, ?, last_sender_user_id),"
                + " last_client_message_id = IF(? >= last_seq, ?, last_client_message_id),"
                + " last_body = IF(? >= last_seq, ?, last_body), last_sent_at = IF(? >= last_seq,"
                + " ?, last_sent_at), updated_at = GREATEST(updated_at, ?)")) {
      int i = 1;
      ps.setLong(i++, userId);
      ps.setLong(i++, o.conversationId());
      ps.setLong(i++, peerId);
      ps.setString(i++, peerName);
      ps.setLong(i++, msg.getSeq());
      ps.setLong(i++, msg.getSenderUserId());
      ps.setString(i++, msg.getClientMessageId());
      ps.setString(i++, msg.getText());
      ps.setObject(i++, sentAt);
      ps.setObject(i++, occurredAt);
      ps.setLong(i++, msg.getSeq());
      ps.setLong(i++, msg.getSeq());
      ps.setLong(i++, msg.getSenderUserId());
      ps.setLong(i++, msg.getSeq());
      ps.setString(i++, msg.getClientMessageId());
      ps.setLong(i++, msg.getSeq());
      ps.setString(i++, msg.getText());
      ps.setLong(i++, msg.getSeq());
      ps.setObject(i++, sentAt);
      ps.setObject(i, occurredAt);
      ps.executeUpdate();
    }
  }

  private String peerName(long userId) throws SQLException {
    String cached = names.get(userId);
    if (cached != null) {
      return cached;
    }
    try (Connection c = db.getConnection();
        PreparedStatement ps = c.prepareStatement("SELECT name FROM user WHERE user_id = ?")) {
      ps.setLong(1, userId);
      try (ResultSet rs = ps.executeQuery()) {
        if (!rs.next()) {
          throw new SQLException("name of user " + userId + ": no such user");
        }
        String name = rs.getString(1);
        names.put(userId, name);
        return name;
      }
    }
  }
}
