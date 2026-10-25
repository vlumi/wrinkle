package wrinkle.presence;

import io.lettuce.core.RedisClient;
import io.lettuce.core.RedisURI;
import io.lettuce.core.ScriptOutputType;
import io.lettuce.core.SetArgs;
import io.lettuce.core.api.StatefulRedisConnection;
import io.lettuce.core.codec.ByteArrayCodec;
import io.lettuce.core.pubsub.RedisPubSubAdapter;
import io.lettuce.core.pubsub.StatefulRedisPubSubConnection;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;
import java.time.Duration;
import java.util.HashMap;
import java.util.HashSet;
import java.util.HexFormat;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionStage;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.function.BiConsumer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.ShardWork;
import wrinkle.Uuid;
import wrinkle.backend.v1.Push;

/**
 * Which device is connected to which gateway instance, the push channel from fan-out workers to
 * gateways, and the leases that share background work among instances, through Valkey: one pub/sub
 * channel per user. A device's entry names the gateway instance, which counts as alive while its
 * own key exists.
 */
public final class Presence implements AutoCloseable, ShardWork.Leases {
  private static final Logger log = LoggerFactory.getLogger(Presence.class);
  private static final String CHANNEL_PREFIX = "push:";
  private static final String INSTANCE_PREFIX = "gateway:";
  private static final long INSTANCE_TTL_SECONDS = 90;
  private static final Duration COMMAND_TIMEOUT = Duration.ofSeconds(5);
  private static final byte[] ALIVE = bytes("1");
  // Removes a device's entry only if it still names this instance.
  private static final byte[] REMOVE_OWN =
      bytes(
          "if redis.call('HGET', KEYS[1], ARGV[1]) == ARGV[2] then"
              + " return redis.call('HDEL', KEYS[1], ARGV[1]) end return 0");
  // Change a lease only while this instance holds it.
  private static final byte[] RENEW_LEASE =
      bytes(
          "if redis.call('GET', KEYS[1]) == ARGV[1] then"
              + " return redis.call('PEXPIRE', KEYS[1], ARGV[2]) end return 0");
  private static final byte[] DROP_LEASE =
      bytes(
          "if redis.call('GET', KEYS[1]) == ARGV[1] then"
              + " return redis.call('DEL', KEYS[1]) end return 0");

  private final RedisClient client;
  private final StatefulRedisConnection<byte[], byte[]> commands;
  private final StatefulRedisPubSubConnection<byte[], byte[]> pubsub;
  private final byte[] instance;
  private final byte[] instanceKey;

  public Presence(String addr, String node) {
    RedisURI uri = RedisURI.create("redis://" + addr);
    uri.setTimeout(COMMAND_TIMEOUT);
    // Lettuce reconnects with exponential backoff and resubscribes the channels it holds.
    this.client = RedisClient.create(uri);
    this.commands = client.connect(ByteArrayCodec.INSTANCE);
    this.pubsub = client.connectPubSub(ByteArrayCodec.INSTANCE);
    byte[] random = new byte[8];
    new SecureRandom().nextBytes(random);
    String id = node + "/" + HexFormat.of().formatHex(random);
    this.instance = bytes(id);
    this.instanceKey = bytes(INSTANCE_PREFIX + id);
    commands.sync().ping();
  }

  /** Marks this gateway instance alive for the next while; repeat well within the expiry. */
  public void heartbeat() {
    commands.sync().set(instanceKey, ALIVE, SetArgs.Builder.ex(INSTANCE_TTL_SECONDS));
  }

  /** Marks this gateway instance gone. */
  public void withdraw() {
    commands.sync().del(instanceKey);
  }

  // Keys and channels name a user by the lowercase hex of the id.
  private static byte[] channel(Uuid userId) {
    return (CHANNEL_PREFIX + userId.hex()).getBytes(StandardCharsets.US_ASCII);
  }

  /** The user a push channel belongs to, or null. */
  static Uuid userOf(byte[] channel) {
    String s = new String(channel, StandardCharsets.US_ASCII);
    if (!s.startsWith(CHANNEL_PREFIX)) {
      return null;
    }
    return Uuid.fromHex(s.substring(CHANNEL_PREFIX.length()));
  }

  /** Publishes on the user's push channel; a failure is logged, not retried. */
  public void publish(Uuid userId, Push push) {
    logged("publish", commands.async().publish(channel(userId), push.toByteArray()));
  }

