package wrinkle;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import org.junit.jupiter.api.Test;

class UuidTest {
  @Test
  void randomIsVersion4() {
    Uuid id = Uuid.random();
    byte[] b = id.bytes();
    assertEquals(16, b.length);
    assertEquals(0x40, b[6] & 0xf0);
    assertEquals(0x80, b[8] & 0xc0);
    assertNotEquals(id, Uuid.random());
  }

  @Test
  void hexRoundTrip() {
    Uuid id = Uuid.random();
    assertEquals(32, id.hex().length());
    assertEquals(id, Uuid.fromHex(id.hex()));
    // Only the lowercase form names a user.
    assertNull(Uuid.fromHex("FF" + "0".repeat(30)));
    assertEquals("ff" + "0".repeat(30), Uuid.fromHex("ff" + "0".repeat(30)).hex());
    assertNull(Uuid.fromHex("00"));
  }

  @Test
  void parseNeedsSixteenBytes() {
    assertNull(Uuid.parse(new byte[15]));
    assertNull(Uuid.parse(new byte[17]));
    assertEquals("00000000000000000000000000000000", Uuid.parse(new byte[16]).hex());
  }

  @Test
  void ordersBytewiseUnsigned() {
    byte[] low = new byte[16];
    byte[] high = new byte[16];
    low[0] = 0x7f;
    high[0] = (byte) 0x80;
    assertTrue(Uuid.of(low).compareTo(Uuid.of(high)) < 0);
  }

  @Test
  void randomIdsFitIn63Bits() {
    for (int i = 0; i < 100; i++) {
      assertTrue(Ids.random() > 0);
    }
  }
}
