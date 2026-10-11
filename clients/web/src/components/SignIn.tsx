import { useState } from "react";
import { useSession } from "../lib/session";
import { defaultGatewayURL } from "../lib/util";

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
          <input data-testid="name" value={name} onChange={(e) => setName(e.target.value)} maxLength={64} autoComplete="username" autoCapitalize="none" required />
        </label>
        <label>
          Gateway
          <input data-testid="url" value={url} onChange={(e) => setURL(e.target.value)} autoCapitalize="none" spellCheck={false} required />
        </label>
        <button type="submit">Sign in</button>
      </form>
    </main>
  );
}
