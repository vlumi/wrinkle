package wrinkle;

import com.google.protobuf.ByteString;
import java.security.SecureRandom;
import java.util.Arrays;
import java.util.HexFormat;

/**
 * A user or conversation id: a random version 4 UUID, 16 bytes. Stored as BINARY(16), sent as
 * bytes, and named by its lowercase hex in Valkey keys. Ordered bytewise, unsigned.
 */
public final class Uuid implements Comparable<Uuid> {
  public static final int LENGTH = 16;
  private static final SecureRandom RANDOM = new SecureRandom();
  private static final HexFormat HEX = HexFormat.of();

  private final byte[] bytes;

  private Uuid(byte[] bytes) {
    this.bytes = bytes;
  }

  /** A new random version 4 UUID. */
  public static Uuid random() {
    byte[] b = new byte[LENGTH];
    RANDOM.nextBytes(b);
    b[6] = (byte) ((b[6] & 0x0f) | 0x40);
    b[8] = (byte) ((b[8] & 0x3f) | 0x80);
    return new Uuid(b);
  }

  /** The id in these bytes, or null unless there are exactly 16. */
  public static Uuid parse(byte[] b) {
    return b != null && b.length == LENGTH ? new Uuid(b.clone()) : null;
  }

  /** The id in these bytes, or null unless there are exactly 16. */
  public static Uuid parse(ByteString b) {
    return b.size() == LENGTH ? new Uuid(b.toByteArray()) : null;
  }

  /** The id a BINARY(16) column holds; anything else is a corrupt row. */
  public static Uuid of(byte[] b) {
    Uuid id = parse(b);
    if (id == null) {
      throw new IllegalArgumentException("uuid of " + (b == null ? "null" : b.length + " bytes"));
    }
    return id;
  }

  /** The id named by its lowercase hex, or null. */
  public static Uuid fromHex(String s) {
    if (s.length() != 2 * LENGTH) {
      return null;
    }
    try {
      Uuid id = new Uuid(HEX.parseHex(s));
      return id.hex().equals(s) ? id : null;
    } catch (IllegalArgumentException e) {
      return null;
    }
  }

  public byte[] bytes() {
    return bytes.clone();
  }

  public ByteString toByteString() {
    return ByteString.copyFrom(bytes);
  }

  /** Lowercase hex of the 16 bytes, as Valkey keys name users. */
  public String hex() {
    return HEX.formatHex(bytes);
  }

  public boolean matches(ByteString b) {
    return b.size() == LENGTH && Arrays.equals(bytes, b.toByteArray());
  }

  @Override
  public int compareTo(Uuid o) {
    return Arrays.compareUnsigned(bytes, o.bytes);
  }

  @Override
  public boolean equals(Object o) {
    return o instanceof Uuid u && Arrays.equals(bytes, u.bytes);
  }

  @Override
  public int hashCode() {
    return Arrays.hashCode(bytes);
  }

  @Override
  public String toString() {
    return hex();
  }
}
