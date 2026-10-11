import { useState } from "react";
import { useSession } from "../session/session";
import { defaultGatewayURL, forgetGateway, gatewayLabel, loadGateways } from "./gateways";

export function SignIn({ initialName, initialURL }: { initialName?: string; initialURL?: string }) {
  const signIn = useSession((s) => s.signIn);
  const [name, setName] = useState(initialName ?? "");
  const [url, setURL] = useState(initialURL ?? defaultGatewayURL());

  return (
    <main className="signin">
      <h1>Wrinkle</h1>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) signIn(name.trim(), url.trim());
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
        <button type="submit">Sign in</button>
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
