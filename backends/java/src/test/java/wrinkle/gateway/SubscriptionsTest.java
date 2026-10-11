package wrinkle.gateway;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Queue;
import java.util.concurrent.CompletableFuture;
import org.junit.jupiter.api.Test;

class SubscriptionsTest {
  private final Queue<Runnable> workers = new ArrayDeque<>();
  private final List<String> calls = new ArrayList<>();

  @Test
  void subscribesOnceForSeveralConnections() {
    Subscriptions<Integer> subs =
        new Subscriptions<>(u -> calls.add("sub " + u), u -> calls.add("unsub " + u), workers::add);
    CompletableFuture<Void> first = subs.add(1);
    assertFalse(first.isDone());
    workers.poll().run();
    assertTrue(first.isDone());
    assertTrue(subs.add(1).isDone());
    subs.remove(1);
    assertTrue(workers.isEmpty());
    subs.remove(1);
    workers.poll().run();
    assertEquals(List.of("sub 1", "unsub 1"), calls);
  }

  @Test
  void anAttachDuringTheLastDetachEndsSubscribed() {
    List<Runnable> during = new ArrayList<>();
    List<CompletableFuture<Void>> attached = new ArrayList<>();
    @SuppressWarnings({"unchecked", "rawtypes"})
    Subscriptions<Integer>[] subs = new Subscriptions[1];
    subs[0] =
        new Subscriptions<>(
            u -> calls.add("sub " + u),
            u -> {
              calls.add("unsub " + u);
              during.forEach(Runnable::run);
              during.clear();
            },
            workers::add);
    subs[0].add(1);
    workers.poll().run();
    subs[0].remove(1);
    // A new connection attaches while the unsubscribe is in flight.
    during.add(() -> attached.add(subs[0].add(1)));
    workers.poll().run();
    assertEquals(List.of("sub 1", "unsub 1", "sub 1"), calls);
    assertTrue(attached.getFirst().isDone());
    assertTrue(workers.isEmpty());
  }

  @Test
  void retriesAFailedSubscribe() {
    int[] failures = {1};
    Subscriptions<Integer> subs =
        new Subscriptions<>(
            u -> {
              if (failures[0]-- > 0) {
                throw new IllegalStateException("down");
              }
              calls.add("sub " + u);
            },
            u -> calls.add("unsub " + u),
            workers::add);
    CompletableFuture<Void> ready = subs.add(1);
    workers.poll().run();
    assertTrue(ready.isDone());
    assertEquals(List.of("sub 1"), calls);
  }

  @Test
  void aDetachBeforeTheSubscribeLandsUnsubscribes() {
    List<Runnable> during = new ArrayList<>();
    @SuppressWarnings({"unchecked", "rawtypes"})
    Subscriptions<Integer>[] subs = new Subscriptions[1];
    subs[0] =
        new Subscriptions<>(
            u -> {
              calls.add("sub " + u);
              during.forEach(Runnable::run);
              during.clear();
            },
            u -> calls.add("unsub " + u),
            workers::add);
    subs[0].add(1);
    during.add(() -> subs[0].remove(1));
    workers.poll().run();
    assertEquals(List.of("sub 1", "unsub 1"), calls);
    assertTrue(workers.isEmpty());
  }
}
