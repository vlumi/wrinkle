package wrinkle.gateway;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Queue;
import org.junit.jupiter.api.Test;

class AckCoalescerTest {
  private final Queue<Runnable> tasks = new ArrayDeque<>();
  private final List<Long> written = new ArrayList<>();

  @Test
  void writesOneAtATimeKeepingTheHighestPending() {
    List<Runnable> during = new ArrayList<>();
    AckCoalescer acks =
        new AckCoalescer(
            tasks::add,
            cursor -> {
              written.add(cursor);
              during.forEach(Runnable::run);
              during.clear();
            });
    acks.offer(1);
    // Arrive while the first write is in flight.
    during.add(
        () -> {
          acks.offer(5);
          acks.offer(3);
          acks.offer(4);
        });
    acks.offer(2);
    assertEquals(1, tasks.size());
    tasks.poll().run();
    assertEquals(List.of(1L, 5L), written);
    assertEquals(0, tasks.size());
  }

  @Test
  void startsAgainAfterIdle() {
    AckCoalescer acks = new AckCoalescer(tasks::add, written::add);
    acks.offer(1);
    tasks.poll().run();
    acks.offer(2);
    assertEquals(1, tasks.size());
    tasks.poll().run();
    assertEquals(List.of(1L, 2L), written);
  }
}
