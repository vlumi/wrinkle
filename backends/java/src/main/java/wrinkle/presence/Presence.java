package wrinkle.presence;

import io.lettuce.core.RedisClient;
import io.lettuce.core.api.StatefulRedisConnection;
import io.lettuce.core.codec.ByteArrayCodec;
import io.lettuce.core.pubsub.RedisPubSubAdapter;
import io.lettuce.core.pubsub.StatefulRedisPubSubConnection;
import java.nio.charset.StandardCharsets;
import java.util.function.BiConsumer;

/**
 * Which device is connected to which gateway node, and the push channel from fan-out workers to
 * gateways, through Valkey: one pub/sub channel per user.
 */
public final class Presence implements AutoCloseable {
  private static final String CHANNEL_PREFIX = "push:";

  private final RedisClient client;
  private final StatefulRedisConnection<byte[], byte[]> commands;
  private final StatefulRedisPubSubConnection<byte[], byte[]> pubsub;
  private final String node;

  public Presence(String addr, String node) {
    this.client = RedisClient.create("redis://" + addr);
    this.commands = client.connect(ByteArrayCodec.INSTANCE);
    this.pubsub = client.connectPubSub(ByteArrayCodec.INSTANCE);
    this.node = node;
    commands.sync().ping();
  }

  private static byte[] channel(long userId) {
    return (CHANNEL_PREFIX + userId).getBytes(StandardCharsets.US_ASCII);
  }

  /** The user a push channel belongs to, or -1. */
  public static long userOf(byte[] channel) {
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
    commands.async().hset(presenceKey(userId), bytes(Long.toString(deviceId)), bytes(node));
  }

  public void deviceDisconnected(long userId, long deviceId) {
    commands.async().hdel(presenceKey(userId), bytes(Long.toString(deviceId)));
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
