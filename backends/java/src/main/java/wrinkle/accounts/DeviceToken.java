package wrinkle.accounts;

import java.nio.ByteBuffer;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.security.SecureRandom;
import java.util.Arrays;
import java.util.Base64;
import wrinkle.Ids;
import wrinkle.Uuid;

/**
 * A device token: "v1." and the unpadded base64url of the user id, the device id as 8 bytes
 * big-endian and a 32-byte secret. The device row keeps only the SHA-256 of the secret.
 */
record DeviceToken(Uuid userId, long deviceId, byte[] secret) {
  private static final String PREFIX = "v1.";
  private static final int SECRET_LENGTH = 32;
  private static final int LENGTH = Uuid.LENGTH + 8 + SECRET_LENGTH;
  private static final SecureRandom RANDOM = new SecureRandom();

  /** A token for a new device of the user, with a fresh secret. */
  static DeviceToken create(Uuid userId) {
    byte[] secret = new byte[SECRET_LENGTH];
    RANDOM.nextBytes(secret);
    return new DeviceToken(userId, Ids.random(), secret);
  }

  /** The token in the string, or null if it is malformed. */
  static DeviceToken parse(String s) {
    if (!s.startsWith(PREFIX) || s.indexOf('=') >= 0) {
      return null;
    }
    byte[] raw;
    try {
      raw = Base64.getUrlDecoder().decode(s.substring(PREFIX.length()));
    } catch (IllegalArgumentException e) {
      return null;
    }
    if (raw.length != LENGTH) {
      return null;
    }
    ByteBuffer b = ByteBuffer.wrap(raw);
    byte[] user = new byte[Uuid.LENGTH];
    b.get(user);
    long deviceId = b.getLong();
    return new DeviceToken(Uuid.of(user), deviceId, Arrays.copyOfRange(raw, b.position(), LENGTH));
  }

  @Override
  public String toString() {
    byte[] raw =
        ByteBuffer.allocate(LENGTH).put(userId.bytes()).putLong(deviceId).put(secret).array();
    return PREFIX + Base64.getUrlEncoder().withoutPadding().encodeToString(raw);
  }

  /** What the device row stores. */
  byte[] hash() {
    try {
      return MessageDigest.getInstance("SHA-256").digest(secret);
    } catch (NoSuchAlgorithmException e) {
      throw new IllegalStateException(e);
    }
  }
}
