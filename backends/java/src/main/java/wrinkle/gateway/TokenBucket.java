package wrinkle.gateway;

/** Up to capacity tokens, refilled continuously at a fixed rate. Not thread-safe. */
final class TokenBucket {
  private static final double NANOS_PER_SECOND = 1e9;

  private final double capacity;
  private final double perNano;
  private double tokens;
  private long last;

  TokenBucket(int capacity, int perSecond, long nowNanos) {
    this.capacity = capacity;
    this.perNano = perSecond / NANOS_PER_SECOND;
    this.tokens = capacity;
    this.last = nowNanos;
  }

  /** Takes a token if there is one. */
  boolean take(long nowNanos) {
    tokens = Math.min(capacity, tokens + (nowNanos - last) * perNano);
    last = nowNanos;
    if (tokens < 1) {
      return false;
    }
    tokens--;
    return true;
  }
}
