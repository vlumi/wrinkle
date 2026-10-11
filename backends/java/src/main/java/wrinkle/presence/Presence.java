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
import java.util.HexFormat;
import java.util.function.BiConsumer;
import wrinkle.Uuid;

/**
 * Which device is connected to which gateway instance, and the push channel from fan-out workers to
 * gateways, through Valkey: one pub/sub channel per user. A device's entry names the gateway
 * instance, which counts as alive while its own key exists.
 */
public final class Presence implements AutoCloseable {
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

  public void publish(Uuid userId, byte[] payload) {
    commands.async().publish(channel(userId), payload);
  }

  /** Delivers pushes for subscribed users to the listener. */
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
    commands.async().hset(presenceKey(userId), bytes(Long.toString(deviceId)), instance);
  }

  /** Removes the device's entry unless it has since connected to another instance. */
  public void deviceDisconnected(Uuid userId, long deviceId) {
    commands
        .async()
        .eval(
            REMOVE_OWN,
            ScriptOutputType.INTEGER,
            new byte[][] {presenceKey(userId)},
            bytes(Long.toString(deviceId)),
            instance);
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
