import { useState } from "react";
import { errorText } from "../connection/connection";
import { useSession } from "../session/session";

/** Asks about sharing the history with a new device of the user, then tells what was done. */
export function SecurityBanners() {
  const newDevice = useSession((s) => s.newDevice);
  const notice = useSession((s) => s.securityNotice);
  const decideSharing = useSession((s) => s.decideSharing);
  const dismiss = useSession((s) => s.dismissSecurityNotice);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const decide = (share: boolean) => {
    setBusy(true);
    setError(null);
    decideSharing(share)
      .catch((err: unknown) => setError(errorText(err)))
      .finally(() => setBusy(false));
  };

  return (
    <>
      {newDevice && (
        <div className="banner security" role="alertdialog" aria-label="New device" data-testid="share-prompt">
          <span>
            {newDevice.name} signed in. Share your message history with it?
            {error && <span className="error"> {error}</span>}
          </span>
          <span className="banner-actions">
            <button data-testid="share-history" disabled={busy} onClick={() => decide(true)}>
              Share
            </button>
            <button className="secondary" data-testid="skip-sharing" disabled={busy} onClick={() => decide(false)}>
              Not now
            </button>
          </span>
        </div>
      )}
      {notice && (
        <div className="banner security" role="status" data-testid="security-notice">
          <span>{notice}</span>
          <button onClick={dismiss} aria-label="Dismiss">
            ×
          </button>
        </div>
      )}
    </>
  );
}
