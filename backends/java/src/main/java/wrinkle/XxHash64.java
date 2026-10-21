package wrinkle;

import java.lang.invoke.MethodHandles;
import java.lang.invoke.VarHandle;
import java.nio.ByteOrder;

/** The 64-bit xxHash (XXH64) with seed 0, as Vitess's xxhash vindex computes it. */
public final class XxHash64 {
  private XxHash64() {}

  private static final long P1 = 0x9E3779B185EBCA87L;
  private static final long P2 = 0xC2B2AE3D27D4EB4FL;
  private static final long P3 = 0x165667B19E3779F9L;
  private static final long P4 = 0x85EBCA77C2B2AE63L;
  private static final long P5 = 0x27D4EB2F165667C5L;
  private static final VarHandle LONG =
      MethodHandles.byteArrayViewVarHandle(long[].class, ByteOrder.LITTLE_ENDIAN);
  private static final VarHandle INT =
      MethodHandles.byteArrayViewVarHandle(int[].class, ByteOrder.LITTLE_ENDIAN);

  public static long hash(byte[] b) {
    int len = b.length;
    int i = 0;
    long h;
    if (len >= 32) {
      long v1 = P1 + P2;
      long v2 = P2;
      long v3 = 0;
      long v4 = -P1;
      for (; i <= len - 32; i += 32) {
        v1 = round(v1, (long) LONG.get(b, i));
        v2 = round(v2, (long) LONG.get(b, i + 8));
        v3 = round(v3, (long) LONG.get(b, i + 16));
        v4 = round(v4, (long) LONG.get(b, i + 24));
      }
      h =
          Long.rotateLeft(v1, 1)
              + Long.rotateLeft(v2, 7)
              + Long.rotateLeft(v3, 12)
              + Long.rotateLeft(v4, 18);
      h = merge(h, v1);
      h = merge(h, v2);
      h = merge(h, v3);
      h = merge(h, v4);
    } else {
      h = P5;
    }
    h += len;
    for (; i + 8 <= len; i += 8) {
      h ^= round(0, (long) LONG.get(b, i));
      h = Long.rotateLeft(h, 27) * P1 + P4;
    }
    if (i + 4 <= len) {
      h ^= ((int) INT.get(b, i) & 0xFFFFFFFFL) * P1;
      h = Long.rotateLeft(h, 23) * P2 + P3;
      i += 4;
    }
    for (; i < len; i++) {
      h ^= (b[i] & 0xFFL) * P5;
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
    return Long.rotateLeft(acc + input * P2, 31) * P1;
  }

  private static long merge(long acc, long v) {
    return (acc ^ round(0, v)) * P1 + P4;
  }
}
