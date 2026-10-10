package wrinkle;

import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;

/**
 * Derived identifiers, the same functions as the Go backend so both work on one database: xxhash64
 * of the name for a user, of the ordered user pair for a conversation, within 63 bits.
 */
public final class Ids {
  private Ids() {}

  private static final long MASK = Long.MAX_VALUE;
  private static final SecureRandom RANDOM = new SecureRandom();

  public static long user(String name) {
    return nonZero(xxhash64(name.getBytes(StandardCharsets.UTF_8)) & MASK);
  }

  public static long conversation(long a, long b) {
    long lo = Math.min(a, b);
    long hi = Math.max(a, b);
    byte[] buf = ByteBuffer.allocate(16).putLong(lo).putLong(hi).array();
    return nonZero(xxhash64(buf) & MASK);
  }

  public static long random() {
    return nonZero(RANDOM.nextLong() & MASK);
  }

  private static long nonZero(long id) {
    return id == 0 ? 1 : id;
  }

  private static final long P1 = 0x9E3779B185EBCA87L;
  private static final long P2 = 0xC2B2AE3D27D4EB4FL;
  private static final long P3 = 0x165667B19E3779F9L;
  private static final long P4 = 0x85EBCA77C2B2AE63L;
  private static final long P5 = 0x27D4EB2F165667C5L;

  /** XXH64 with seed 0, as cespare/xxhash computes it for the Go backend. */
  static long xxhash64(byte[] input) {
    ByteBuffer in = ByteBuffer.wrap(input).order(ByteOrder.LITTLE_ENDIAN);
    int len = input.length;
    long h;
    if (len >= 32) {
      long v1 = P1 + P2;
      long v2 = P2;
      long v3 = 0;
      long v4 = -P1;
      while (in.remaining() >= 32) {
        v1 = round(v1, in.getLong());
        v2 = round(v2, in.getLong());
        v3 = round(v3, in.getLong());
        v4 = round(v4, in.getLong());
      }
      h =
          Long.rotateLeft(v1, 1)
              + Long.rotateLeft(v2, 7)
              + Long.rotateLeft(v3, 12)
              + Long.rotateLeft(v4, 18);
      h = mergeRound(h, v1);
      h = mergeRound(h, v2);
      h = mergeRound(h, v3);
      h = mergeRound(h, v4);
    } else {
      h = P5;
    }
    h += len;
    while (in.remaining() >= 8) {
      h ^= round(0, in.getLong());
      h = Long.rotateLeft(h, 27) * P1 + P4;
    }
    if (in.remaining() >= 4) {
      h ^= (in.getInt() & 0xFFFFFFFFL) * P1;
      h = Long.rotateLeft(h, 23) * P2 + P3;
    }
    while (in.hasRemaining()) {
      h ^= (in.get() & 0xFFL) * P5;
      h = Long.rotateLeft(h, 11) * P1;
    }
    h ^= h >>> 33;
    h *= P2;
    h ^= h >>> 29;
    h *= P3;
    h ^= h >>> 32;
    return h;
  }

  private static long round(long acc, long input) {
    acc += input * P2;
    acc = Long.rotateLeft(acc, 31);
    return acc * P1;
  }

  private static long mergeRound(long acc, long val) {
    acc ^= round(0, val);
    return acc * P1 + P4;
  }
}
