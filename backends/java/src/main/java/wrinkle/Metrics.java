package wrinkle;

import io.prometheus.metrics.core.metrics.Counter;
import io.prometheus.metrics.core.metrics.Gauge;
import io.prometheus.metrics.core.metrics.Histogram;
import io.prometheus.metrics.instrumentation.jvm.JvmMetrics;

/** The metrics every backend exposes, with the names in docs/metrics.md. */
public final class Metrics {
  private Metrics() {}

  public static final Gauge CONNECTIONS =
      Gauge.builder()
          .name("wrinkle_connections")
          .help("Open WebSocket connections on this gateway.")
          .register();

  public static final Counter REQUESTS =
      Counter.builder()
          .name("wrinkle_requests")
          .help("Requests handled, by type and result (ok or an error code).")
          .labelNames("type", "code")
          .register();

  public static final Histogram REQUEST_DURATION =
      Histogram.builder()
          .name("wrinkle_request_duration_seconds")
          .help("Time from reading a request to queueing its response.")
          .labelNames("type")
          .classicExponentialUpperBounds(0.0005, 2, 16)
          .register();

  public static final Counter EVENTS_PUSHED =
      Counter.builder()
          .name("wrinkle_events_pushed")
          .help("Events written to connected devices.")
          .register();

  public static final Counter FANOUT_RECORDS =
      Counter.builder()
          .name("wrinkle_fanout_records")
          .help("Outbox records consumed from Kafka.")
          .register();

  public static final Counter FANOUT_DELIVERIES =
      Counter.builder()
          .name("wrinkle_fanout_deliveries")
          .help("Events appended to a user's log.")
          .register();

  public static final Histogram FANOUT_LAG =
      Histogram.builder()
          .name("wrinkle_fanout_lag_seconds")
          .help("Time from an event being stored to its append to a user's log.")
          .classicExponentialUpperBounds(0.001, 2, 16)
          .register();

  public static final Counter PUSH_SENT =
      Counter.builder()
          .name("wrinkle_push_sent")
          .help(
              "Push notifications to devices not connected, by result: ok, unregistered, error,"
                  + " dropped.")
          .labelNames("result")
          .register();

  public static final Counter SHARD_ROUNDS =
      Counter.builder()
          .name("wrinkle_shard_rounds")
          .help(
              "Rounds of per-shard work, by job, shard and result: ok, error, skipped (leased"
                  + " elsewhere), lost (lease lost midway).")
          .labelNames("job", "shard", "result")
          .register();

  public static final Histogram SHARD_ROUND_DURATION =
      Histogram.builder()
          .name("wrinkle_shard_round_duration_seconds")
          .help("Time of the rounds this instance held the lease for, by job.")
          .labelNames("job")
          .classicExponentialUpperBounds(0.001, 2, 18)
          .register();

  public static final Counter SHARD_WORK =
      Counter.builder()
          .name("wrinkle_shard_work")
          .help("What the per-shard rounds did, by job: events trimmed, messages moved.")
          .labelNames("job")
          .register();

  public static final Histogram COLD_READ_DURATION =
      Histogram.builder()
          .name("wrinkle_cold_read_duration_seconds")
          .help(
              "Reads from the cold tier, by kind: range (a page of history) or message (one"
                  + " message).")
          .labelNames("kind")
          .classicExponentialUpperBounds(0.0005, 2, 16)
          .register();

  public static void init() {
    JvmMetrics.builder().register();
  }
}
