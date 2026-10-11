// One device's WebSocket connection: binary Frames in and out, requests
// matched to responses by id, events and state changes to callbacks.

import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { Event } from "../gen/wrinkle/v1/event_pb";
import { FrameSchema, RequestSchema } from "../gen/wrinkle/v1/frame_pb";
import type { Frame, Response } from "../gen/wrinkle/v1/frame_pb";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";

export type ConnectionState = "offline" | "connecting" | "connected";

/** An error response from the backend. */
export class BackendError extends Error {
  constructor(
    readonly code: ErrorCode,
    message: string,
  ) {
    super(message || ErrorCode[code]);
  }
}

/** A message for the user: the one given for the error's code, or else the error's own. */
export function errorText(err: unknown, byCode: Partial<Record<ErrorCode, string>> = {}): string {
  if (err instanceof BackendError && byCode[err.code]) return byCode[err.code]!;
  return err instanceof Error ? err.message : String(err);
}

type RequestBody = MessageInitShape<typeof RequestSchema>["body"];
type ResponseBody = Response["body"];

/** The response body of the given case. */
type BodyOf<C extends NonNullable<ResponseBody>["case"]> = Extract<ResponseBody, { case: C }>["value"];

export interface Handlers {
  state(state: ConnectionState): void;
  open(): void;
  event(event: Event): void;
}

const RECONNECT_MIN_MS = 500;
const RECONNECT_MAX_MS = 10_000;

export class Connection {
  private ws: WebSocket | null = null;
  private nextId = 0n;
  private pending = new Map<bigint, { resolve(r: Response): void; reject(e: Error): void }>();
  private closed = false;
  private backoff = RECONNECT_MIN_MS;
  private retry: ReturnType<typeof setTimeout> | null = null;

  constructor(
    readonly url: string,
    private readonly handlers: Handlers,
  ) {}

  open(): void {
    this.closed = false;
    if (this.ws) return;
    this.handlers.state("connecting");
    const ws = new WebSocket(this.url);
    ws.binaryType = "arraybuffer";
    ws.onopen = () => {
      this.backoff = RECONNECT_MIN_MS;
      this.handlers.state("connected");
      this.handlers.open();
    };
    ws.onmessage = (e) => this.receive(new Uint8Array(e.data as ArrayBuffer));
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      for (const p of this.pending.values()) p.reject(new Error("connection closed"));
      this.pending.clear();
      this.handlers.state("offline");
      if (!this.closed) {
        this.retry = setTimeout(() => this.open(), this.backoff);
        this.backoff = Math.min(this.backoff * 2, RECONNECT_MAX_MS);
      }
    };
    this.ws = ws;
  }

  /** Reconnects now instead of waiting out the backoff, if offline. */
  wake(): void {
    if (this.closed || this.ws) return;
    if (this.retry) clearTimeout(this.retry);
    this.backoff = RECONNECT_MIN_MS;
    this.open();
  }

  /** Drops the socket and reconnects with backoff. */
  restart(): void {
    this.ws?.close();
  }

  close(): void {
    this.closed = true;
    if (this.retry) clearTimeout(this.retry);
    const ws = this.ws;
    this.ws = null;
    ws?.close();
    this.handlers.state("offline");
  }

  private send(frame: MessageInitShape<typeof FrameSchema>): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) throw new Error("not connected");
    this.ws.send(toBinary(FrameSchema, create(FrameSchema, frame)));
  }

  /** Sends a request and resolves with the response body of the expected case. */
  async call<C extends NonNullable<ResponseBody>["case"]>(expected: C, body: RequestBody): Promise<BodyOf<C>> {
    const id = ++this.nextId;
    const response = await new Promise<Response>((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      try {
        this.send({ kind: { case: "request", value: { id, body } } });
      } catch (err) {
        this.pending.delete(id);
        reject(err as Error);
      }
    });
    if (response.body.case === "error") {
      throw new BackendError(response.body.value.code, response.body.value.message);
    }
    if (response.body.case !== expected) throw new Error(`unexpected response ${response.body.case}`);
    return response.body.value as BodyOf<C>;
  }

  ack(cursor: bigint): void {
    try {
      this.send({ kind: { case: "ack", value: { cursor } } });
    } catch {
      // The next connect acks again.
    }
  }

  private receive(bytes: Uint8Array): void {
    let frame: Frame;
    try {
      frame = fromBinary(FrameSchema, bytes);
    } catch {
      return;
    }
    switch (frame.kind.case) {
      case "response": {
        const p = this.pending.get(frame.kind.value.requestId);
        if (!p) return;
        this.pending.delete(frame.kind.value.requestId);
        p.resolve(frame.kind.value);
        break;
      }
      case "event":
        this.handlers.event(frame.kind.value);
        break;
      case "ping":
        this.send({ kind: { case: "pong", value: {} } });
        break;
      default:
        break;
    }
  }
}
