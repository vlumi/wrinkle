package wrinkle.fanout;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.sql.SQLException;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.atomic.AtomicBoolean;
import javax.sql.DataSource;
import org.apache.kafka.clients.consumer.CloseOptions;
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
import wrinkle.presence.Presence;
import wrinkle.v1.Event;

/**
 * Turns committed outbox rows, read from Kafka, into entries in each participant's event log, and
 * pushes them to connected devices.
 */
public final class FanoutWorker {
  public static final String TOPIC = "wrinkle.conversation.outbox";
  private static final Logger log = LoggerFactory.getLogger(FanoutWorker.class);
  private static final int ATTEMPTS = 5;
  private static final Duration CLOSE_TIMEOUT = Duration.ofSeconds(3);

  private final KafkaConsumer<byte[], byte[]> consumer;
  private final UserLog userLog;
  private final ExecutorService tasks = Executors.newVirtualThreadPerTaskExecutor();
  private final AtomicBoolean stopped = new AtomicBoolean();
  private volatile Thread thread;

  public FanoutWorker(String brokers, String group, DataSource userDb, Presence presence) {
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
    this.userLog = new UserLog(userDb, presence);
  }

  /** Consumes on a platform thread of its own until stopped. */
  public void start() {
    thread = Thread.ofPlatform().name("fanout").start(this::run);
  }

  /** Asks the consumer to stop; it leaves the group as it closes. */
  public void stop() {
    stopped.set(true);
    consumer.wakeup();
  }

  /** Waits until the consumer has closed, or the time passes; true if it has. */
  public boolean awaitStopped(long millis) throws InterruptedException {
    return thread == null || thread.join(Duration.ofMillis(Math.max(millis, 1)));
  }

  /** Consumes until stopped. A batch is committed only after every record in it is delivered. */
  private void run() {
    consumer.subscribe(List.of(TOPIC));
    try {
      while (!stopped.get()) {
        ConsumerRecords<byte[], byte[]> records = consumer.poll(Duration.ofSeconds(1));
        if (records.isEmpty()) {
          continue;
        }
        if (deliverBatch(records)) {
          consumer.commitSync();
        } else {
          // The records come back on the next poll.
          for (TopicPartition tp : records.partitions()) {
            consumer.seek(tp, records.records(tp).getFirst().offset());
          }
          Thread.sleep(1000);
        }
      }
    } catch (WakeupException e) {
      // stop() was called.
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
    } finally {
      consumer.close(CloseOptions.timeout(CLOSE_TIMEOUT));
    }
  }

  /**
   * Delivers conversations in parallel and each conversation's records in order: the ordering the
   * contract promises, while a user's log is serialized by its row lock whatever the interleaving.
   * False if any record could not be delivered.
   */
  private boolean deliverBatch(ConsumerRecords<byte[], byte[]> records)
      throws InterruptedException {
    List<Future<?>> running = new ArrayList<>();
    for (TopicPartition tp : records.partitions()) {
      for (List<ConsumerRecord<byte[], byte[]>> conversation :
          byConversation(records.records(tp))) {
        running.add(
            tasks.submit(
                () -> {
                  for (ConsumerRecord<byte[], byte[]> rec : conversation) {
                    handleWithRetry(rec);
                  }
                  return null;
                }));
      }
    }
    boolean delivered = true;
    for (Future<?> f : running) {
      try {
        f.get();
      } catch (ExecutionException e) {
        delivered = false;
        log.error("giving up on a batch", e.getCause());
      }
    }
    return delivered;
  }

  /** Groups records by key, the conversation id, keeping each group in offset order. */
  private static List<List<ConsumerRecord<byte[], byte[]>>> byConversation(
      List<ConsumerRecord<byte[], byte[]>> records) {
    Map<String, List<ConsumerRecord<byte[], byte[]>>> groups = new LinkedHashMap<>();
    for (ConsumerRecord<byte[], byte[]> rec : records) {
      String key = rec.key() == null ? "" : new String(rec.key(), StandardCharsets.UTF_8);
      groups.computeIfAbsent(key, k -> new ArrayList<>()).add(rec);
    }
    return new ArrayList<>(groups.values());
  }

  private void handleWithRetry(ConsumerRecord<byte[], byte[]> rec) throws Exception {
    Exception last = null;
    for (int attempt = 1; attempt <= ATTEMPTS; attempt++) {
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

  private void handle(ConsumerRecord<byte[], byte[]> rec) throws Exception {
    if (rec.value() == null || rec.value().length == 0) {
      return;
    }
    Metrics.FANOUT_RECORDS.inc();
    OutboxRecord o;
    try {
      o = OutboxRecord.parse(rec.value());
    } catch (IOException | RuntimeException e) {
      // A malformed row never gets better; log it and move on.
      log.error("skipping outbox record at offset {}: {}", rec.offset(), e.toString());
      return;
    }
    if (o.event().getBodyCase() == Event.BodyCase.BODY_NOT_SET) {
      log.error("skipping outbox record without a body, kind {}", o.kind());
      return;
    }
    Future<?> a = tasks.submit(() -> appendFor(o.userA(), o));
    Future<?> b = tasks.submit(() -> appendFor(o.userB(), o));
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

  private Void appendFor(long userId, OutboxRecord o) throws SQLException {
    userLog.append(userId, o);
    return null;
  }
}
