package wrinkle.gateway;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Queue;
import java.util.concurrent.FutureTask;
import org.junit.jupiter.api.Test;

class AckCoalescerTest {
  private static final long INTERVAL = 60_000;

  private final Queue<Runnable> tasks = new ArrayDeque<>();
  private final List<Long> written = new ArrayList<>();
  private final List<Runnable> during = new ArrayList<>();
  private final List<Timed> timers = new ArrayList<>();
  private long now = 1_000_000;

  private record Timed(long at, FutureTask<Void> task) {}

  private AckCoalescer coalescer(long interval) {
    return new AckCoalescer(
        tasks::add,
        (task, delay) -> {
          FutureTask<Void> f = new FutureTask<>(task, null);
          timers.add(new Timed(now + delay, f));
          return f;
        },
        () -> now,
        interval,
        cursor -> {
          written.add(cursor);
          List<Runnable> d = new ArrayList<>(during);
          during.clear();
          d.forEach(Runnable::run);
        });
  }

  /** Moves the clock and runs the timers due by then. */
  private void advance(long by) {
    now += by;
    for (Timed t : new ArrayList<>(timers)) {
      if (t.at() <= now) {
        timers.remove(t);
        t.task().run();
      }
    }
  }

  private void runTasks() {
    while (!tasks.isEmpty()) {
      tasks.poll().run();
    }
  }

  private long pendingTimers() {
    return timers.stream().filter(t -> !t.task().isCancelled()).count();
  }

  @Test
  void writesTheFirstAckAtOnce() {
    AckCoalescer acks = coalescer(INTERVAL);
    acks.offer(1);
    assertEquals(1, tasks.size());
    runTasks();
    assertEquals(List.of(1L), written);
    assertEquals(0, pendingTimers());
  }

  @Test
  void holdsAcksWithinTheIntervalAndWritesTheHighestOnce() {
    AckCoalescer acks = coalescer(INTERVAL);
    acks.offer(1);
    runTasks();
    advance(10_000);
    acks.offer(5);
    acks.offer(3);
    advance(10_000);
    acks.offer(4);
    assertEquals(0, tasks.size());
    assertEquals(1, timers.size());
    assertEquals(1_000_000 + INTERVAL, timers.get(0).at());
    advance(INTERVAL - 20_001);
    assertEquals(0, tasks.size());
    advance(1);
    runTasks();
    assertEquals(List.of(1L, 5L), written);
    assertEquals(0, pendingTimers());
  }

  @Test
  void writesAtOnceAfterAQuietInterval() {
    AckCoalescer acks = coalescer(INTERVAL);
    acks.offer(1);
    runTasks();
    advance(INTERVAL);
    acks.offer(2);
    runTasks();
    assertEquals(List.of(1L, 2L), written);
    assertEquals(0, pendingTimers());
  }

  @Test
  void endingFlushesThePendingCursorAndCancelsTheTimer() {
    AckCoalescer acks = coalescer(INTERVAL);
    acks.offer(1);
    runTasks();
    acks.offer(7);
    assertEquals(1, pendingTimers());
    acks.end();
    assertEquals(0, pendingTimers());
    runTasks();
    assertEquals(List.of(1L, 7L), written);
    // Acks after the end are written at once.
    acks.offer(8);
    runTasks();
    advance(INTERVAL);
    assertEquals(List.of(1L, 7L, 8L), written);
  }

  @Test
  void endingWithNothingPendingWritesNothing() {
    AckCoalescer acks = coalescer(INTERVAL);
    acks.offer(1);
    runTasks();
    acks.end();
    assertEquals(0, tasks.size());
    assertEquals(List.of(1L), written);
  }

  @Test
  void endingDuringAWriteFlushesAfterIt() {
    AckCoalescer acks = coalescer(INTERVAL);
    acks.offer(1);
    during.add(
        () -> {
          acks.offer(4);
          acks.end();
          // The flush waits for the write in flight.
          assertEquals(0, tasks.size());
        });
    runTasks();
    assertEquals(List.of(1L, 4L), written);
    assertEquals(0, pendingTimers());
  }

  @Test
  void neverStartsASecondWriteWhileOneIsInFlight() {
    AckCoalescer acks = coalescer(INTERVAL);
    acks.offer(1);
    during.add(
        () -> {
          acks.offer(3);
          acks.offer(2);
          advance(INTERVAL);
          acks.offer(4);
          assertEquals(0, tasks.size());
          assertEquals(0, pendingTimers());
        });
    runTasks();
    // The write took the whole interval, so the next one is due at once.
    assertEquals(List.of(1L, 4L), written);
  }

  @Test
  void schedulesTheNextWriteFromTheLastStartWhenAcksArriveDuringAWrite() {
    AckCoalescer acks = coalescer(INTERVAL);
    acks.offer(1);
    during.add(
        () -> {
          advance(1000);
          acks.offer(2);
        });
    runTasks();
    assertEquals(List.of(1L), written);
    assertEquals(1, timers.size());
    assertEquals(1_000_000 + INTERVAL, timers.get(0).at());
    advance(INTERVAL);
    runTasks();
    assertEquals(List.of(1L, 2L), written);
  }

  @Test
  void intervalZeroWritesEachAckOneAtATime() {
    AckCoalescer acks = coalescer(0);
    acks.offer(1);
    during.add(
        () -> {
          acks.offer(5);
          acks.offer(3);
          acks.offer(4);
        });
    acks.offer(2);
    assertEquals(1, tasks.size());
    runTasks();
    assertEquals(List.of(1L, 5L), written);
    acks.offer(6);
    assertEquals(1, tasks.size());
    runTasks();
    assertEquals(List.of(1L, 5L, 6L), written);
    assertTrue(timers.isEmpty());
  }
}
