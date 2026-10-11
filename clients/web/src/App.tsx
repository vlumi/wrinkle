import { useEffect } from "react";
import { ConversationList } from "./components/ConversationList";
import { ConversationView } from "./components/ConversationView";
import { SignIn } from "./components/SignIn";
import { StatusBadge } from "./components/StatusBadge";
import { lastSession, useSession } from "./lib/session";

const last = lastSession();

export function App() {
  const name = useSession((s) => s.name);
  const me = useSession((s) => s.me);
  const status = useSession((s) => s.status);
  const current = useSession((s) => s.current);
  const error = useSession((s) => s.error);
  const signIn = useSession((s) => s.signIn);
  const signOut = useSession((s) => s.signOut);
  const clearError = useSession((s) => s.clearError);

  // Resume the last session; sign out to switch users.
  useEffect(() => {
    if (last) signIn(last.name, last.url);
  }, [signIn]);

  if (!name) return <SignIn initialName={last?.name} initialURL={last?.url} />;

  return (
    <div className="app" data-view={current ? "conversation" : "list"}>
      <header className="top">
        <h1>Wrinkle</h1>
        <StatusBadge status={status} />
        <span className="me" data-testid="me">
          {me?.name ?? name}
        </span>
        <button className="signout" onClick={signOut}>
          Sign out
        </button>
      </header>
      {error && (
        <div className="banner" role="alert" data-testid="error">
          {error}
          <button onClick={clearError} aria-label="Dismiss">
            ×
          </button>
        </div>
      )}
      <div className="panes">
        <ConversationList />
        {current ? <ConversationView id={current} /> : <p className="empty">Pick a conversation or start one.</p>}
      </div>
    </div>
  );
}
