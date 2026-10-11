import { useState } from "react";
import { errorText } from "../connection/connection";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import { useSession } from "../session/session";
import { Avatar } from "./Avatar";
import { ICONS } from "./icons";
import { nameProblem, passwordProblem } from "./rules";

/** The signed-in user's profile and account. */
export function Settings({ onClose }: { onClose(): void }) {
  const me = useSession((s) => s.me);
  const signOut = useSession((s) => s.signOut);

  return (
    <section className="settings" aria-label="Settings">
      <header>
        <button className="back" onClick={onClose} aria-label="Back to conversations" data-testid="close-settings">
          ‹
        </button>
        <h2>Settings</h2>
      </header>
      <div className="settings-body">
        <div className="profile">
          <Avatar user={me} large />
          <span data-testid="profile-name">{me?.name}</span>
        </div>
        <NameForm current={me?.name ?? ""} />
        <IconPicker current={me?.icon ?? ""} />
        <PasswordForm />
        <button className="signout" data-testid="signout" onClick={() => signOut()}>
          Sign out
        </button>
      </div>
    </section>
  );
}

/** A form's outcome: an error, or what was done. */
type Outcome = { error: string } | { done: string } | null;

function OutcomeLine({ outcome }: { outcome: Outcome }) {
  if (!outcome) return null;
  return "error" in outcome ? (
    <p className="error" role="alert" data-testid="settings-error">
      {outcome.error}
    </p>
  ) : (
    <p className="hint" data-testid="settings-done">
      {outcome.done}
    </p>
  );
}

function NameForm({ current }: { current: string }) {
  const updateProfile = useSession((s) => s.updateProfile);
  const [name, setName] = useState("");
  const [outcome, setOutcome] = useState<Outcome>(null);

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const problem = nameProblem(name);
        if (problem) return setOutcome({ error: problem });
        updateProfile({ name })
          .then(() => {
            setName("");
            setOutcome({ done: "Name changed." });
          })
          .catch((err) => setOutcome({ error: errorText(err, { [ErrorCode.ALREADY_EXISTS]: "That name is taken." }) }));
      }}
    >
      <label>
        Name
        <span className="row-input">
          <input
            data-testid="new-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={current}
            maxLength={64}
            autoCapitalize="none"
          />
          <button type="submit" disabled={!name || name === current}>
            Save
          </button>
        </span>
      </label>
      <OutcomeLine outcome={outcome} />
    </form>
  );
}

function IconPicker({ current }: { current: string }) {
  const updateProfile = useSession((s) => s.updateProfile);
  const [error, setError] = useState<string | null>(null);
  const choices: [string, string][] = [["", "none"], ...Object.entries(ICONS)];

  return (
    <div className="field">
      <span className="label">Icon</span>
      <div className="icons" role="radiogroup" aria-label="Icon">
        {choices.map(([icon, emoji]) => (
          <button
            key={icon || "none"}
            type="button"
            role="radio"
            data-testid={`icon-${icon || "none"}`}
            aria-checked={icon === current}
            aria-label={icon || "No icon"}
            onClick={() =>
              updateProfile({ icon })
                .then(() => setError(null))
                .catch((err) => setError(errorText(err)))
            }
          >
            {icon ? emoji : "–"}
          </button>
        ))}
      </div>
      {error && <p className="error">{error}</p>}
    </div>
  );
}

function PasswordForm() {
  const changePassword = useSession((s) => s.changePassword);
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [outcome, setOutcome] = useState<Outcome>(null);

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const problem = passwordProblem(next) ?? (next !== confirm ? "The new passwords differ." : null);
        if (problem) return setOutcome({ error: problem });
        changePassword(current, next)
          .then(() => {
            setCurrent("");
            setNext("");
            setConfirm("");
            setOutcome({ done: "Password changed. Your other devices were signed out." });
          })
          .catch((err) =>
            setOutcome({ error: errorText(err, { [ErrorCode.UNAUTHENTICATED]: "The current password is wrong." }) }),
          );
      }}
    >
      <label>
        Current password
        <input
          data-testid="current-password"
          type="password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          autoComplete="current-password"
          required
        />
      </label>
      <label>
        New password
        <input
          data-testid="new-password"
          type="password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
          autoComplete="new-password"
          required
        />
      </label>
      <label>
        Confirm new password
        <input
          data-testid="confirm-password"
          type="password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          autoComplete="new-password"
          required
        />
      </label>
      <button type="submit">Change password</button>
      <OutcomeLine outcome={outcome} />
    </form>
  );
}
