package wrinkle.accounts;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.util.Base64;
import java.util.concurrent.Semaphore;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import org.bouncycastle.crypto.generators.Argon2BytesGenerator;
import org.bouncycastle.crypto.params.Argon2Parameters;

/**
 * argon2id password hashes in the PHC string format, bit for bit as the other backends write them.
 * Verification reads the parameters from the stored string, so they can change.
 */
final class Passwords {
  private Passwords() {}

  static final int MEMORY_KIB = 19456;
  static final int ITERATIONS = 2;
  static final int PARALLELISM = 1;
  private static final int SALT_LENGTH = 16;
  private static final int KEY_LENGTH = 32;
  private static final int VERSION = Argon2Parameters.ARGON2_VERSION_13;

  private static final SecureRandom RANDOM = new SecureRandom();
  private static final Base64.Encoder B64 = Base64.getEncoder().withoutPadding();
  private static final Pattern PARAMS = Pattern.compile("m=([0-9]+),t=([0-9]+),p=([0-9]+)");

  // Each hash takes about 19 MiB and a core for tens of milliseconds; a burst of sign-ins must not
  // exhaust memory.
  private static final Semaphore SLOTS = new Semaphore(Runtime.getRuntime().availableProcessors());

  /** A stored hash that cannot be read: an internal error, since the backends wrote it. */
  static final class MalformedHashException extends RuntimeException {
    MalformedHashException() {
      super("malformed password hash", null, false, false);
    }
  }

  /** The argon2id hash of the password with a fresh salt. */
  static String hash(String password) {
    byte[] salt = new byte[SALT_LENGTH];
    RANDOM.nextBytes(salt);
    SLOTS.acquireUninterruptibly();
    try {
      return encode(password, salt, MEMORY_KIB, ITERATIONS, PARALLELISM);
    } finally {
      SLOTS.release();
    }
  }

  static String encode(String password, byte[] salt, int memory, int iterations, int parallelism) {
    byte[] key = derive(password, salt, memory, iterations, parallelism, KEY_LENGTH);
    return "$argon2id$v="
        + VERSION
        + "$m="
        + memory
        + ",t="
        + iterations
        + ",p="
        + parallelism
        + "$"
        + B64.encodeToString(salt)
        + "$"
        + B64.encodeToString(key);
  }

  /** Whether the password matches the PHC string. */
  static boolean verify(String password, String encoded) {
    String[] parts = encoded.split("\\$", -1);
    if (parts.length != 6
        || !parts[0].isEmpty()
        || !parts[1].equals("argon2id")
        || !parts[2].equals("v=" + VERSION)) {
      throw new MalformedHashException();
    }
    Matcher m = PARAMS.matcher(parts[3]);
    if (!m.matches()) {
      throw new MalformedHashException();
    }
    long memory;
    long iterations;
    long parallelism;
    try {
      memory = Long.parseLong(m.group(1));
      iterations = Long.parseLong(m.group(2));
      parallelism = Long.parseLong(m.group(3));
    } catch (NumberFormatException e) {
      throw new MalformedHashException();
    }
    if (!parts[3].equals("m=" + memory + ",t=" + iterations + ",p=" + parallelism)) {
      throw new MalformedHashException();
    }
    // Bounds against a corrupt row costing unbounded memory or time.
    if (memory < 8
        || memory > 1 << 21
        || iterations < 1
        || iterations > 16
        || parallelism < 1
        || parallelism > 255) {
      throw new MalformedHashException();
    }
    byte[] salt = decode(parts[4]);
    byte[] want = decode(parts[5]);
    if (want.length < 16 || want.length > 64) {
      throw new MalformedHashException();
    }
    SLOTS.acquireUninterruptibly();
    byte[] got;
    try {
      got = derive(password, salt, (int) memory, (int) iterations, (int) parallelism, want.length);
    } finally {
      SLOTS.release();
    }
    return MessageDigest.isEqual(got, want);
  }

  /** Standard base64 without padding, as the PHC format has it. */
  private static byte[] decode(String s) throws MalformedHashException {
    if (s.indexOf('=') >= 0) {
      throw new MalformedHashException();
    }
    try {
      return Base64.getDecoder().decode(s);
    } catch (IllegalArgumentException e) {
      throw new MalformedHashException();
    }
  }

  private static byte[] derive(
      String password, byte[] salt, int memory, int iterations, int parallelism, int length) {
    Argon2BytesGenerator gen = new Argon2BytesGenerator();
    gen.init(
        new Argon2Parameters.Builder(Argon2Parameters.ARGON2_id)
            .withVersion(VERSION)
            .withMemoryAsKB(memory)
            .withIterations(iterations)
            .withParallelism(parallelism)
            .withSalt(salt)
            .build());
    byte[] out = new byte[length];
    gen.generateBytes(password.getBytes(StandardCharsets.UTF_8), out);
    return out;
  }

  /**
   * A hash to verify against when there is no account to check, so an unknown name takes as long as
   * a wrong password.
   */
  static String dummy() {
    return Dummy.HASH;
  }

  private static final class Dummy {
    static final String HASH;

    static {
      byte[] password = new byte[24];
      byte[] salt = new byte[SALT_LENGTH];
      RANDOM.nextBytes(password);
      RANDOM.nextBytes(salt);
      HASH = encode(B64.encodeToString(password), salt, MEMORY_KIB, ITERATIONS, PARALLELISM);
    }
  }
}
