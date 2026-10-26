package wrinkle;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.fail;

import java.time.Duration;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import org.junit.jupiter.api.Test;
import org.slf4j.helpers.NOPLogger;

class ShardWorkTest {
  /** Leases for one instance; others take them by holding the name. */
  static final class FakeLeases implements ShardWork.Leases {
    final Map<String, String> held = new HashMap<>();
    final String me;

    FakeLeases(String me) {
      this.me = me;
    }

    @Override
    public synchronized boolean takeLease(String name, Duration ttl) {
      return held.putIfAbsent(name, me) == null;
    }

    @Override
    public synchronized boolean renewLease(String name, Duration ttl) {
      return me.equals(held.get(name));
    }

    @Override
    public synchronized void dropLease(String name, Duration deadline) {
      held.remove(name, me);
    }

    synchronized void give(String name, String to) {
      held.put(name, to);
    }

    synchronized Map<String, String> snapshot() {
      return Map.copyOf(held);
    }
  }

  private static ShardWork job(FakeLeases leases, ShardWork.Round round) {
    return new ShardWork(
        "work",
        "work",
        "ks",
        List.of(),
        Duration.ofHours(1),
        "items",
        round,
        leases,
        NOPLogger.NOP_LOGGER,
        Duration.ofMillis(20));
  }

  /** The test job's rounds counted with the result. */
  private static double counted(String shard, String result) {
    return Metrics.SHARD_ROUNDS.labelValues("work", shard, result).get();
  }

  private static double work() {
    return Metrics.SHARD_WORK.labelValues("work").get();
  }

  private static ShardPools.Shard shard(String name) {
    return new ShardPools.Shard(name, null);
  }

  @Test
  void leasedRoundRunsOnceAndDropsTheLease() throws Exception {
    double ok = counted("-80", "ok");
    double work = work();
    FakeLeases leases = new FakeLeases("a");
    AtomicInteger rounds = new AtomicInteger();
    ShardWork j =
        job(
            leases,
            (s, stopped) -> {
              rounds.incrementAndGet();
              return 3;
            });
    assertEquals(3, j.leased(shard("-80")));
    assertEquals(1, rounds.get());
    assertEquals(Map.of(), leases.snapshot());
    assertEquals(ok + 1, counted("-80", "ok"), "round not counted");
    assertEquals(work + 3, work(), "work not counted");
  }

  @Test
  void shardLeasedElsewhereIsSkipped() throws Exception {
    double skipped = counted("-80", "skipped");
    FakeLeases leases = new FakeLeases("a");
    leases.give("work:ks/-80", "b");
    ShardWork j =
        job(
            leases,
            (s, stopped) -> {
              if (s.name().equals("-80")) {
                fail("ran a shard another instance holds");
              }
              return 0;
            });
    assertEquals(0, j.leased(shard("-80")));
    assertEquals(skipped + 1, counted("-80", "skipped"), "skipped round not counted");
    assertEquals(0, j.leased(shard("80-")));
    assertEquals(Map.of("work:ks/-80", "b"), leases.snapshot());
  }

  @Test
  void losingTheLeaseStopsTheRound() throws Exception {
    double lost = counted("-", "lost");
    FakeLeases leases = new FakeLeases("a");
    ShardWork j =
        job(
            leases,
            (s, stopped) -> {
              leases.give("work:ks/-", "b");
              long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5);
              while (!stopped.getAsBoolean()) {
                if (System.nanoTime() > deadline) {
                  fail("round went on after its lease was lost");
                }
                try {
                  Thread.sleep(5);
                } catch (InterruptedException e) {
                  throw new IllegalStateException(e);
                }
              }
              return 1;
            });
    assertEquals(1, j.leased(shard("-")));
    assertEquals("b", leases.snapshot().get("work:ks/-"), "dropped another instance's lease");
    assertEquals(lost + 1, counted("-", "lost"), "lost round not counted");
  }
}
