// Bounded concurrency with promises.

/** A counting semaphore: run waits for one of n slots. */
export class Limit {
  private free: number;
  private readonly waiters: (() => void)[] = [];

  constructor(n: number) {
    this.free = Math.max(n, 1);
  }

  async run<T>(fn: () => Promise<T>): Promise<T> {
    if (this.free > 0) this.free--;
    else await new Promise<void>((resolve) => this.waiters.push(resolve));
    try {
      return await fn();
    } finally {
      const next = this.waiters.shift();
      if (next) next();
      else this.free++;
    }
  }
}

/**
 * Runs fn for each item, at most n at a time. After the first failure no
 * more are started; it throws that failure once those running have ended.
 */
export async function eachLimited<T>(items: Iterable<T>, n: number, fn: (item: T) => Promise<void>): Promise<void> {
  const it = items[Symbol.iterator]();
  let failure: { err: unknown } | undefined;
  const lane = async () => {
    for (let next = it.next(); !next.done && !failure; next = it.next()) {
      try {
        await fn(next.value);
      } catch (err) {
        failure ??= { err };
      }
    }
  };
  await Promise.all(Array.from({ length: Math.max(n, 1) }, lane));
  if (failure) throw failure.err;
}