  /** Logs a command's failure instead of dropping it with the future. */
  private static void logged(String command, CompletionStage<?> result) {
    result.exceptionally(
        e -> {
          log.warn("{} failed: {}", command, e.toString());
          return null;
        });
  }

  /** Delivers pushes for subscribed users to the listener, as serialized Push messages. */
  public void listen(BiConsumer<Uuid, byte[]> listener) {
    pubsub.addListener(
        new RedisPubSubAdapter<>() {
          @Override
          public void message(byte[] channel, byte[] message) {
            Uuid userId = userOf(channel);
            if (userId != null) {
              listener.accept(userId, message);
            }
          }
        });
  }

  /** Blocks until Valkey confirms; not for an event-loop thread. */
  public void subscribe(Uuid userId) {
    pubsub.sync().subscribe(channel(userId));
  }

  /** Blocks until Valkey confirms; not for an event-loop thread. */
  public void unsubscribe(Uuid userId) {
    pubsub.sync().unsubscribe(channel(userId));
  }

  public void deviceConnected(Uuid userId, long deviceId) {
    logged(
        "presence update",
        commands.async().hset(presenceKey(userId), bytes(Long.toString(deviceId)), instance));
  }

  /** Removes the device's entry unless it has since connected to another instance. */
  public void deviceDisconnected(Uuid userId, long deviceId) {
    logged(
        "presence removal",
        commands
            .async()
            .eval(
                REMOVE_OWN,
                ScriptOutputType.INTEGER,
                new byte[][] {presenceKey(userId)},
                bytes(Long.toString(deviceId)),
                instance));
  }

  /**
   * The user's devices connected to a live gateway instance. Entries of instances whose key expired
   * are removed on the way.
   */
  public Set<Long> connectedDevices(Uuid userId) {
    byte[] key = presenceKey(userId);
    Map<byte[], byte[]> entries = commands.sync().hgetall(key);
    Map<String, CompletableFuture<Long>> checks = new HashMap<>();
    for (byte[] instance : entries.values()) {
      checks.computeIfAbsent(
          new String(instance, StandardCharsets.UTF_8),
          id -> commands.async().exists(bytes(INSTANCE_PREFIX + id)).toCompletableFuture());
    }
    Set<Long> connected = new HashSet<>();
    for (Map.Entry<byte[], byte[]> e : entries.entrySet()) {
      String instance = new String(e.getValue(), StandardCharsets.UTF_8);
      if (checks.get(instance).join() == 0) {
        logged(
            "presence removal",
            commands
                .async()
                .eval(
                    REMOVE_OWN,
                    ScriptOutputType.INTEGER,
                    new byte[][] {key},
                    e.getKey(),
                    e.getValue()));
        continue;
      }
      try {
        connected.add(Long.parseUnsignedLong(new String(e.getKey(), StandardCharsets.UTF_8)));
      } catch (NumberFormatException ex) {
        // Not a device id; ignore the field.
      }
    }
    return connected;
  }

  @Override
  public boolean takeLease(String name, Duration ttl) {
    return "OK"
        .equals(
            commands.sync().set(leaseKey(name), instance, SetArgs.Builder.nx().px(ttl.toMillis())));
  }

  @Override
  public boolean renewLease(String name, Duration ttl) {
    Long n =
        commands
            .sync()
            .eval(
                RENEW_LEASE,
                ScriptOutputType.INTEGER,
                new byte[][] {leaseKey(name)},
                instance,
                bytes(Long.toString(ttl.toMillis())));
    return n == 1;
  }

  @Override
  public void dropLease(String name, Duration deadline) {
    try {
      commands
          .async()
          .eval(DROP_LEASE, ScriptOutputType.INTEGER, new byte[][] {leaseKey(name)}, instance)
          .toCompletableFuture()
          .get(deadline.toMillis(), TimeUnit.MILLISECONDS);
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
    } catch (ExecutionException | TimeoutException e) {
      throw new IllegalStateException("dropping lease " + name + " failed", e);
    }
  }

  private static byte[] leaseKey(String name) {
    return bytes("lease:" + name);
  }

  private static byte[] presenceKey(Uuid userId) {
    return bytes("presence:" + userId.hex());
  }

  private static byte[] bytes(String s) {
    return s.getBytes(StandardCharsets.UTF_8);
  }

  @Override
  public void close() {
    pubsub.close();
    commands.close();
    client.shutdown();
  }
}
