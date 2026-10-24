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
import java.util.concurrent.Semaphore;
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
import wrinkle.Shards;
import wrinkle.Uuid;
import wrinkle.presence.Presence;
import wrinkle.push.Notifier;
import wrinkle.v1.Event;

/**
 * Turns committed outbox rows, read from Kafka, into entries in the event logs of everyone
 * concerned, and pushes them to connected devices: a conversation's events to the recipients its
 * outbox row names, a user's profile changes to the user and every direct conversation partner, a
 * device's key changes to the user, the partners and the members of the user's groups, a palette
 * change to the user alone, a contact change to both users concerned. With a notifier, messages and
 * deletions also go to APNs for the recipients' devices that are not connected.
 */
public final class FanoutWorker {
  // The outbox topics: conversations' rows keyed by conversation id, users' by user id.
  public static final String CONVERSATION_TOPIC = "wrinkle.conversation.outbox";
  public static final String USER_TOPIC = "wrinkle.user.user_outbox";
  public static final List<String> TOPICS = List.of(CONVERSATION_TOPIC, USER_TOPIC);
  private static final Logger log = LoggerFactory.getLogger(FanoutWorker.class);
  private static final int ATTEMPTS = 5;
  // A key's records retry in turn, up to 3 seconds each; a poll's worth is bounded and given the
  // time, so the group does not take a working consumer for dead.
  private static final int MAX_POLL_RECORDS = 200;
  private static final long MAX_POLL_INTERVAL_MILLIS = 15 * 60 * 1000;
  // How many recipients of one outbox row are delivered to at a time.
  private static final int RECIPIENT_CONCURRENCY = 16;
  private static final Duration CLOSE_TIMEOUT = Duration.ofSeconds(3);

  private final KafkaConsumer<byte[], byte[]> consumer;
  private final UserLog userLog;
  private final ExecutorService tasks = Executors.newVirtualThreadPerTaskExecutor();
  private final ProfileFanout profiles;
  private final DeviceFanout devices;
  private final ContactFanout contacts;
  private final AtomicBoolean stopped = new AtomicBoolean();
  private volatile Thread thread;

  public FanoutWorker(
      String brokers,
      String group,
      DataSource userDb,
      String userKeyspace,
      DataSource conversationDb,
      Presence presence,
      Notifier notifier) {
    Properties p = new Properties();
    p.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, brokers);
    p.put(ConsumerConfig.GROUP_ID_CONFIG, group);
    p.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, "false");
    p.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
    p.put(ConsumerConfig.SESSION_TIMEOUT_MS_CONFIG, "10000");
    p.put(ConsumerConfig.HEARTBEAT_INTERVAL_MS_CONFIG, "3000");
    p.put(ConsumerConfig.MAX_POLL_RECORDS_CONFIG, Integer.toString(MAX_POLL_RECORDS));
    p.put(ConsumerConfig.MAX_POLL_INTERVAL_MS_CONFIG, Long.toString(MAX_POLL_INTERVAL_MILLIS));
    p.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, ByteArrayDeserializer.class.getName());
    p.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, ByteArrayDeserializer.class.getName());
    this.consumer = new KafkaConsumer<>(p);
    this.userLog = new UserLog(userDb, presence, Shards.load(userDb, userKeyspace), notifier);
    this.profiles = new ProfileFanout(userLog, tasks);
    this.devices = new DeviceFanout(userLog, conversationDb, tasks);
    this.contacts = new ContactFanout(userLog);
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
    consumer.subscribe(TOPICS);
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
   * Delivers keys (conversations, users) in parallel and each key's records in order: the ordering
   * the contract promises, while a user's log is serialized by its row lock whatever the
   * interleaving. False if any record could not be delivered.
   */
  private boolean deliverBatch(ConsumerRecords<byte[], byte[]> records)
      throws InterruptedException {
    List<Future<?>> running = new ArrayList<>();
    for (TopicPartition tp : records.partitions()) {
      for (List<ConsumerRecord<byte[], byte[]>> group : byKey(records.records(tp))) {
        running.add(
            tasks.submit(
                () -> {
                  for (ConsumerRecord<byte[], byte[]> rec : group) {
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

  /** Groups records by key, the conversation or user id, keeping each group in offset order. */
  private static List<List<ConsumerRecord<byte[], byte[]>>> byKey(
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
      } catch (InterruptedException e) {
        throw e;
      } catch (Exception e) {
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
    if (rec.topic().equals(USER_TOPIC)) {
      handleUserRecord(rec);
      return;
    }
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
    if (AppendBatcher.batchable(o)) {
      userLog.deliverBatched(o);
      return;
    }
    Semaphore slots = new Semaphore(RECIPIENT_CONCURRENCY);
    List<Future<?>> running = new ArrayList<>();
    for (Uuid recipient : o.recipients()) {
      slots.acquire();
      running.add(
          tasks.submit(
              () -> {
                try {
                  return deliver(recipient, o);
                } finally {
                  slots.release();
                }
              }));
    }
    Exception failure = null;
    for (Future<?> f : running) {
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

  private Void deliver(Uuid userId, OutboxRecord o) throws SQLException {
    userLog.deliver(userId, o);
    return null;
  }

  private void handleUserRecord(ConsumerRecord<byte[], byte[]> rec) throws Exception {
    UserOutboxRecord o;
    try {
      o = UserOutboxRecord.parse(rec.value());
    } catch (IOException | RuntimeException e) {
      log.error("skipping user outbox record at offset {}: {}", rec.offset(), e.toString());
      return;
    }
    switch (o.event().getBodyCase()) {
      case PROFILE -> profiles.deliver(o);
      case DEVICE -> devices.deliver(o);
      // The user's own devices only.
      case PALETTE -> userLog.append(o.userId(), o.userId(), o.outboxId(), o.event(), null);
      case CONTACT -> contacts.deliver(o);
      default ->
          log.error(
              "skipping user outbox record without a profile, device, palette or contact, kind {},"
                  + " offset {}",
              o.kind(),
              rec.offset());
    }
  }
}
