package wrinkle.gateway;

import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.Executor;
import java.util.concurrent.TimeUnit;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * One push subscription per user with a connection here. Adding and removing a connection only
 * changes a count; a worker per user, one at a time, issues SUBSCRIBE or UNSUBSCRIBE until what is
 * applied matches whether the user has connections, retrying with backoff.
 */
final class Subscriptions {
  private static final Logger log = LoggerFactory.getLogger(Subscriptions.class);
  private static final long MIN_BACKOFF_MILLIS = 100;
  private static final long MAX_BACKOFF_MILLIS = 5000;

  /** A SUBSCRIBE or UNSUBSCRIBE, which blocks until Valkey confirms it. */
  interface Op {
    void apply(long userId) throws Exception;
  }

  private static final class State {
    int connections;
    boolean subscribed;
    boolean reconciling;
    CompletableFuture<Void> ready = new CompletableFuture<>();
  }

  private final Op subscribe;
  private final Op unsubscribe;
  private final Executor workers;
  // Guarded by this.
  private final Map<Long, State> states = new HashMap<>();
  private boolean stopped;

  Subscriptions(Op subscribe, Op unsubscribe, Executor workers) {
    this.subscribe = subscribe;
    this.unsubscribe = unsubscribe;
    this.workers = workers;
  }

  /** Counts a connection of the user's; completes once the user's channel is subscribed. */
  CompletableFuture<Void> add(long userId) {
    State s;
    synchronized (this) {
      s = states.computeIfAbsent(userId, k -> new State());
      s.connections++;
      if (s.subscribed && !s.reconciling) {
        return CompletableFuture.completedFuture(null);
      }
      if (s.ready.isDone()) {
        s.ready = new CompletableFuture<>();
      }
      if (!startReconciling(s)) {
        return s.ready;
      }
    }
    CompletableFuture<Void> ready = s.ready;
    workers.execute(() -> reconcile(userId, s));
    return ready;
  }

  /** Uncounts a connection of the user's; the last one leaving unsubscribes. */
  void remove(long userId) {
    State s;
    synchronized (this) {
      s = states.get(userId);
      if (s == null || --s.connections > 0 || !startReconciling(s)) {
        return;
      }
    }
    workers.execute(() -> reconcile(userId, s));
  }

  /** Ends the workers after their current call; for shutdown. */
  synchronized void stop() {
    stopped = true;
  }

  // Guarded by this.
  private boolean startReconciling(State s) {
    if (s.reconciling) {
      return false;
    }
    s.reconciling = true;
    return true;
  }

  private void reconcile(long userId, State s) {
    long backoff = MIN_BACKOFF_MILLIS;
    while (true) {
      boolean want;
      synchronized (this) {
        want = s.connections > 0;
        if (stopped || want == s.subscribed) {
          s.reconciling = false;
          if (want == s.subscribed) {
            if (want) {
              s.ready.complete(null);
            } else {
              states.remove(userId, s);
            }
          }
          return;
        }
      }
      try {
        (want ? subscribe : unsubscribe).apply(userId);
      } catch (Exception e) {
        log.warn(
            "{} of user {} failed: {}", want ? "subscribe" : "unsubscribe", userId, e.toString());
        try {
          TimeUnit.MILLISECONDS.sleep(backoff);
        } catch (InterruptedException ie) {
          Thread.currentThread().interrupt();
          synchronized (this) {
            s.reconciling = false;
          }
          return;
        }
        backoff = Math.min(backoff * 2, MAX_BACKOFF_MILLIS);
        continue;
      }
      backoff = MIN_BACKOFF_MILLIS;
      synchronized (this) {
        s.subscribed = want;
      }
    }
  }
}
