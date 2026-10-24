// Deletes uploads nothing refers to a day after their slot was made: the
// row first, while still unused, and only then the blob, so of several
// sweepers one deletes it.
import { exec, now, rows } from "../internal/db.ts";
import type { Deps } from "../services/deps.ts";

const sweepFirst = 60_000;
const sweepInterval = 10 * 60_000;
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

export async function sweep(deps: Deps): Promise<number> {
  const page = await rows<{ user_id: Buffer; blob_id: Buffer }>(
    deps.users,
    `SELECT user_id, blob_id FROM upload WHERE used = FALSE AND created_at < ? LIMIT ?`,
    [new Date(now().getTime() - unusedFor), sweepPage],
  );
  let deleted = 0;
  for (const u of page) {
    const res = await exec(deps.users, `DELETE FROM upload WHERE user_id = ? AND blob_id = ? AND used = FALSE`, [
      u.user_id,
      u.blob_id,
    ]);
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
