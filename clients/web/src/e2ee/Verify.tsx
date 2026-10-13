import { useEffect, useState } from "react";
import { Avatar } from "../account/Avatar";
import { displayName } from "../account/icons";
import { errorText } from "../connection/connection";
import type { User } from "../gen/wrinkle/v1/model_pb";
import { hex } from "../ids";
import { useSession } from "../session/session";
import { changedSinceVerified } from "./verified";

/** A contact's safety number, to compare with theirs, and marking them verified. */
export function Verify({ user, onClose }: { user: User; onClose: () => void }) {
  const safetyNumber = useSession((s) => s.safetyNumber);
  const markVerified = useSession((s) => s.markVerified);
  const id = hex(user.userId);
  const verified = useSession((s) => s.verified);
  const fingerprints = useSession((s) => s.fingerprints);
  const mine = useSession((s) => (s.me ? s.fingerprints[hex(s.me.userId)] : undefined));
  const [number, setNumber] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const theirs = fingerprints[id];

  // Again whenever either side's devices change.
  useEffect(() => {
    let current = true;
    safetyNumber(user.userId).then(
      (n) => current && setNumber(n),
      (err: unknown) => current && setError(errorText(err)),
    );
    return () => {
      current = false;
    };
  }, [user.userId, safetyNumber, theirs, mine]);

  const name = displayName(user);
  const isVerified = !!verified[id] && verified[id] === theirs;
  const changed = changedSinceVerified(verified, fingerprints, id);

  return (
    <section className="settings verify" aria-label={`Verify ${name}`}>
      <header>
        <Avatar user={user} />
        <h2>{name}</h2>
        <button className="close-info secondary" data-testid="close-verify" onClick={onClose}>
          Done
        </button>
      </header>
      <div className="settings-body">
        <div className="field">
          <span className="label">Safety number</span>
          <p className="safety-number" data-testid="safety-number">
            {number ?? "…"}
          </p>
          <p className="hint">
            Compare this number with the one {name} sees, in person or over another channel you trust. If they match,
            only your devices and theirs can read your messages.
          </p>
        </div>
        {changed && (
          <p className="error" role="alert">
            {name}'s devices changed since you verified them.
          </p>
        )}
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        {isVerified ? (
          <p className="verified" data-testid="verified">
            <span className="tag">Verified</span>
          </p>
        ) : (
          <button
            data-testid="mark-verified"
            disabled={!number}
            onClick={() => markVerified(user.userId).catch((err: unknown) => setError(errorText(err)))}
          >
            Mark as verified
          </button>
        )}
      </div>
    </section>
  );
}
