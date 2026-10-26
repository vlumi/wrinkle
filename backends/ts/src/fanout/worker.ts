// The fan-out: committed outbox rows, read from Kafka, into entries in the
// event logs of everyone concerned, pushed to connected devices: a
// conversation's events to the recipients its row names, a user's profile
// changes to the user and every direct conversation partner, a user's
// device changes to the user and, for device keys, to everyone in a
// conversation with the user, and a user's contact changes to both users.
import { setTimeout as sleep } from "node:timers/promises";
import { KafkaJS } from "@confluentinc/kafka-javascript";
import { hex } from "../internal/ids.ts";
import type { Logger } from "../internal/log.ts";
import * as metrics from "../internal/metrics.ts";
import { loadShards } from "../internal/shards.ts";
import type { FanoutDeps } from "../services/deps.ts";
import { batchable, Batcher, deliverBatched } from "./batch.ts";
import { deliverContact } from "./contact.ts";
import { Delivery, type Notifier } from "./deliver.ts";
import { deliverDevice } from "./device.ts";
import { eachLimited, Limit } from "./limit.ts";
import { parseOutboxRow, parseUserOutboxRow } from "./outbox.ts";
import { deliverDeletion, deliverProfile } from "./profile.ts";
import { settleUploads } from "./uploads.ts";

// The outbox topics: conversations' rows keyed by conversation id, users'
// by user id.
export const conversationTopic = "wrinkle.conversation.outbox";
export const userTopic = "wrinkle.user.user_outbox";
export const topics = [conversationTopic, userTopic];

const group = "wrinkle-fanout";
/** How many recipients of one row are delivered to at a time. */
const recipientConcurrency = 16;
/** Partitions handled at a time; the outbox topics have 16 each. */
const partitionConcurrency = 32;
/** The most records of one partition handled, and committed, together. */
const maxBatch = 256;

export interface Fanout {
  stop(): Promise<void>;
}

export interface FanoutOptions {
  brokers: string;
  /** How many conversations or users are delivered at a time. */
  concurrency: number;
  notifier?: Notifier;
}

type Consumer = KafkaJS.Consumer;
type KafkaRecord = KafkaJS.KafkaMessage;

/** Connects to the brokers and consumes until stopped. */
export async function startFanout(deps: FanoutDeps, opts: FanoutOptions): Promise<Fanout> {
  const shards = await loadShards(deps.users, "user").catch((err: unknown) => {
    throw new Error(`shards of user: ${(err as Error).message}`, { cause: err });
  });
  const kafka = new KafkaJS.Kafka({
    kafkaJS: { brokers: opts.brokers.split(","), logger: kafkaLogger(deps.log), logLevel: KafkaJS.logLevel.WARN },
  });
  const consumer = kafka.consumer({
    kafkaJS: {
      groupId: group,
      fromBeginning: true,
      autoCommit: false,
      // A worker that dies without leaving holds its partitions until the
      // session expires; keep that short.
      sessionTimeout: 10_000,
      heartbeatInterval: 3_000,
      // The group is shared with the Go and Java workers, which use the
      // cooperative sticky assignor (Java among others); librdkafka's default
      // has none in common with them.
      partitionAssigners: [KafkaJS.PartitionAssigners.cooperativeSticky],
    },
    "js.consumer.max.batch.size": maxBatch,
    // localhost resolves to ::1 first, where the brokers do not listen.
    "broker.address.family": "v4",
  });
  try {
    await consumer.connect();
    await consumer.subscribe({ topics });
  } catch (err) {
    await consumer.disconnect().catch(() => undefined);
    throw new Error(`kafka at ${opts.brokers}: ${(err as Error).message}`, { cause: err });
  }
  const delivery = new Delivery(deps.users, deps.conversations, deps.presence, opts.notifier, deps.log);
  const worker = new Worker(consumer, delivery, new Batcher(deps.users, shards, deps.log), opts.concurrency, deps.log);
  await consumer.run({
    eachBatchAutoResolve: false,
    partitionsConsumedConcurrently: partitionConcurrency,
    eachBatch: (payload) => worker.handleBatch(payload),
  });
  return { stop: () => worker.stop() };
}

class Worker {
  private readonly consumer: Consumer;
  private readonly d: Delivery;
  private readonly batches: Batcher;
  /** Shared by the partitions handled at a time: keys delivered at a time. */
  private readonly keys: Limit;
  private readonly log: Logger;
  private stopping = false;

  constructor(consumer: Consumer, d: Delivery, batches: Batcher, concurrency: number, log: Logger) {
    this.consumer = consumer;
    this.d = d;
    this.batches = batches;
    this.keys = new Limit(concurrency);
    this.log = log;
  }

