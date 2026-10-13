import { useRef, useState } from "react";
import { errorText } from "../connection/connection";
import { Devices } from "../devices/Devices";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import { useSession } from "../session/session";
import { Palette } from "../stickers/Palette";
import { Avatar } from "./Avatar";
import { ICONS } from "./icons";
import { preparePicture } from "./picture";
import { nameProblem, passwordProblem } from "./rules";

/** The signed-in user's profile and account. */
export function Settings({ onClose }: { onClose: () => void }) {
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
        <PictureForm hasPicture={(me?.avatar.length ?? 0) > 0} />
        <IconPicker current={me?.icon ?? ""} />
        <Palette />
        <PasswordForm />
        <Devices />
        <button className="signout" data-testid="signout" onClick={() => signOut()}>
          Sign out
        </button>
        <DeleteAccount />
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
        if (problem) {
          setOutcome({ error: problem });
          return;
        }
        updateProfile({ name })
          .then(() => {
            setName("");
            setOutcome({ done: "Name changed." });
          })
          .catch((err: unknown) =>
            setOutcome({ error: errorText(err, { [ErrorCode.ALREADY_EXISTS]: "That name is taken." }) }),
          );
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

function PictureForm({ hasPicture }: { hasPicture: boolean }) {
  const setPicture = useSession((s) => s.setPicture);
  const updateProfile = useSession((s) => s.updateProfile);
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<Outcome>(null);

  function run(work: Promise<void>, done: string) {
    setBusy(true);
    setOutcome(null);
    work
      .then(() => setOutcome({ done }))
      .catch((err: unknown) => setOutcome({ error: errorText(err) }))
      .finally(() => setBusy(false));
  }

  return (
    <div className="field">
      <span className="label">Picture</span>
      <span className="picture-actions">
        <button type="button" data-testid="choose-picture" disabled={busy} onClick={() => input.current?.click()}>
          {busy ? "Saving…" : "Choose picture"}
        </button>
        {hasPicture && (
          <button
            type="button"
            className="secondary"
            data-testid="remove-picture"
            disabled={busy}
            onClick={() => run(updateProfile({ avatar: new Uint8Array() }), "Picture removed.")}
          >
            Remove picture
          </button>
        )}
      </span>
      <input
        ref={input}
        type="file"
        accept="image/*"
        hidden
        data-testid="picture-input"
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          if (file) run(preparePicture(file).then(setPicture), "Picture changed.");
        }}
      />
      <OutcomeLine outcome={outcome} />
    </div>
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
                .catch((err: unknown) => setError(errorText(err)))
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
        if (problem) {
          setOutcome({ error: problem });
          return;
        }
        changePassword(current, next)
          .then(() => {
            setCurrent("");
            setNext("");
            setConfirm("");
            setOutcome({ done: "Password changed. Your other devices were signed out." });
          })
          .catch((err: unknown) =>
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

function DeleteAccount() {
  const deleteAccount = useSession((s) => s.deleteAccount);
  const [open, setOpen] = useState(false);
  const [password, setPassword] = useState("");
  const [outcome, setOutcome] = useState<Outcome>(null);

  if (!open)
    return (
      <button className="delete-account" data-testid="delete-account" onClick={() => setOpen(true)}>
        Delete account…
      </button>
    );

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        deleteAccount(password).catch((err: unknown) =>
          setOutcome({ error: errorText(err, { [ErrorCode.UNAUTHENTICATED]: "The password is wrong." }) }),
        );
      }}
    >
      <p className="hint">
        Deleting the account frees your name and signs out every device. People you talked with see a deleted account;
        the messages you sent stay with them.
      </p>
      <label>
        Password
        <input
          data-testid="delete-password"
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="current-password"
          required
        />
      </label>
      <span className="row-input">
        <button type="submit" className="danger" disabled={!password}>
          Delete account
        </button>
        <button
          type="button"
          className="secondary"
          onClick={() => {
            setOpen(false);
            setPassword("");
            setOutcome(null);
          }}
        >
          Cancel
        </button>
      </span>
      <OutcomeLine outcome={outcome} />
    </form>
  );
}
