package wrinkle;

import java.security.SecureRandom;

/** Random 63-bit ids for devices and outbox rows; they survive signed 64-bit handling. */
public final class Ids {
  private Ids() {}

  private static final SecureRandom RANDOM = new SecureRandom();

  public static long random() {
    long id = RANDOM.nextLong() & Long.MAX_VALUE;
    return id == 0 ? 1 : id;
  }
}
