// Presence in Valkey: which instance holds each connected device, whether
// an instance is alive, and a pub/sub channel per user for pushes.
import { randomBytes } from "node:crypto";
import { createClient } from "redis";
import { fromHex, hex, type UUID } from "../internal/ids.ts";

export const heartbeatInterval = 30_000;
const instanceTTL = 90;
const channelPrefix = "push:";

// Removes a device's entry only if it still names this instance, so a device
// that reconnected elsewhere keeps its new entry.
const release = `if redis.call('HGET', KEYS[1], ARGV[1]) == ARGV[2] then return redis.call('HDEL', KEYS[1], ARGV[1]) end return 0`;

const newClient = (addr: string) => createClient({ url: `redis://${addr}` });
export type Redis = ReturnType<typeof newClient>;

export const channelOf = (userId: UUID): string => channelPrefix + hex(userId);
export const userOf = (channel: string): UUID | undefined =>
  channel.startsWith(channelPrefix) ? fromHex(channel.slice(channelPrefix.length)) : undefined;
const presenceKey = (userId: UUID) => "presence:" + hex(userId);
const instanceKey = (instance: string) => "gateway:" + instance;

export class Presence {
  readonly instance: string;
  readonly redis: Redis;

  private constructor(redis: Redis, node: string) {
    this.redis = redis;
    this.instance = `${node}/${randomBytes(8).toString("hex")}`;
  }

  static async connect(addr: string, node: string): Promise<Presence> {
    const redis = newClient(addr);
    redis.on("error", () => undefined); // reconnects by itself; commands fail meanwhile
    try {
      await redis.connect();
    } catch (err) {
      throw new Error(`valkey at ${addr}: ${(err as Error).message}`, { cause: err });
    }
    return new Presence(redis, node);
  }

  async publish(userId: UUID, payload: Uint8Array): Promise<void> {
    await this.redis.publish(channelOf(userId), Buffer.from(payload));
  }

  /** A connection of its own for subscriptions. */
  async subscriber(): Promise<Redis> {
    const sub = this.redis.duplicate();
    sub.on("error", () => undefined);
    await sub.connect();
    return sub;
  }

  async announce(): Promise<void> {
    await this.redis.set(instanceKey(this.instance), "1", { EX: instanceTTL });
  }

  async withdraw(): Promise<void> {
    await this.redis.del(instanceKey(this.instance));
  }

  async deviceConnected(userId: UUID, deviceId: bigint): Promise<void> {
    await this.redis.hSet(presenceKey(userId), deviceId.toString(), this.instance);
  }

  async deviceDisconnected(userId: UUID, deviceId: bigint): Promise<void> {
    await this.redis.eval(release, { keys: [presenceKey(userId)], arguments: [deviceId.toString(), this.instance] });
  }

  /** The user's devices connected to a live instance; entries of dead instances are removed. */
  async connectedDevices(userId: UUID): Promise<Set<bigint>> {
    const key = presenceKey(userId);
    const entries = await this.redis.hGetAll(key);
    const instances = [...new Set(Object.values(entries))];
    const alive = new Map<string, boolean>();
    if (instances.length > 0) {
      const counts = await Promise.all(instances.map((i) => this.redis.exists(instanceKey(i))));
      instances.forEach((i, n) => alive.set(i, counts[n]! > 0));
    }
    const out = new Set<bigint>();
    for (const [field, instance] of Object.entries(entries)) {
      if (!alive.get(instance)) {
        await this.redis.eval(release, { keys: [key], arguments: [field, instance] }).catch(() => undefined);
        continue;
      }
      out.add(BigInt(field));
    }
    return out;
  }

  async close(): Promise<void> {
    await this.redis.quit().catch(() => undefined);
  }
}
