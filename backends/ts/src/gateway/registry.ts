// The users with a connection on this gateway, each subscribed to its push
// channel while it has one. Attaching waits for the subscription, so no push
// that follows Connect is missed; subscribing and unsubscribing a user run
// one at a time, so an attach racing the last detach ends subscribed.
import { channelOf, type Presence, type Redis } from "../presence/presence.ts";
import { hex, type UUID } from "../internal/ids.ts";
import { log } from "../internal/log.ts";

interface UserState<C> {
  userId: UUID;
  conns: Set<C>;
  subscribed: boolean;
  busy: boolean;
  waiters: (() => void)[];
  retry: number;
}

export class Registry<C> {
  private readonly users = new Map<string, UserState<C>>();
  private sub: Redis | undefined;
  private readonly deliver: (userId: UUID, message: Buffer, conns: Set<C>) => void;

  constructor(deliver: (userId: UUID, message: Buffer, conns: Set<C>) => void) {
    this.deliver = deliver;
  }

  async start(presence: Presence): Promise<void> {
    this.sub = await presence.subscriber();
  }

  /** Adds the connection and waits, up to the timeout, for its user's subscription. */
  async add(conn: C, userId: UUID, timeout: number): Promise<void> {
    const key = hex(userId);
    let st = this.users.get(key);
    if (!st) {
      st = { userId, conns: new Set(), subscribed: false, busy: false, waiters: [], retry: 100 };
      this.users.set(key, st);
    }
    st.conns.add(conn);
    if (st.subscribed && !st.busy) return;
    const state = st;
    const ready = new Promise<void>((resolve) => state.waiters.push(resolve));
    this.reconcile(state);
    let timer: NodeJS.Timeout | undefined;
    const expired = new Promise<"timeout">((resolve) => (timer = setTimeout(() => resolve("timeout"), timeout)));
    const result = await Promise.race([ready, expired]);
    clearTimeout(timer);
    if (result === "timeout") throw new Error("push subscription timed out");
    if (!state.conns.has(conn)) throw new Error("detached while subscribing");
  }

  remove(conn: C, userId: UUID): void {
    const st = this.users.get(hex(userId));
    if (!st) return;
    st.conns.delete(conn);
    if (st.conns.size === 0) this.reconcile(st);
  }

  private reconcile(st: UserState<C>): void {
    if (st.busy) return;
    st.busy = true;
    void this.apply(st);
  }

  private async apply(st: UserState<C>): Promise<void> {
    const channel = channelOf(st.userId);
    try {
      while (st.conns.size > 0 !== st.subscribed) {
        if (st.conns.size > 0) {
          await this.sub!.subscribe(channel, (message: Buffer) => this.deliver(st.userId, message, st.conns), true);
          st.subscribed = true;
        } else {
          await this.sub!.unsubscribe(channel);
          st.subscribed = false;
        }
      }
      st.busy = false;
      st.retry = 100;
      if (st.subscribed) {
        for (const w of st.waiters.splice(0)) w();
      } else if (this.users.get(hex(st.userId)) === st) {
        this.users.delete(hex(st.userId));
      }
    } catch (err) {
      log.warn("subscription change failed", { user: st.userId, err });
      const delay = st.retry;
      st.retry = Math.min(st.retry * 2, 5_000);
      setTimeout(() => void this.apply(st), delay);
    }
  }

  async close(): Promise<void> {
    await this.sub?.quit().catch(() => undefined);
  }
}
