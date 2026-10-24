// A connection pool for one Vitess keyspace through vtgate, and what the
// services share for it. Parameters are interpolated on the client, so
// vtgate sees no prepared statements; BIGINT columns come back as bigint.
import mysql, { type Pool, type PoolConnection, type RowDataPacket, type ResultSetHeader } from "mysql2/promise";

export type Row = RowDataPacket;
export type Querier = Pool | PoolConnection;

export async function openKeyspace(addr: string, keyspace: string, connections: number): Promise<Pool> {
  const [host, port] = splitHostPort(addr);
  const pool = mysql.createPool({
    host,
    port,
    database: keyspace,
    connectionLimit: connections,
    maxIdle: connections,
    idleTimeout: 5 * 60_000,
    connectTimeout: 5_000,
    timezone: "Z",
    dateStrings: false,
    supportBigNumbers: true,
    bigNumberStrings: true,
    typeCast(field, next) {
      if (field.type === "LONGLONG") {
        const s = field.string();
        return s === null ? null : BigInt(s);
      }
      return next();
    },
  });
  try {
    await pool.query("SELECT 1");
  } catch (err) {
    await pool.end();
    throw new Error(`keyspace ${keyspace} at ${addr}: ${(err as Error).message}`, { cause: err });
  }
  return pool;
}

function splitHostPort(addr: string): [string, number] {
  const i = addr.lastIndexOf(":");
  return [addr.slice(0, i) || "127.0.0.1", Number(addr.slice(i + 1))];
}

/** Rows of a SELECT. */
export async function rows<T = Row>(q: Querier, sql: string, args: unknown[] = []): Promise<T[]> {
  const [result] = await q.query<RowDataPacket[]>(sql, args);
  return result as T[];
}

/** The first row of a SELECT, or undefined. */
export async function row<T = Row>(q: Querier, sql: string, args: unknown[] = []): Promise<T | undefined> {
  return (await rows<T>(q, sql, args))[0];
}

/** Runs a statement that returns no rows. */
export async function exec(q: Querier, sql: string, args: unknown[] = []): Promise<ResultSetHeader> {
  const [result] = await q.query<ResultSetHeader>(sql, args);
  return result;
}

/** Runs fn in a transaction, committing when it returns and rolling back when it throws. */
export async function tx<T>(pool: Pool, fn: (c: PoolConnection) => Promise<T>): Promise<T> {
  const c = await pool.getConnection();
  try {
    await c.beginTransaction();
    const result = await fn(c);
    await c.commit();
    return result;
  } catch (err) {
    await c.rollback().catch(() => undefined);
    throw err;
  } finally {
    c.release();
  }
}

/** The time as the database stores it: UTC, to the microsecond (milliseconds here). */
export const now = (): Date => new Date();

export const isDuplicate = (err: unknown): boolean => (err as { errno?: number }).errno === 1062;

/** An optional BLOB column: empty bytes are stored as NULL. */
export const nullBytes = (b: Uint8Array | undefined): Buffer | null => (b && b.length > 0 ? Buffer.from(b) : null);

export const placeholders = (n: number): string => Array(n).fill("?").join(", ");

/** n rows of a multi-row VALUES list of the given width. */
export const rowsOf = (n: number, width: number): string =>
  Array(n)
    .fill(`(${placeholders(width)})`)
    .join(", ");
