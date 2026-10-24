// Per-connection limits from the contract, and the ack writer.

export const bucketCapacity = 100;
export const bucketRefill = 50; // tokens per second
export const maxInFlight = 32;

/** Frames a connection may send: 50 a second on average, in bursts of 100. */
export class TokenBucket {
  private tokens = bucketCapacity;
  private last: number;
  constructor(now: number) {
    this.last = now;
  }
  take(now: number): boolean {
    if (now > this.last) {
      this.tokens = Math.min(bucketCapacity, this.tokens + ((now - this.last) / 1000) * bucketRefill);
      this.last = now;
    }
    if (this.tokens < 1) return false;
    this.tokens--;
    return true;
  }
}

/**
 * A connection's acks: one write at a time, at most one per interval, the
 * highest cursor pending, and whatever is pending written at once when the
 * connection ends.
 */
export class AckCursor {
  private pending = 0n;
  private started = 0; // when the last write started, 0 before the first
  private running = false; // a write runs, or its timer is set
  private closed = false;
  private timer: NodeJS.Timeout | undefined;
  private readonly interval: number;
  private readonly write: (cursor: bigint) => Promise<void>;
  private readonly clock: () => number;

  constructor(interval: number, write: (cursor: bigint) => Promise<void>, clock: () => number = Date.now) {
    this.interval = interval;
    this.write = write;
    this.clock = clock;
  }

  offer(cursor: bigint): void {
    if (cursor > this.pending) this.pending = cursor;
    if (this.running) return;
    this.running = true;
    void this.run();
  }

  /** Marks the connection ended: what is pending is written without waiting. */
  close(): void {
    this.closed = true;
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = undefined;
      void this.run();
    } else if (!this.running && this.pending > 0n) {
      this.running = true;
      void this.run();
    }
  }

  private async run(): Promise<void> {
    for (;;) {
      if (this.pending === 0n) {
        this.running = false;
        return;
      }
      const now = this.clock();
      const wait = this.started + this.interval - now;
      if (!this.closed && this.started > 0 && wait > 0) {
        this.timer = setTimeout(() => {
          this.timer = undefined;
          void this.run();
        }, wait);
        return;
      }
      const cursor = this.pending;
      this.pending = 0n;
      this.started = now;
      await this.write(cursor);
    }
  }
}
