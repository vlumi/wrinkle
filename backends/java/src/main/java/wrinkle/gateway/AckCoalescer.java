package wrinkle.gateway;

import java.util.concurrent.Executor;
import java.util.function.LongConsumer;

/**
 * Writes a device's acks one at a time. Acks that arrive during a write replace the pending cursor,
 * keeping the highest, which is written when the current write finishes.
 */
final class AckCoalescer {
  private final Executor executor;
  private final LongConsumer writer;
  private boolean writing;
  private boolean hasPending;
  private long pending;

  /** The writer handles its own failures. */
  AckCoalescer(Executor executor, LongConsumer writer) {
    this.executor = executor;
    this.writer = writer;
  }

  void offer(long cursor) {
    synchronized (this) {
      if (writing) {
        if (!hasPending || cursor > pending) {
          pending = cursor;
          hasPending = true;
        }
        return;
      }
      writing = true;
    }
    executor.execute(() -> write(cursor));
  }

  private void write(long cursor) {
    long next = cursor;
    while (true) {
      writer.accept(next);
      synchronized (this) {
        if (!hasPending) {
          writing = false;
          return;
        }
        next = pending;
        hasPending = false;
      }
    }
  }
}
