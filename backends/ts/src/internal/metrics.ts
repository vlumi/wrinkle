// The metrics every backend exposes under the same names (docs/metrics.md).
import client from "prom-client";

export const registry = new client.Registry();
client.collectDefaultMetrics({ register: registry });

export const connections = new client.Gauge({
  name: "wrinkle_connections",
  help: "Open WebSocket connections on this gateway.",
  registers: [registry],
});
export const requests = new client.Counter({
  name: "wrinkle_requests_total",
  help: "Requests handled, by type and result (ok or an error code).",
  labelNames: ["type", "code"],
  registers: [registry],
});
export const requestDuration = new client.Histogram({
  name: "wrinkle_request_duration_seconds",
  help: "Time from reading a request to queueing its response.",
  labelNames: ["type"],
  buckets: client.exponentialBuckets(0.0005, 2, 16),
  registers: [registry],
});
export const eventsPushed = new client.Counter({
  name: "wrinkle_events_pushed_total",
  help: "Events written to connected devices.",
  registers: [registry],
});
export const fanoutRecords = new client.Counter({
  name: "wrinkle_fanout_records_total",
  help: "Outbox records consumed from Kafka.",
  registers: [registry],
});
export const fanoutDeliveries = new client.Counter({
  name: "wrinkle_fanout_deliveries_total",
  help: "Events appended to a user's log.",
  registers: [registry],
});
export const pushSent = new client.Counter({
  name: "wrinkle_push_sent_total",
  help: "Push notifications to devices not connected, by result: ok, unregistered, error, dropped.",
  labelNames: ["result"],
  registers: [registry],
});
export const fanoutLag = new client.Histogram({
  name: "wrinkle_fanout_lag_seconds",
  help: "Time from an event being stored to its append to a user's log.",
  buckets: client.exponentialBuckets(0.001, 2, 16),
  registers: [registry],
});
export const shardRounds = new client.Counter({
  name: "wrinkle_shard_rounds_total",
  help: "Rounds of per-shard work, by job, shard and result: ok, error, skipped (leased elsewhere), lost (lease lost midway).",
  labelNames: ["job", "shard", "result"],
  registers: [registry],
});
export const shardRoundDuration = new client.Histogram({
  name: "wrinkle_shard_round_duration_seconds",
  help: "Time of the rounds this instance held the lease for, by job.",
  labelNames: ["job"],
  buckets: client.exponentialBuckets(0.001, 2, 18),
  registers: [registry],
});
export const shardWork = new client.Counter({
  name: "wrinkle_shard_work_total",
  help: "What the per-shard rounds did, by job: events trimmed, messages moved.",
  labelNames: ["job"],
  registers: [registry],
});
export const coldReadDuration = new client.Histogram({
  name: "wrinkle_cold_read_duration_seconds",
  help: "Reads from the cold tier, by kind: range (a page of history) or message (one message).",
  labelNames: ["kind"],
  buckets: client.exponentialBuckets(0.0005, 2, 16),
  registers: [registry],
});
