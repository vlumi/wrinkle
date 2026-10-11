import { useState } from "react";
import { NAME_RULES, PASSWORD_RULES, nameProblem, passwordProblem } from "../account/rules";
import { errorText } from "../connection/connection";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import { useSession } from "../session/session";
import type { AuthMode } from "../session/session";
import { defaultGatewayURL, forgetGateway, gatewayLabel, loadGateways } from "./gateways";

const modes: [AuthMode, string][] = [
  ["signIn", "Sign in"],
  ["register", "Create account"],
];

export function SignIn() {
  const authenticate = useSession((s) => s.authenticate);
  const notice = useSession((s) => s.notice);
  const [mode, setMode] = useState<AuthMode>("signIn");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [url, setURL] = useState(() => loadGateways()[0] ?? defaultGatewayURL());
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const register = mode === "register";

  function problem(): string | null {
    if (!register) return name && password ? null : "Enter your name and password.";
    return nameProblem(name) ?? passwordProblem(password) ?? (password !== confirm ? "The passwords differ." : null);
  }

  return (
    <main className="signin">
      <h1>Wrinkle</h1>
      <div className="tabs" role="tablist">
        {modes.map(([m, label]) => (
          <button
            key={m}
            type="button"
            role="tab"
            data-testid={`mode-${m}`}
            aria-selected={m === mode}
            onClick={() => {
              setMode(m);
              setError(null);
            }}
          >
            {label}
          </button>
        ))}
      </div>
      {notice && !error && <p className="notice">{notice}</p>}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          const p = problem();
          setError(p);
          if (p) return;
          setBusy(true);
          authenticate(mode, name, password, url.trim())
            .catch((err) =>
              setError(
                errorText(err, {
                  [ErrorCode.ALREADY_EXISTS]: "That name is taken.",
                  [ErrorCode.UNAUTHENTICATED]: "Wrong name or password.",
                  [ErrorCode.INVALID_ARGUMENT]: `${NAME_RULES} ${PASSWORD_RULES}`,
                }),
              ),
            )
            .finally(() => setBusy(false));
        }}
      >
        <label>
          Name
          <input
            data-testid="name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={64}
            autoComplete="username"
            autoCapitalize="none"
            required
          />
        </label>
        <label>
          Password
          <input
            data-testid="password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete={register ? "new-password" : "current-password"}
            required
          />
        </label>
        {register && (
          <label>
            Confirm password
            <input
              data-testid="confirm"
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              autoComplete="new-password"
              required
            />
          </label>
        )}
        {register && (
          <p className="hint">
            {NAME_RULES} {PASSWORD_RULES}
          </p>
        )}
        <label>
          Gateway
          <input
            data-testid="url"
            value={url}
            onChange={(e) => setURL(e.target.value)}
            autoCapitalize="none"
            spellCheck={false}
            required
          />
        </label>
        <RememberedGateways chosen={url.trim()} onPick={setURL} />
        {error && (
          <p className="error" role="alert" data-testid="signin-error">
            {error}
          </p>
        )}
        <button type="submit" disabled={busy}>
          {register ? "Create account" : "Sign in"}
        </button>
      </form>
    </main>
  );
}

function RememberedGateways({ chosen, onPick }: { chosen: string; onPick(url: string): void }) {
  const [gateways, setGateways] = useState(loadGateways);
  if (gateways.length === 0) return null;

  return (
    <ul className="gateways" aria-label="Gateways used before">
      {gateways.map((g) => (
        <li key={g} className={g === chosen ? "chosen" : undefined}>
          <button
            type="button"
            className="pick"
            data-testid="gateway"
            title={g}
            aria-pressed={g === chosen}
            onClick={() => onPick(g)}
          >
            {gatewayLabel(g)}
          </button>
          <button
            type="button"
            className="forget"
            data-testid="forget-gateway"
            aria-label={`Forget ${g}`}
            onClick={() => setGateways(forgetGateway(g))}
          >
            ×
          </button>
        </li>
      ))}
    </ul>
  );
}
