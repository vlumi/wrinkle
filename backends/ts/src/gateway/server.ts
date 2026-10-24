// The WebSocket gateway: connections, pushes from Valkey to them, presence,
// keepalive and drain.
import type { IncomingMessage } from "node:http";
import type { Duplex } from "node:stream";
import { fromBinary } from "@bufbuild/protobuf";
import { WebSocketServer } from "ws";
import { PushSchema } from "../gen/wrinkle/backend/v1/services_pb.js";
import type { UUID } from "../internal/ids.ts";
import type { Logger } from "../internal/log.ts";
import * as metrics from "../internal/metrics.ts";
import { heartbeatInterval, type Presence } from "../presence/presence.ts";
import type { Backend } from "../services/backend.ts";
import { close, Conn, subscribeTimeout } from "./conn.ts";
import { Registry } from "./registry.ts";

const maxFrameBytes = 64 * 1024;
const idleTimeout = 90_000;
// A drain gives requests in flight this long, then closing handshakes this
// long more, before dropping the connections that remain.
const requestGrace = 5_000;
const closeGrace = 3_000;

export class Server {
  readonly backend: Backend;
  readonly log: Logger;
  readonly ackInterval: number;
  private readonly presence: Presence;
  private readonly registry: Registry<Conn>;
  private readonly wss = new WebSocketServer({ noServer: true, maxPayload: maxFrameBytes, perMessageDeflate: false });
  private readonly conns = new Set<Conn>();
  private readonly inFlight = new Set<Promise<unknown>>();
  private keepalive: NodeJS.Timeout | undefined;
  draining = false;

  constructor(backend: Backend, presence: Presence, ackInterval: number, log: Logger) {
    this.backend = backend;
    this.presence = presence;
    this.ackInterval = ackInterval;
    this.log = log;
    this.registry = new Registry<Conn>((userId, message, conns) => this.deliver(userId, message, conns));
  }

  /** Announces the instance in presence and subscribes for pushes. */
  async start(): Promise<void> {
    await this.presence.announce();
    await this.registry.start(this.presence);
    this.log.info("gateway instance", { instance: this.presence.instance });
    this.keepalive = setInterval(() => void this.heartbeat(), heartbeatInterval);
  }

  /** Takes an HTTP upgrade to /v1/ws. */
  upgrade(req: IncomingMessage, socket: Duplex, head: Buffer): void {
    if (this.draining) {
      socket.end("HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\n\r\nshutting down");
      return;
    }
    this.wss.handleUpgrade(req, socket, head, (ws) => {
      const conn = new Conn(this, ws, `${req.socket.remoteAddress ?? ""}:${req.socket.remotePort ?? ""}`);
      this.conns.add(conn);
      metrics.connections.inc();
      if (this.draining) conn.close(close.goingAway, "shutting down");
    });
  }

  untrack(conn: Conn): void {
    if (this.conns.delete(conn)) metrics.connections.dec();
  }

  /** Counts work a drain waits for: requests in flight and ack writes. */
  track<T>(p: Promise<T>): Promise<T> {
    this.inFlight.add(p);
    void p.finally(() => this.inFlight.delete(p)).catch(() => undefined);
    return p;
  }

  async attach(conn: Conn): Promise<void> {
    try {
      await this.registry.add(conn, conn.userId!, subscribeTimeout);
    } catch (err) {
      this.registry.remove(conn, conn.userId!);
      this.log.warn("subscribing failed", { user: conn.userId!, err });
      throw err;
    }
    await this.presence
      .deviceConnected(conn.userId!, conn.deviceId)
      .catch((err: unknown) => this.log.warn("presence update failed", { err }));
  }

  detach(conn: Conn): void {
    this.registry.remove(conn, conn.userId!);
    void this.presence
      .deviceDisconnected(conn.userId!, conn.deviceId)
      .catch((err: unknown) => this.log.warn("presence update failed", { err }));
  }

  /** A push on a user's channel: an event for every connection, or a sign-out. */
  private deliver(userId: UUID, message: Buffer, conns: Set<Conn>): void {
    const event = eventOf(message);
    if (event) {
      for (const c of conns) if (c.connected) c.pushEvent(event);
      return;
    }
    let push;
    try {
      push = fromBinary(PushSchema, message);
    } catch (err) {
      this.log.warn("undecodable push", { user: userId, err });
      return;
    }
    if (push.kind.case === "event") {
      for (const c of conns) if (c.connected) c.pushEvent(push.kind.value);
    } else if (push.kind.case === "signedOut") {
      const { allDevices, deviceIds } = push.kind.value;
      for (const c of conns) {
        if (allDevices || deviceIds.includes(c.deviceId)) c.close(close.signedOut, "signed out");
      }
    }
  }

  private async heartbeat(): Promise<void> {
    await this.presence.announce().catch((err: unknown) => this.log.warn("presence heartbeat failed", { err }));
    const cutoff = Date.now() - idleTimeout;
    for (const c of this.conns) {
      if (c.lastSeen < cutoff) c.close(close.goingAway, "idle");
      else c.ping();
    }
  }

  /** Refuses new connections, closes the open ones and lets requests in flight finish. */
  async drain(): Promise<void> {
    this.draining = true;
    clearInterval(this.keepalive);
    this.log.info("draining", { connections: this.conns.size });
    for (const c of this.conns) c.close(close.goingAway, "shutting down");
    if (!(await settle(() => this.inFlight.size === 0, requestGrace))) {
      this.log.warn("requests still in flight after the grace period");
    }
    if (!(await settle(() => this.conns.size === 0, closeGrace))) {
      for (const c of this.conns) c.terminate();
    }
    await this.presence.withdraw().catch((err: unknown) => this.log.warn("withdrawing the instance failed", { err }));
    await this.registry.close();
  }
}

/** The event of a push that is only an event, as the fan-out writes them, or undefined. */
export function eventOf(message: Buffer): Buffer | undefined {
  if (message[0] !== 0x0a) return undefined;
  let len = 0;
  let shift = 0;
  let i = 1;
  for (; i < message.length && i < 6; i++) {
    const b = message[i]!;
    len |= (b & 0x7f) << shift;
    shift += 7;
    if (b < 0x80) break;
  }
  return i + 1 + len === message.length ? message.subarray(i + 1) : undefined;
}

async function settle(done: () => boolean, timeout: number): Promise<boolean> {
  const deadline = Date.now() + timeout;
  while (!done()) {
    if (Date.now() >= deadline) return false;
    await new Promise((r) => setTimeout(r, 50));
  }
  return true;
}
