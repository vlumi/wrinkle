// Ids: users and conversations are random UUIDs, 16 bytes (BINARY(16), hex
// in Valkey); devices and outbox rows random 63-bit numbers.
import { randomBytes } from "node:crypto";

export type UUID = Buffer;

export function newUUID(): UUID {
  const u = randomBytes(16);
  u[6] = (u[6]! & 0x0f) | 0x40;
  u[8] = (u[8]! & 0x3f) | 0x80;
  return u;
}

/** The bytes as an id, or undefined unless there are 16 of them. */
export function parseUUID(b: Uint8Array | null | undefined): UUID | undefined {
  return b?.length === 16 ? Buffer.from(b) : undefined;
}

export function fromHex(s: string): UUID | undefined {
  if (!/^[0-9a-f]{32}$/.test(s)) return undefined;
  return Buffer.from(s, "hex");
}

export const hex = (u: Uint8Array): string => Buffer.from(u).toString("hex");

export function isZero(u: Uint8Array | null | undefined): boolean {
  return !u || u.length === 0 || u.every((b) => b === 0);
}

export const sameId = (a: Uint8Array | undefined, b: Uint8Array | undefined): boolean =>
  !!a && !!b && Buffer.compare(a, b) === 0;

/** The two users of a pair, bytewise smaller first. */
export function participants(a: UUID, b: UUID): [UUID, UUID] {
  return Buffer.compare(a, b) > 0 ? [b, a] : [a, b];
}

/** A random id in 1 to 2^63-1. */
export function randomId(): bigint {
  const id = randomBytes(8).readBigUInt64BE() & ((1n << 63n) - 1n);
  return id === 0n ? 1n : id;
}
