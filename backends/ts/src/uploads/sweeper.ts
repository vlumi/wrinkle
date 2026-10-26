// Deletes blobs nothing refers to: uploads never used, released, or taken by
// a send that failed. The row goes first, while still unused or pending, and
// only then the blob, so of several sweepers one deletes it.
import { sqlTime } from "../fanout/sqltime.ts";
import { exec, now, row, rows, timedRows, timestampOf, type DbTime } from "../internal/db.ts";
import { parseUUID } from "../internal/ids.ts";
import type { Deps } from "../services/deps.ts";

const sweepPage = 500;

export interface SweepOptions {
  /** How long a slot may go unused, or an attachment wait for its message's delivery, before its blob is deleted. */
  unusedMs: number;
  /** Time between two sweeps. */
  intervalMs: number;
  /** How long messages stay in MySQL, where the sweeper finds a pending attachment's message. */
  hotMs: number;
}

/** Sweeps a minute after starting, or the interval when shorter, then every interval. */
export function startSweeper(deps: Deps, opts: SweepOptions): () => void {
  let timer = setTimeout(
    function run() {
      sweep(deps, opts)
        .then((n) => {
          if (n > 0) deps.log.info("swept unused uploads", { blobs: n });
        })
        .catch((err: unknown) => deps.log.warn("sweeping uploads failed", { err }))
        .finally(() => (timer = setTimeout(run, opts.intervalMs)));
    },
    Math.min(60_000, opts.intervalMs),
  );
  return () => clearTimeout(timer);
}

interface Pending {
  user_id: Buffer;
  blob_id: Buffer;
  conversation_id?: Buffer | null;
  client_message_id?: string | null;
  pending_since?: DbTime;
}

/**
 * Deletes a page of unused uploads, settles a page of pending ones, deleting
 * those of failed sends, and returns how many blobs it deleted.
 */
export async function sweep(deps: Deps, opts: SweepOptions): Promise<number> {
  const at = now();
  const cutoff = new Date(at.getTime() - opts.unusedMs);
  let deleted = 0;
  const unused = await rows<Pending>(
    deps.users,
    `SELECT user_id, blob_id FROM upload WHERE used = FALSE AND created_at < ? LIMIT ?`,
    [cutoff, sweepPage],
  );
  for (const u of unused) if (await remove(deps, u, "used = FALSE")) deleted++;
  const waiting = await timedRows<Pending>(
    deps.users,
    `SELECT user_id, blob_id, conversation_id, client_message_id, pending_since FROM upload
     WHERE pending_since < ? LIMIT ?`,
    [cutoff, sweepPage],
  );
  for (const p of waiting) {
    const since = p.pending_since!;
    // Matched to the microsecond, as another backend may have stored it.
    const stored = sqlTime(timestampOf(since));
    if (pendingVerdict(await sent(deps, p), since, at, opts.hotMs) === "keep") {
      await exec(
        deps.users,
        `UPDATE upload SET pending_since = NULL WHERE user_id = ? AND blob_id = ? AND pending_since = ?`,
        [p.user_id, p.blob_id, stored],
      );
    } else if (await remove(deps, p, "pending_since = ?", stored)) {
      deleted++;
    }
  }
  return deleted;
}

/**
 * Decides an attachment upload still pending after the cutoff. Its message,
 * if the send stored one, was sent after the upload was taken, so while that
 * is well within the hot window the message would be in MySQL: not finding
 * it there means the send failed. Older, the message may have moved to the
 * cold tier, so the upload is kept.
 */
export function pendingVerdict(sent: boolean, since: Date, at: Date, hotMs: number): "keep" | "drop" {
  return !sent && since.getTime() > at.getTime() - hotMs / 2 ? "drop" : "keep";
}

/**
 * Whether the conversation keyspace has the message the upload was taken
 * for. Uploads taken before the message was recorded on them count as sent.
 */
async function sent(deps: Deps, p: Pending): Promise<boolean> {
  const conversationId = p.conversation_id ? parseUUID(p.conversation_id) : undefined;
  if (!conversationId || p.client_message_id == null) return true;
  const r = await row(
    deps.conversations,
    `SELECT 1 FROM message WHERE conversation_id = ? AND sender_user_id = ? AND client_message_id = ?`,
    [conversationId, p.user_id, p.client_message_id],
  );
  return r !== undefined;
}

/** Deletes the upload's row while the condition still holds, then its blob; whether it deleted the blob. */
async function remove(deps: Deps, p: Pending, condition: string, ...args: unknown[]): Promise<boolean> {
  const res = await exec(deps.users, `DELETE FROM upload WHERE user_id = ? AND blob_id = ? AND ${condition}`, [
    p.user_id,
    p.blob_id,
    ...args,
  ]);
  // Taken into use or delivered meanwhile, or another sweeper got it.
  if (res.affectedRows !== 1) return false;
  try {
    await deps.blobs.delete(p.blob_id, now());
    return true;
  } catch (err) {
    // The row is gone; the blob stays behind unreferenced.
    deps.log.warn("deleting blob failed", { blob: p.blob_id, err });
    return false;
  }
}
