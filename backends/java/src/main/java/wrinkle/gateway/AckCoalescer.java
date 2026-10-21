package wrinkle.gateway;

import java.util.concurrent.Executor;
import java.util.concurrent.Future;
import java.util.function.LongConsumer;
import java.util.function.LongSupplier;

/**
 * Writes a device's acks one at a time, at most one write per interval: the first at once, later
 * ones at the previous write's start plus the interval, each with the highest cursor acked
 * meanwhile. Once the connection ends, the pending cursor is written at once, and so is every ack
 * after it. An interval of 0 writes each ack as it comes, collapsing those that arrive during a
 * write.
 */
final class AckCoalescer {
  /** Runs a task after a delay; the returned future cancels it. */
  interface Timer {
    Future<?> schedule(Runnable task, long delayNanos);
  }

  private final Executor executor;
  private final Timer timer;
  private final LongSupplier clock;
  private final long intervalNanos;
  private final LongConsumer writer;
  // Guarded by this.
  private boolean hasPending;
  private long pending;
  private boolean writing;
  private Future<?> scheduled;
  private boolean started;
  private long lastStart;
  private boolean ended;

  /** The writer handles its own failures. */
  AckCoalescer(
      Executor executor, Timer timer, LongSupplier clock, long intervalNanos, LongConsumer writer) {
    this.executor = executor;
    this.timer = timer;
    this.clock = clock;
    this.intervalNanos = intervalNanos;
    this.writer = writer;
  }

  void offer(long cursor) {
    long next;
    synchronized (this) {
      if (!hasPending || cursor > pending) {
        pending = cursor;
        hasPending = true;
      }
      if (writing || scheduled != null) {
        return;
      }
      long now = clock.getAsLong();
      if (started && !immediate() && now - lastStart < intervalNanos) {
        scheduled = timer.schedule(this::fire, lastStart + intervalNanos - now);
        return;
      }
      next = take(now);
    }
    executor.execute(() -> write(next));
  }

  /** The connection has ended: writes the pending cursor now, after any write in flight. */
  void end() {
    long next;
    synchronized (this) {
      if (ended) {
        return;
      }
      ended = true;
      if (scheduled != null) {
        scheduled.cancel(false);
        scheduled = null;
      }
      if (writing || !hasPending) {
        return;
      }
      next = take(clock.getAsLong());
    }
    executor.execute(() -> write(next));
  }

  private void fire() {
    long next;
    synchronized (this) {
      if (scheduled == null) {
        return;
      }
      scheduled = null;
      if (writing || !hasPending) {
        return;
      }
      next = take(clock.getAsLong());
    }
    executor.execute(() -> write(next));
  }

  private boolean immediate() {
    return intervalNanos <= 0 || ended;
  }

  /** Starts a write of the pending cursor. */
  private long take(long now) {
    hasPending = false;
    writing = true;
    started = true;
    lastStart = now;
    return pending;
  }

  private void write(long cursor) {
    long next = cursor;
    while (true) {
      writer.accept(next);
      synchronized (this) {
        writing = false;
        if (!hasPending) {
          return;
        }
        long now = clock.getAsLong();
        long delay = lastStart + intervalNanos - now;
        if (!immediate() && delay > 0) {
          scheduled = timer.schedule(this::fire, delay);
          return;
        }
        next = take(now);
      }
    }
  }
}
