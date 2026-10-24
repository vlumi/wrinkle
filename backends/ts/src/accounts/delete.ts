// Deleting an account.
import { create } from "@bufbuild/protobuf";
import type { DeleteAccountRequest } from "../gen/wrinkle/v1/messaging_pb.js";
import { ProfileEventSchema } from "../gen/wrinkle/v1/event_pb.js";
import { UserSchema } from "../gen/wrinkle/v1/model_pb.js";
import { exec, now, row, tx } from "../internal/db.ts";
import type { UUID } from "../internal/ids.ts";
import { releaseClaim } from "./names.ts";
import { lockOutbox } from "./outbox.ts";
import type { AccountsDeps } from "./service.ts";
import { verifyCurrent, wrongPassword } from "./session.ts";

/**
 * Checks the password, has leaveGroups take the user out of their groups,
 * then empties the user row, keeping it as a tombstone, signs out every
 * device and writes a ProfileEvent of the deleted user, all in one
 * transaction. Fan-out tells the partners, closes the connections and
 * removes the user's log, list and devices. The name claim goes last; one
 * left behind is stale, as the row no longer has the name.
 */
export async function deleteAccount(
  d: AccountsDeps,
  userId: UUID,
  req: DeleteAccountRequest,
  leaveGroups: () => Promise<void>,
): Promise<void> {
  const verified = await verifyCurrent(d, userId, req.password);
  await leaveGroups();
  const name = await tx(d.db, async (c) => {
    const o = await lockOutbox(c, userId, now());
    const r = await row<{ name: string; password_hash: string }>(
      c,
      `SELECT name, password_hash FROM user WHERE user_id = ?`,
      [userId],
    );
    if (r?.password_hash !== verified) throw wrongPassword();
    await exec(
      c,
      `UPDATE user SET name = '', icon = '', avatar = NULL, password_hash = '', deleted_at = ? WHERE user_id = ?`,
      [o.now, userId],
    );
    await exec(c, `UPDATE device SET token_hash = NULL, push_service = NULL, push_token = NULL WHERE user_id = ?`, [
      userId,
    ]);
    const user = create(UserSchema, { userId, deleted: true });
    await o.add("profile", { case: "profile", value: create(ProfileEventSchema, { user }) });
    await o.save();
    return r.name;
  });
  await releaseClaim(d.db, name, userId).catch((err: unknown) =>
    d.log.warn("releasing the deleted account's name failed", { user: userId, err }),
  );
}
