// Passwords as argon2id in the PHC string format. Hashing runs on the libuv
// thread pool, as many at once as there are cores.
import { randomBytes, timingSafeEqual } from "node:crypto";
import { availableParallelism } from "node:os";
import { hashRaw } from "@node-rs/argon2";
import { unavailable } from "../internal/apierror.ts";
import { deadline } from "../internal/deadline.ts";

// Parameters for new hashes; verification reads them from the stored string.
export const argonMemory = 19456;
export const argonTime = 2;
export const argonThreads = 1;
const argonSaltLen = 16;
const argonKeyLen = 32;
const argonVersion = 19;

export class MalformedHash extends Error {
  constructor() {
    super("malformed password hash");
  }
}

const b64 = (b: Uint8Array) => Buffer.from(b).toString("base64").replace(/=+$/, "");

function fromB64(s: string): Buffer | undefined {
  // Unpadded standard base64 only, as Go's RawStdEncoding.
  if (!/^[A-Za-z0-9+/]*$/.test(s) || s.length % 4 === 1) return undefined;
  const b = Buffer.from(s, "base64");
  return b64(b) === s ? b : undefined;
}

/**
 * Bounds concurrent hashing: each takes about 19 MiB and a core for tens of
 * milliseconds, and a burst of sign-ins must not exhaust memory or hold the
 * thread pool's other work. A wait ends with the request's deadline.
 */
export class HashSlots {
  private free: number;
  private readonly waiting: (() => void)[] = [];

  constructor(size: number) {
    this.free = size;
  }

  async run<T>(fn: () => Promise<T>, signal = deadline()): Promise<T> {
    await this.acquire(signal);
    try {
      return await fn();
    } finally {
      this.release();
    }
  }

  private acquire(signal: AbortSignal | undefined): Promise<void> {
    if (signal?.aborted) return Promise.reject(busy());
    if (this.free > 0) {
      this.free--;
      return Promise.resolve();
    }
    return new Promise((resolve, reject) => {
      const onAbort = () => {
        const i = this.waiting.indexOf(take);
        if (i >= 0) this.waiting.splice(i, 1);
        reject(busy());
      };
      const take = () => {
        signal?.removeEventListener("abort", onAbort);
        resolve();
      };
      this.waiting.push(take);
      signal?.addEventListener("abort", onAbort, { once: true });
    });
  }

  private release(): void {
    const next = this.waiting.shift();
    if (next) next();
    else this.free++;
  }
}

const busy = () => unavailable("password hashing is busy; try again");

export const hashSlots = new HashSlots(availableParallelism());

const key = (password: string, salt: Uint8Array, memory: number, time: number, threads: number, len: number) =>
  hashSlots.run(() =>
    hashRaw(password, { salt, memoryCost: memory, timeCost: time, parallelism: threads, outputLen: len }),
  );

export async function encodeHash(
  password: string,
  salt: Uint8Array,
  memory: number,
  time: number,
  threads: number,
): Promise<string> {
  const k = await key(password, salt, memory, time, threads, argonKeyLen);
  return `$argon2id$v=${argonVersion}$m=${memory},t=${time},p=${threads}$${b64(salt)}$${b64(k)}`;
}

/** The argon2id hash of the password with a fresh salt. */
export const hashPassword = (password: string): Promise<string> =>
  encodeHash(password, randomBytes(argonSaltLen), argonMemory, argonTime, argonThreads);

/** Whether the password matches the PHC string; throws MalformedHash for one it can't read. */
export async function verifyPassword(password: string, encoded: string): Promise<boolean> {
  const parts = encoded.split("$");
  if (parts.length !== 6 || parts[0] !== "" || parts[1] !== "argon2id" || parts[2] !== `v=${argonVersion}`)
    throw new MalformedHash();
  const m = /^m=(\d+),t=(\d+),p=(\d+)$/.exec(parts[3]!);
  if (!m) throw new MalformedHash();
  const [memory, time, threads] = [Number(m[1]), Number(m[2]), Number(m[3])];
  if (`m=${memory},t=${time},p=${threads}` !== parts[3]) throw new MalformedHash();
  // Bounds against a corrupt row costing unbounded memory or time.
  if (memory < 8 || memory > 1 << 21 || time < 1 || time > 16 || threads < 1 || threads > 255)
    throw new MalformedHash();
  const salt = fromB64(parts[4]!);
  const want = fromB64(parts[5]!);
  if (!salt || !want || want.length < 16 || want.length > 64) throw new MalformedHash();
  const got = await key(password, salt, memory, time, threads, want.length);
  return timingSafeEqual(got, want);
}
