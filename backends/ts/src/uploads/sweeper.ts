// Deletes uploads nothing refers to a day after their slot was made, and
// attachments still waiting a day later for their message's delivery, which
// a failed send left behind: the row first, while still unused or pending,
// and only then the blob, so of several sweepers one deletes it.
import { exec, now, rows } from "../internal/db.ts";
import type { Deps } from "../services/deps.ts";

const sweepFirst = 60_000;
const sweepInterval = 10 * 60_000;
// How long a slot may go unused, or an attachment wait for its message's
// delivery, before its blob is deleted.
const unusedFor = 24 * 3600_000;
const sweepPage = 500;

export function startSweeper(deps: Deps): () => void {
  let timer = setTimeout(function run() {
    sweep(deps)
      .then((n) => {
        if (n > 0) deps.log.info("swept unused uploads", { blobs: n });
      })
      .catch((err: unknown) => deps.log.warn("sweeping uploads failed", { err }))
      .finally(() => (timer = setTimeout(run, sweepInterval)));
  }, sweepFirst);
  return () => clearTimeout(timer);
}

/** Deletes a page of unused uploads and one of pending ones; returns how many blobs it deleted. */
export async function sweep(deps: Deps): Promise<number> {
  const cutoff = new Date(now().getTime() - unusedFor);
  let deleted = 0;
  for (const query of [
    `SELECT user_id, blob_id FROM upload WHERE used = FALSE AND created_at < ? LIMIT ?`,
    `SELECT user_id, blob_id FROM upload WHERE pending_since < ? LIMIT ?`,
  ]) {
    deleted += await sweepPageOf(deps, query, cutoff);
  }
  return deleted;
}

async function sweepPageOf(deps: Deps, query: string, cutoff: Date): Promise<number> {
  const page = await rows<{ user_id: Buffer; blob_id: Buffer }>(deps.users, query, [cutoff, sweepPage]);
  let deleted = 0;
  for (const u of page) {
    const res = await exec(
      deps.users,
      `DELETE FROM upload WHERE user_id = ? AND blob_id = ? AND (used = FALSE OR pending_since < ?)`,
      [u.user_id, u.blob_id, cutoff],
    );
    // Taken into use or delivered meanwhile, or another sweeper got it.
    if (res.affectedRows !== 1) continue;
    try {
      await deps.blobs.delete(u.blob_id, now());
      deleted++;
    } catch (err) {
      deps.log.warn("deleting blob failed", { blob: u.blob_id, err });
    }
  }
  return deleted;
}
