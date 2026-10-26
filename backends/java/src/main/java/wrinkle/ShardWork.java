package wrinkle;

import java.sql.SQLException;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.function.BooleanSupplier;
import org.slf4j.Logger;

/**
 * Background work over every shard of a keyspace, each shard on its own virtual thread, shared
 * among the instances that run it: a shard's round needs its lease, so one instance works it at a
 * time and the others take the rest.
 */
public final class ShardWork {
  // Bounds how long a stopped instance keeps its shards; a round renews its lease at a third of it.
  static final Duration LEASE_TTL = Duration.ofSeconds(30);
  private static final Duration DROP_DEADLINE = Duration.ofSeconds(5);
  private static final Duration FIRST_ROUND = Duration.ofMinutes(1);

  /** Leases held by name, for this instance. */
  public interface Leases {
    /** Takes the lease for ttl unless another instance holds it; true if taken. */
    boolean takeLease(String name, Duration ttl);

    /** Extends a lease this instance holds; true if it still did. */
    boolean renewLease(String name, Duration ttl);

    /** Gives up a lease this instance holds, waiting at most the deadline. */
    void dropLease(String name, Duration deadline);
  }

  /** What is due on a shard: returns how much it did, stopping early once stopped says so. */
  public interface Round {
    int run(ShardPools.Shard shard, BooleanSupplier stopped) throws SQLException;
  }

  private final String name;
  private final String metric;
  private final String keyspace;
  private final List<ShardPools.Shard> shards;
  private final Duration interval;
  private final String unit;
  private final Round round;
  private final Leases leases;
  private final Logger log;
  private final Duration leaseTtl;
  private final CountDownLatch stopping = new CountDownLatch(1);
  private final List<Thread> threads = new ArrayList<>();

  /**
   * A job named name over the shards, each shard's rounds interval apart, counting in unit; metric
   * labels its metrics.
   */
  public ShardWork(
      String name,
      String metric,
      String keyspace,
      List<ShardPools.Shard> shards,
      Duration interval,
      String unit,
      Round round,
      Leases leases,
      Logger log) {
    this(name, metric, keyspace, shards, interval, unit, round, leases, log, LEASE_TTL);
  }

  ShardWork(
      String name,
      String metric,
      String keyspace,
      List<ShardPools.Shard> shards,
      Duration interval,
      String unit,
      Round round,
      Leases leases,
      Logger log,
      Duration leaseTtl) {
    this.name = name;
    this.metric = metric;
    this.keyspace = keyspace;
    this.shards = shards;
    this.interval = interval;
    this.unit = unit;
    this.round = round;
    this.leases = leases;
    this.log = log;
    this.leaseTtl = leaseTtl;
  }

  /** Starts every shard's loop. A shard's first round is after a minute, or the interval. */
  public void start() {
    for (ShardPools.Shard shard : shards) {
      threads.add(
          Thread.ofVirtual()
              .name(name + " " + keyspace + "/" + shard.name())
              .start(() -> loop(shard)));
    }
  }

  /** Stops the loops; a round under way stops at its next step and drops its lease. */
  public void stop() {
    stopping.countDown();
  }

  /** Waits up to the time given for the loops to end; false if some have not. */
  public boolean awaitStopped(long millis) throws InterruptedException {
    long deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(millis);
    for (Thread t : threads) {
      long left = deadline - System.nanoTime();
      if (left <= 0 || !t.join(Duration.ofNanos(left))) {
        return false;
      }
    }
    return true;
  }

  private boolean stopped() {
    return stopping.getCount() == 0;
  }

  private void loop(ShardPools.Shard shard) {
    // The shard shows before its first round ends.
    Metrics.SHARD_ROUNDS.labelValues(metric, shard.name(), "ok").inc(0);
    Duration delay = FIRST_ROUND.compareTo(interval) < 0 ? FIRST_ROUND : interval;
    try {
      while (!stopping.await(delay.toMillis(), TimeUnit.MILLISECONDS)) {
        try {
          int n = leased(shard);
          if (n > 0) {
            log.info("{} {}/{}: {} {}", name, keyspace, shard.name(), n, unit);
          }
        } catch (SQLException | RuntimeException e) {
          if (!stopped()) {
            log.warn("{} {}/{} failed", name, keyspace, shard.name(), e);
          }
        }
        delay = interval;
      }
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
    }
  }

  /**
   * Runs a round under the shard's lease, if this instance gets it. Losing the lease midway, or
   * failing to renew it for as long as it lasts, stops the round; whoever took it goes on.
   */
  int leased(ShardPools.Shard shard) throws SQLException {
    String lease = name + ":" + keyspace + "/" + shard.name();
    boolean taken;
    try {
      taken = leases.takeLease(lease, leaseTtl);
    } catch (RuntimeException e) {
      Metrics.SHARD_ROUNDS.labelValues(metric, shard.name(), "error").inc();
      throw e;
    }
    if (!taken) {
      Metrics.SHARD_ROUNDS.labelValues(metric, shard.name(), "skipped").inc();
      return 0;
    }
    long start = System.nanoTime();
    Duration renewEvery = leaseTtl.dividedBy(3);
    AtomicBoolean lost = new AtomicBoolean();
    CountDownLatch done = new CountDownLatch(1);
    Thread renewer =
        Thread.ofVirtual()
            .start(
                () -> {
                  long renewedAt = start;
                  try {
                    while (!done.await(renewEvery.toMillis(), TimeUnit.MILLISECONDS)) {
                      try {
                        if (!leases.renewLease(lease, leaseTtl)) {
                          lost.set(true);
                          return;
                        }
                        renewedAt = System.nanoTime();
                      } catch (RuntimeException e) {
                        // Tried again at the next renewal, unless unanswered for as long as the
                        // lease lasts: then it may be another's.
                        if (System.nanoTime() - renewedAt >= leaseTtl.toNanos()) {
                          lost.set(true);
                          return;
                        }
                      }
                    }
                  } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                  }
                });
    int n = 0;
    boolean failed = true;
    try {
      n = round.run(shard, () -> lost.get() || stopped());
      failed = false;
      return n;
    } finally {
      done.countDown();
      try {
        renewer.join();
      } catch (InterruptedException e) {
        Thread.currentThread().interrupt();
      }
      Metrics.SHARD_ROUND_DURATION.labelValues(metric).observe((System.nanoTime() - start) / 1e9);
      Metrics.SHARD_WORK.labelValues(metric).inc(n);
      String result = lost.get() ? "lost" : failed ? "error" : "ok";
      Metrics.SHARD_ROUNDS.labelValues(metric, shard.name(), result).inc();
      try {
        leases.dropLease(lease, DROP_DEADLINE);
      } catch (RuntimeException e) {
        // It expires on its own.
      }
    }
  }
}
