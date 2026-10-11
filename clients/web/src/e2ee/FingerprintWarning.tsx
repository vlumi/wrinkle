import { displayName } from "../account/icons";
import type { User } from "../gen/wrinkle/v1/model_pb";
import { hex, sameId } from "../ids";
import { useSession } from "../session/session";
import { changedSinceVerified } from "./verified";

/** Tells which verified contacts in the conversation have changed devices since. */
export function FingerprintWarning({
  users,
  me,
  onVerify,
}: {
  users: User[];
  me: Uint8Array | undefined;
  onVerify(): void;
}) {
  const verified = useSession((s) => s.verified);
  const fingerprints = useSession((s) => s.fingerprints);
  const changed = users.filter(
    (u) => !sameId(u.userId, me) && changedSinceVerified(verified, fingerprints, hex(u.userId)),
  );
  if (changed.length === 0) return null;
  const names = changed.map(displayName).join(", ");
  return (
    <div className="banner security warning" role="alert" data-testid="fingerprint-warning">
      <span>The devices of {names} changed since you verified them. Compare the safety number again.</span>
      <button className="secondary" onClick={onVerify}>
        Verify
      </button>
    </div>
  );
}
