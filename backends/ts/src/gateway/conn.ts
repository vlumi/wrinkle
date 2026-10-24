// One device's WebSocket connection: the read side, its limits and state
// rules, requests to the backend, and the frames written back.
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import type { WebSocket } from "ws";
import { ErrorCode, FrameSchema, type Frame, type Request, type Response } from "../gen/wrinkle/v1/frame_pb.js";
import {
  errorResponse,
  failedPrecondition,
  resourceExhausted,
  unauthenticated,
  unavailable,
} from "../internal/apierror.ts";
import { parseUUID, type UUID } from "../internal/ids.ts";
import { Logger } from "../internal/log.ts";
import * as metrics from "../internal/metrics.ts";
import { AckCursor, maxInFlight, TokenBucket } from "./limits.ts";
import type { Server } from "./server.ts";

export const requestTimeout = 10_000;
export const connectTimeout = 5_000;
export const subscribeTimeout = 5_000;
export const outboundQueue = 32;
// Frame's event field (3), length-delimited: a stored event becomes a frame
// by prefixing it.
const frameEventTag = (3 << 3) | 2;

export const close = {
  normal: 1000,
  goingAway: 1001,
  unsupportedData: 1003,
  policyViolation: 1008,
  internalError: 1011,
  tryAgainLater: 1013,
  signedOut: 4001,
} as const;

export class Conn {
  private readonly server: Server;
  private readonly ws: WebSocket;
  log: Logger;
  lastSeen = Date.now();
  private readonly bucket = new TokenBucket(Date.now());
  private inFlight = 0;
  private queued = 0;
  private closing = false;
  ended = false;
  private connectTimer: NodeJS.Timeout | undefined;
  userId: UUID | undefined;
  deviceId = 0n;
  private acks: AckCursor | undefined;

  constructor(server: Server, ws: WebSocket, remote: string) {
    this.server = server;
    this.ws = ws;
    this.log = server.log.with({ remote });
    ws.on("message", (data, isBinary) => this.onFrame(data as Buffer, isBinary));
    ws.on("pong", () => (this.lastSeen = Date.now()));
    ws.on("close", () => this.onEnd());
    ws.on("error", (err) => this.log.debug("connection error", { err }));
    this.startConnectTimer();
  }

  get connected(): boolean {
    return this.userId !== undefined;
  }

  close(code: number, reason: string): void {
    if (this.closing) return;
    this.closing = true;
    if (code === close.tryAgainLater) this.log.warn("outbound queue full, closing");
    this.ws.close(code, reason);
  }

  /** Ends the connection without the closing handshake. */
  terminate(): void {
    this.ws.terminate();
  }

  ping(): void {
    if (!this.ended) this.ws.ping();
  }

  private onFrame(data: Buffer, isBinary: boolean): void {
    const now = Date.now();
    this.lastSeen = now;
    if (!isBinary) {
      this.close(close.unsupportedData, "frames are binary protobuf");
      return;
    }
    let frame: Frame;
    try {
      frame = fromBinary(FrameSchema, data);
    } catch {
      this.close(close.policyViolation, "malformed frame");
      return;
    }
    const allowed = this.bucket.take(now);
    switch (frame.kind.case) {
      case "request": {
        this.startRequest(frame.kind.value, allowed);
        return;
      }
      case "ack":
        if (this.connected && allowed) this.ackCursor().offer(frame.kind.value.cursor);
        return;
      case "ping":
        if (allowed) this.reply(create(FrameSchema, { kind: { case: "pong", value: {} } }));
        return;
      case "pong":
        return;
      default: {
        this.close(close.policyViolation, "unexpected frame");
        return;
      }
    }
  }

  private ackCursor(): AckCursor {
    if (!this.acks) {
      const userId = this.userId!;
      const deviceId = this.deviceId;
      this.acks = new AckCursor(this.server.ackInterval, (cursor) =>
        this.server.track(
          this.server.backend
            .ack(userId, deviceId, cursor)
            .catch((err: unknown) => this.log.warn("ack failed", { err })),
        ),
      );
    }
    return this.acks;
  }

  private startRequest(req: Request, allowed: boolean): void {
    if (!allowed || this.inFlight >= maxInFlight) {
      const resp = errorResponse(req.id, resourceExhausted("too many requests"));
      this.reply(create(FrameSchema, { kind: { case: "response", value: resp } }));
      observe(req, resp, Date.now());
      return;
    }
    this.inFlight++;
    const start = Date.now();
    void this.server.track(
      this.dispatch(req)
        .then((resp) => {
          this.reply(create(FrameSchema, { kind: { case: "response", value: resp } }));
          observe(req, resp, start);
        })
        .finally(() => this.inFlight--),
    );
  }

