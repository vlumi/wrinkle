// Outbox rows arrive as Debezium emits them after unwrapping: binary columns
// as base64 strings, unsigned bigints as numbers or decimal strings.
import { fromBinary } from "@bufbuild/protobuf";
import { EventSchema, type Event } from "../gen/wrinkle/v1/event_pb.js";
import { sameId, type UUID } from "../internal/ids.ts";

const maxUint64 = (1n << 64n) - 1n;

/** A row of a conversation's outbox. */
export interface OutboxRow {
  conversationId: UUID;
  outboxId: bigint;
  recipients: UUID[];
  // The conversation as of the change.
  conversationKind: string;
  topic: string;
  memberCount: number;
  kind: string;
  /** The outbox row of the message a deletion is about, 0 for others. */
  targetOutboxId: bigint;
  /** A deletion's sender and the uploads of the message's attachments. */
  releasedBy: UUID | undefined;
  releasedUploads: UUID[];
  event: Event;
}

/** A row of a user's outbox: a profile, device, palette or contact change. */
export interface UserOutboxRow {
  userId: UUID;
  outboxId: bigint;
  kind: string;
  event: Event;
}

type Json = Record<string, unknown>;

// Numbers keep their source text, so ids beyond 2^53 survive.
const parseJson = (value: Buffer): Json => {
  const reviver = (_key: string, v: unknown, context?: { source?: string }) =>
    typeof v === "number" && context?.source !== undefined ? { number: context.source } : v;
  const parsed: unknown = JSON.parse(value.toString("utf8"), reviver);
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed))
    throw new Error("outbox row is not an object");
  return parsed as Json;
};

function uint(r: Json, column: string): bigint {
  const v = r[column];
  if (v === undefined) return 0n;
  const text =
    typeof v === "string" ? v : typeof v === "object" && v !== null ? (v as { number?: string }).number : undefined;
  if (text === undefined || !/^\d+$/.test(text)) throw new Error(`unsigned bigint ${column} ${JSON.stringify(v)}`);
  const n = BigInt(text);
  if (n > maxUint64) throw new Error(`unsigned bigint ${column} ${text} out of range`);
  return n;
}

function bytes(r: Json, column: string): Buffer {
  const v = r[column];
  if (v === undefined || v === null) return Buffer.alloc(0);
  if (typeof v !== "string") throw new Error(`${column} is not base64`);
  return Buffer.from(v, "base64");
}

function id(r: Json, column: string): UUID {
  const b = bytes(r, column);
  if (b.length !== 16) throw new Error(`${column} of ${b.length} bytes`);
  return b;
}

const text = (r: Json, column: string): string => {
  const v = r[column];
  return typeof v === "string" ? v : "";
};

function parseEvent(payload: Buffer): Event {
  try {
    return fromBinary(EventSchema, payload);
  } catch (err) {
    throw new Error(`outbox payload: ${(err as Error).message}`, { cause: err });
  }
}

export function parseOutboxRow(value: Buffer): OutboxRow {
  const r = parseJson(value);
  const conversationId = id(r, "conversation_id");
  const raw = bytes(r, "recipients");
  if (raw.length % 16 !== 0) throw new Error(`recipients of ${raw.length} bytes`);
  const recipients: UUID[] = [];
  for (let i = 0; i < raw.length; i += 16) recipients.push(Buffer.from(raw.subarray(i, i + 16)));
  const target = r["target_outbox_id"];
  const released = bytes(r, "released_uploads");
  if (released.length > 0 && (released.length % 16 !== 0 || released.length < 32))
    throw new Error(`released_uploads of ${released.length} bytes`);
  const releasedUploads: UUID[] = [];
  for (let i = 16; i < released.length; i += 16) releasedUploads.push(Buffer.from(released.subarray(i, i + 16)));
  return {
    conversationId,
    outboxId: uint(r, "outbox_id"),
    recipients,
    conversationKind: text(r, "conversation_kind"),
    topic: text(r, "topic"),
    memberCount: Number(uint(r, "member_count") & 0xffffffffn),
    kind: text(r, "kind"),
    targetOutboxId: target === undefined || target === null ? 0n : uint(r, "target_outbox_id"),
    releasedBy: released.length > 0 ? Buffer.from(released.subarray(0, 16)) : undefined,
    releasedUploads,
    event: parseEvent(bytes(r, "payload")),
  };
}

export function parseUserOutboxRow(value: Buffer): UserOutboxRow {
  const r = parseJson(value);
  const userId = id(r, "user_id");
  return {
    userId,
    outboxId: uint(r, "outbox_id"),
    kind: text(r, "kind"),
    event: parseEvent(bytes(r, "payload")),
  };
}

export const isDirect = (o: OutboxRow): boolean => o.conversationKind === "direct";

/** The other recipient of a direct conversation's row, which always has both participants. */
export function peerOf(o: OutboxRow, userId: UUID): UUID {
  return o.recipients.find((r) => !sameId(r, userId)) ?? Buffer.alloc(16);
}
