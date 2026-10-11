package wrinkle.accounts;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.nio.charset.StandardCharsets;
import java.util.List;
import org.junit.jupiter.api.Test;
import wrinkle.Uuid;

class AccountsTest {
  // argon2id of "correct horse" with the salt "wrinkle-salt-16b" and the standard parameters.
  // Every backend's test checks the same string, so each verifies what the others write.
  private static final String KNOWN_HASH =
      "$argon2id$v=19$m=19456,t=2,p=1$d3JpbmtsZS1zYWx0LTE2Yg$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc";

  @Test
  void knownHash() throws Exception {
    assertEquals(
        KNOWN_HASH,
        Passwords.encode(
            "correct horse",
            "wrinkle-salt-16b".getBytes(StandardCharsets.US_ASCII),
            Passwords.MEMORY_KIB,
            Passwords.ITERATIONS,
            Passwords.PARALLELISM));
    assertTrue(Passwords.verify("correct horse", KNOWN_HASH));
    assertFalse(Passwords.verify("correct horse!", KNOWN_HASH));
  }

  @Test
  void hashRoundTrip() throws Exception {
    String hash = Passwords.hash("battery staple");
    assertTrue(hash.startsWith("$argon2id$v=19$m=19456,t=2,p=1$"), hash);
    assertTrue(Passwords.verify("battery staple", hash));
    assertNotEquals(hash, Passwords.hash("battery staple"));
  }

  @Test
  void malformedHash() {
    String salt = "d3JpbmtsZS1zYWx0LTE2Yg";
    String key = "C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc";
    for (String h :
        List.of(
            "",
            "$argon2i$v=19$m=19456,t=2,p=1$" + salt + "$" + key,
            "$argon2id$v=16$m=19456,t=2,p=1$" + salt + "$" + key,
            "$argon2id$v=19$m=19456,t=2,p=1x$" + salt + "$" + key,
            "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "==$" + key,
            "$argon2id$v=19$m=99999999,t=2,p=1$" + salt + "$" + key)) {
      assertThrows(
          Passwords.MalformedHashException.class, () -> Passwords.verify("correct horse", h), h);
    }
  }

  @Test
  void token() {
    Uuid userId = Uuid.random();
    DeviceToken token = DeviceToken.create(userId);
    String s = token.toString();
    assertTrue(s.startsWith("v1.") && s.length() == 3 + 75, s);
    DeviceToken parsed = DeviceToken.parse(s);
    assertNotNull(parsed);
    assertEquals(userId, parsed.userId());
    assertEquals(token.deviceId(), parsed.deviceId());
    assertArrayEquals(token.hash(), parsed.hash());
    for (String bad :
        List.of(
            "",
            "v1.",
            "v2." + s.substring(3),
            s + "A",
            s.substring(0, s.length() - 1),
            s.replace("v1.", "v1.+"))) {
      assertNull(DeviceToken.parse(bad), bad);
    }
  }

  @Test
  void tokenLayout() {
    // User id, device id big-endian, secret: fixed bytes give a fixed token.
    byte[] user = new byte[16];
    user[0] = (byte) 0xff;
    user[15] = 0x01;
    DeviceToken token = new DeviceToken(Uuid.of(user), 0x0102030405060708L, new byte[32]);
    assertEquals(
        "v1._wAAAAAAAAAAAAAAAAAAAQECAwQFBgcIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
        token.toString());
  }

  @Test
  void validName() {
    for (String name : List.of("a", "Bob", "Ana María", "n".repeat(64), "😀".repeat(16), "émile")) {
      assertTrue(AccountService.validName(name), name);
    }
    for (String name :
        List.of(
            "",
            "n".repeat(65),
            "😀".repeat(17),
            " bob",
            "bob ",
            "bob ",
            "　bob",
            "bo\u0000b",
            "bo\nb",
            "bo\u0085b",
            "bo\u007fb")) {
      assertFalse(AccountService.validName(name), name);
    }
  }

  @Test
  void validIcon() {
    for (String icon : List.of("", "cat", "unicorn")) {
      assertTrue(AccountService.validIcon(icon), icon);
    }
    for (String icon : List.of("Cat", "dragon", " cat")) {
      assertFalse(AccountService.validIcon(icon), icon);
    }
  }
}
