package wrinkle;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import org.junit.jupiter.api.Test;

class IdsTest {
  // The Go backend's values for the same inputs: both backends must agree, since they share
  // one database.
  @Test
  void matchesTheGoBackend() {
    assertEquals(8332761332120969289L, Ids.user("alice"));
    assertEquals(1335187801665884219L, Ids.user("bob"));
    assertEquals(5036853605090235588L, Ids.conversation(Ids.user("alice"), Ids.user("bob")));
    assertEquals(8710023849341551626L, Ids.user("ö-ünïcode"));
  }

  @Test
  void everyLengthClassHashesLikeTheGoBackend() {
    // Over 32 bytes takes the four-lane loop; 32 exactly has no tail; 7 is tail only.
    assertEquals(
        4216744368860594312L,
        Ids.user("a-name-long-enough-to-need-the-four-lane-loop-of-xxhash64"));
    assertEquals(70996906142265404L, Ids.user("exactly-thirty-two-bytes-long!!!"));
    assertEquals(6027063770514756441L, Ids.user("1234567"));
  }

  @Test
  void conversationIsSymmetric() {
    long a = Ids.user("alice");
    long b = Ids.user("bob");
    assertEquals(Ids.conversation(a, b), Ids.conversation(b, a));
    assertNotEquals(Ids.conversation(a, b), Ids.conversation(a, Ids.user("carol")));
  }

  @Test
  void idsFitIn63Bits() {
    for (long id : new long[] {Ids.user("alice"), Ids.user(""), Ids.random()}) {
      assertTrue(id > 0, "id " + id);
    }
  }
}
