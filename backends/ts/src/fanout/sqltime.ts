// Event timestamps written at their full precision: the internal/db.ts
// helpers read DATETIME(6) to the microsecond, but a Date argument is
// written to the millisecond, and events from other backends carry
// microseconds. An unset timestamp is the Unix epoch.
import type { Timestamp } from "@bufbuild/protobuf/wkt";

const pad = (n: number, width = 2) => String(n).padStart(width, "0");

/** The timestamp as a DATETIME(6) literal, in UTC, to the microsecond. */
export function sqlTime(ts: Timestamp | undefined): string {
  const d = new Date(Number(ts?.seconds ?? 0n) * 1000);
  const micros = Math.floor((ts?.nanos ?? 0) / 1000);
  return (
    `${pad(d.getUTCFullYear(), 4)}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ` +
    `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}.${pad(micros, 6)}`
  );
}

/** Seconds since the timestamp, for lag. */
export function secondsSince(ts: Timestamp | undefined): number {
  const at = ts ? Number(ts.seconds) + ts.nanos / 1e9 : 0;
  return Date.now() / 1000 - at;
}
