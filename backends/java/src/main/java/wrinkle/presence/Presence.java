package wrinkle.presence;

import io.lettuce.core.RedisClient;
import io.lettuce.core.ScriptOutputType;
import io.lettuce.core.SetArgs;
import io.lettuce.core.api.StatefulRedisConnection;
import io.lettuce.core.codec.ByteArrayCodec;
import io.lettuce.core.pubsub.RedisPubSubAdapter;
import io.lettuce.core.pubsub.StatefulRedisPubSubConnection;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;
import java.util.HexFormat;
import java.util.function.BiConsumer;

/**
 * Which device is connected to which gateway instance, and the push channel from fan-out workers to
 * gateways, through Valkey: one pub/sub channel per user. A device's entry names the gateway
 * instance, which counts as alive while its own key exists.
 */
public final class Presence implements AutoCloseable {
  private static final String CHANNEL_PREFIX = "push:";
  private static final String INSTANCE_PREFIX = "gateway:";
  private static final long INSTANCE_TTL_SECONDS = 90;
  private static final byte[] ALIVE = bytes("1");
  // Removes a device's entry only if it still names this instance.
  private static final byte[] REMOVE_OWN =
      bytes(
          "if redis.call('HGET', KEYS[1], ARGV[1]) == ARGV[2] then"
              + " return redis.call('HDEL', KEYS[1], ARGV[1]) end return 0");

  private final RedisClient client;
  private final StatefulRedisConnection<byte[], byte[]> commands;
  private final StatefulRedisPubSubConnection<byte[], byte[]> pubsub;
  private final byte[] instance;
  private final byte[] instanceKey;

  public Presence(String addr, String node) {
    this.client = RedisClient.create("redis://" + addr);
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

  private static byte[] channel(long userId) {
    return (CHANNEL_PREFIX + userId).getBytes(StandardCharsets.US_ASCII);
  }

  /** The user a push channel belongs to, or -1. */
  private static long userOf(byte[] channel) {
    String s = new String(channel, StandardCharsets.US_ASCII);
    if (!s.startsWith(CHANNEL_PREFIX)) {
      return -1;
    }
    try {
      return Long.parseLong(s.substring(CHANNEL_PREFIX.length()));
    } catch (NumberFormatException e) {
      return -1;
    }
  }

  public void publish(long userId, byte[] payload) {
    commands.async().publish(channel(userId), payload);
  }

  /** Delivers pushes for subscribed users to the listener. */
  public void listen(BiConsumer<Long, byte[]> listener) {
    pubsub.addListener(
        new RedisPubSubAdapter<>() {
          @Override
          public void message(byte[] channel, byte[] message) {
            long userId = userOf(channel);
            if (userId >= 0) {
              listener.accept(userId, message);
            }
          }
        });
  }

  public void subscribe(long userId) {
    pubsub.sync().subscribe(channel(userId));
  }

  public void unsubscribe(long userId) {
    pubsub.sync().unsubscribe(channel(userId));
  }

  public void deviceConnected(long userId, long deviceId) {
    commands.async().hset(presenceKey(userId), bytes(Long.toString(deviceId)), instance);
  }

  /** Removes the device's entry unless it has since connected to another instance. */
  public void deviceDisconnected(long userId, long deviceId) {
    commands
        .async()
        .eval(
            REMOVE_OWN,
            ScriptOutputType.INTEGER,
            new byte[][] {presenceKey(userId)},
            bytes(Long.toString(deviceId)),
            instance);
  }

  private static byte[] presenceKey(long userId) {
    return bytes("presence:" + userId);
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
