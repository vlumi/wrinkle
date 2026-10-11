package wrinkle.gateway;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.concurrent.TimeUnit;
import org.junit.jupiter.api.Test;

class TokenBucketTest {
  private static final long START = 1_000_000_000L;

  private static int takeAll(TokenBucket b, long now) {
    int n = 0;
    while (b.take(now)) {
      n++;
    }
    return n;
  }

  @Test
  void startsFullAndEmptiesAtCapacity() {
    TokenBucket b = new TokenBucket(100, 50, START);
    assertEquals(100, takeAll(b, START));
    assertFalse(b.take(START));
  }

  @Test
  void refillsAtTheRate() {
    TokenBucket b = new TokenBucket(100, 50, START);
    takeAll(b, START);
    assertEquals(25, takeAll(b, START + TimeUnit.MILLISECONDS.toNanos(500)));
    assertFalse(b.take(START + TimeUnit.MILLISECONDS.toNanos(510)));
    assertTrue(b.take(START + TimeUnit.MILLISECONDS.toNanos(520)));
  }

  @Test
  void neverHoldsMoreThanCapacity() {
    TokenBucket b = new TokenBucket(100, 50, START);
    assertEquals(100, takeAll(b, START + TimeUnit.MINUTES.toNanos(1)));
  }
}