  /** Applies the connection's state rules, then hands the request to the backend. */
  private async dispatch(req: Request): Promise<Response> {
    const kind = req.body.case;
    const signIn = kind === "register" || kind === "signIn" || kind === "connect";
    if (signIn && this.connected) return errorResponse(req.id, failedPrecondition("already connected"));
    if (!signIn && !this.connected) return errorResponse(req.id, unauthenticated("connect first"));
    let resp: Response;
    try {
      resp = await withTimeout(this.server.backend.call(this.userId, this.deviceId, req), requestTimeout);
    } catch (err) {
      this.log.error("backend call failed", { request: req.id, err });
      return errorResponse(req.id, unavailable("services unavailable"));
    }
    if (resp.body.case === "connect") {
      try {
        await this.identify(resp.body.value.user?.userId, resp.body.value.deviceId);
      } catch (err) {
        return errorResponse(req.id, err);
      }
    }
    if (kind === "register" || kind === "signIn") {
      // Hashing the password may have taken a while; Connect gets a fresh deadline.
      this.stopConnectTimer();
      this.startConnectTimer();
    }
    return resp;
  }

  /** Records the identity Connect established and attaches the connection to its user. */
  private async identify(rawUserId: Uint8Array | undefined, deviceId: bigint): Promise<void> {
    if (this.connected) throw failedPrecondition("already connected");
    const userId = parseUUID(rawUserId);
    if (!userId) throw new Error("connect returned no user id");
    this.userId = userId;
    this.deviceId = deviceId;
    this.stopConnectTimer();
    try {
      await this.server.attach(this);
    } catch {
      // Not connected after all: Connect may be retried, within a fresh deadline.
      this.userId = undefined;
      this.deviceId = 0n;
      this.startConnectTimer();
      throw unavailable("push subscription unavailable");
    }
    if (this.ended) {
      // The connection ended while connecting; its end may have missed the attachment.
      this.server.detach(this);
      throw unavailable("connection closed");
    }
    this.log = this.log.with({ user: userId, device: deviceId });
  }

  private startConnectTimer(): void {
    if (this.ended) return;
    this.connectTimer = setTimeout(() => {
      if (!this.connected) this.close(close.policyViolation, "connect first");
    }, connectTimeout);
  }

  private stopConnectTimer(): void {
    clearTimeout(this.connectTimer);
    this.connectTimer = undefined;
  }

  private onEnd(): void {
    this.ended = true;
    this.stopConnectTimer();
    if (this.connected) {
      this.server.detach(this);
      this.acks?.close();
    }
    this.server.untrack(this);
  }

  /** Writes a frame answering one of the device's own. */
  private reply(frame: Frame): void {
    this.send(Buffer.from(toBinary(FrameSchema, frame)), true);
  }

  /** Writes an event as stored, without decoding it. */
  pushEvent(payload: Uint8Array): void {
    const header = Buffer.alloc(11);
    header[0] = frameEventTag;
    let n = 1;
    for (let len = payload.length; ;) {
      header[n++] = len > 0x7f ? (len & 0x7f) | 0x80 : len;
      if (len <= 0x7f) break;
      len >>>= 7;
    }
    if (this.send(Buffer.concat([header.subarray(0, n), payload]), false)) metrics.eventsPushed.inc();
  }

  /**
   * Queues bytes for the socket. A device that does not keep up with pushed
   * events is disconnected and syncs on reconnect; replies are bounded by the
   * requests in flight, so they always queue.
   */
  private send(data: Buffer, reply: boolean): boolean {
    if (this.ended || this.closing) return false;
    if (!reply && this.queued >= outboundQueue) {
      this.close(close.tryAgainLater, "too slow");
      return false;
    }
    this.queued++;
    this.ws.send(data, { binary: true }, (err) => {
      this.queued--;
      if (err && !this.ended) this.close(close.internalError, "write failed");
    });
    return true;
  }
}

function withTimeout<T>(p: Promise<T>, ms: number): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  return Promise.race([
    p,
    new Promise<never>((_, reject) => (timer = setTimeout(() => reject(new Error("timed out")), ms))),
  ]).finally(() => clearTimeout(timer));
}

const snake = (s: string) => s.replace(/[A-Z]/g, (c) => "_" + c.toLowerCase());

function observe(req: Request, resp: Response, start: number): void {
  const kind = req.body.case ? snake(req.body.case) : "unknown";
  const code = resp.body.case === "error" ? "ERROR_CODE_" + ErrorCode[resp.body.value.code] : "ok";
  metrics.requests.inc({ type: kind, code });
  metrics.requestDuration.observe({ type: kind }, (Date.now() - start) / 1000);
}
