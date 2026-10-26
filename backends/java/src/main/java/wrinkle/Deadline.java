package wrinkle;

import java.util.concurrent.TimeUnit;

/** When the request being handled on this thread must be answered by, as System.nanoTime. */
public final class Deadline {
  private Deadline() {}

  /** How long a request may take, as the gateways wait for one. */
  public static final long REQUEST_TIMEOUT_NANOS = TimeUnit.SECONDS.toNanos(10);

  private static final ScopedValue<Long> AT = ScopedValue.newInstance();

  /** Runs the work with the deadline set. */
  public static <T, X extends Throwable> T within(long at, ScopedValue.CallableOp<T, X> work)
      throws X {
    return ScopedValue.where(AT, at).call(work);
  }

  /** The time left until the deadline, or the request timeout outside a request. */
  public static long remainingNanos() {
    return AT.isBound() ? AT.get() - System.nanoTime() : REQUEST_TIMEOUT_NANOS;
  }
}