  /**
   * Handles a partition's batch: keys (conversations, users) in parallel and
   * a key's records in order, which is the ordering the contract promises; a
   * user's log is serialized by its row lock whatever the interleaving. The
   * batch is committed only once every record in it is handled; when one
   * fails, the batch is fetched again, and records already delivered are
   * skipped by the logs' source key.
   */
  async handleBatch(payload: KafkaJS.EachBatchPayload): Promise<void> {
    const { topic, partition, messages } = payload.batch;
    if (messages.length === 0) return;
    const state = { failed: false };
    await Promise.all(
      byKey(messages).map((records) =>
        this.keys.run(async () => {
          for (const rec of records) {
            if (state.failed || this.stopping) {
              state.failed = true;
              return;
            }
            try {
              await this.handleWithRetry(topic, rec);
            } catch (err) {
              state.failed = true;
              this.log.error("giving up on record", { topic, partition, offset: rec.offset, err });
              return;
            }
          }
        }),
      ),
    );
    if (state.failed) {
      // Leaving the batch unresolved seeks the partition back to its start.
      if (!this.stopping) await sleep(1_000);
      return;
    }
    const last = messages[messages.length - 1]!;
    payload.resolveOffset(last.offset);
    // A partition revoked meanwhile is the new owner's to commit; it may
    // redeliver the batch, which the source keys make harmless.
    if (payload.isStale()) return;
    try {
      await this.consumer.commitOffsets([{ topic, partition, offset: String(BigInt(last.offset) + 1n) }]);
    } catch (err) {
      if (!this.stopping) this.log.warn("commit failed", { topic, partition, err });
    }
  }

  private async handleWithRetry(topic: string, rec: KafkaRecord): Promise<void> {
    let last: unknown;
    for (let attempt = 0; attempt < 5; attempt++) {
      try {
        await this.handle(topic, rec);
        return;
      } catch (err) {
        last = err;
        this.log.warn("delivery failed", { attempt: attempt + 1, err });
        if (this.stopping) break;
        await sleep((attempt + 1) * 200);
      }
    }
    throw last;
  }

  private async handle(topic: string, rec: KafkaRecord): Promise<void> {
    const value = rec.value;
    if (!value || value.length === 0) return;
    metrics.fanoutRecords.inc();
    if (topic === userTopic) {
      await this.handleUserRecord(rec, value);
      return;
    }
    let o;
    try {
      o = parseOutboxRow(value);
    } catch (err) {
      // A malformed row never gets better; log it and move on.
      this.log.error("skipping outbox record", { offset: rec.offset, err });
      return;
    }
    if (o.event.body.case === undefined) {
      this.log.error("skipping outbox record without a body", { kind: o.kind, offset: rec.offset });
      return;
    }
    await settleUploads(this.d.db, o);
    if (batchable(o)) {
      await deliverBatched(this.d, this.batches, o);
      return;
    }
    const row = o;
    await eachLimited(o.recipients, recipientConcurrency, async (userId) => {
      try {
        await this.d.deliver(userId, row);
      } catch (err) {
        throw new Error(`deliver to user ${hex(userId)}: ${(err as Error).message}`, { cause: err });
      }
    });
  }

  private async handleUserRecord(rec: KafkaRecord, value: Buffer): Promise<void> {
    let o;
    try {
      o = parseUserOutboxRow(value);
    } catch (err) {
      this.log.error("skipping user outbox record", { offset: rec.offset, err });
      return;
    }
    const body = o.event.body;
    switch (body.case) {
      case "profile":
        if (!body.value.user) break;
        if (body.value.user.deleted) await deliverDeletion(this.d, o);
        else await deliverProfile(this.d, o);
        return;
      case "device":
        if (!body.value.device) break;
        await deliverDevice(this.d, o);
        return;
      case "contact":
        await deliverContact(this.d, o);
        return;
      case "palette":
        // To the user's own devices only.
        try {
          await this.d.append(o.userId, o.userId, o.outboxId, o.event);
        } catch (err) {
          throw new Error(`deliver to user ${hex(o.userId)}: ${(err as Error).message}`, { cause: err });
        }
        return;
      default:
        break;
    }
    this.log.error("skipping user outbox record without a known body", { kind: o.kind, offset: rec.offset });
  }

  /** Leaves the consumer group; batches in flight end without committing what they did not finish. */
  async stop(): Promise<void> {
    this.stopping = true;
    await this.consumer
      .disconnect()
      .catch((err: unknown) => this.log.warn("leaving the consumer group failed", { err }));
  }
}

/** Groups records by key, the conversation or user id, keeping each group in offset order. */
export function byKey<T extends { key: Buffer | null }>(records: T[]): T[][] {
  const groups = new Map<string, T[]>();
  for (const rec of records) {
    const key = rec.key?.toString("latin1") ?? "";
    let g = groups.get(key);
    if (!g) {
      g = [];
      groups.set(key, g);
    }
    g.push(rec);
  }
  return [...groups.values()];
}

function kafkaLogger(log: Logger): KafkaJS.Logger {
  const l = log.with({ component: "kafka" });
  let level = KafkaJS.logLevel.WARN;
  const logger: KafkaJS.Logger = {
    error: (message, extra) => {
      if (level >= KafkaJS.logLevel.ERROR) l.error(message, flat(extra));
    },
    warn: (message, extra) => {
      if (level >= KafkaJS.logLevel.WARN) l.warn(message, flat(extra));
    },
    info: (message, extra) => {
      if (level >= KafkaJS.logLevel.INFO) l.info(message, flat(extra));
    },
    debug: (message, extra) => {
      if (level >= KafkaJS.logLevel.DEBUG) l.debug(message, flat(extra));
    },
    namespace: () => logger,
    setLogLevel: (to) => {
      level = to;
    },
  };
  return logger;
}

const flat = (extra: object | undefined): Record<string, unknown> => {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(extra ?? {})) {
    if (v === undefined || v === null || typeof v === "object") continue;
    out[k] = v;
  }
  return out;
};
