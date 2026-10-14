import { useEffect } from "react";
import { Avatar } from "./account/Avatar";
import { Settings } from "./account/Settings";
import { StatusBadge } from "./connection/StatusBadge";
import { Contacts } from "./contacts/Contacts";
import { incomingCount } from "./contacts/model";
import { ConversationList } from "./conversations/ConversationList";
import { ConversationView } from "./conversations/ConversationView";
import { SecurityBanners } from "./e2ee/SecurityBanners";
import { useSession } from "./session/session";
import { SignIn } from "./signin/SignIn";

export function App() {
  const credentials = useSession((s) => s.credentials);
  const me = useSession((s) => s.me);
  const status = useSession((s) => s.status);
  const current = useSession((s) => s.current);
  const error = useSession((s) => s.error);
  const resume = useSession((s) => s.resume);
  const clearError = useSession((s) => s.clearError);
  const settings = useSession((s) => s.settings);
  const showSettings = useSession((s) => s.showSettings);
  const contactsShown = useSession((s) => s.contactsShown);
  const showContacts = useSession((s) => s.showContacts);
  const requests = useSession((s) => incomingCount(s.contacts));

  // Resume the saved session; sign out to switch users.
  useEffect(() => {
    resume();
  }, [resume]);

  if (!credentials) return <SignIn />;

  return (
    <div className="app" data-view={settings || contactsShown || current ? "conversation" : "list"}>
      <header className="top">
        <h1>Wrinkle</h1>
        <StatusBadge status={status} />
        <button
          className="me"
          data-testid="open-settings"
          aria-label="Settings"
          aria-pressed={settings}
          onClick={() => showSettings(!settings)}
        >
          <span className="name" data-testid="me">
            {me?.name}
          </span>
          <Avatar user={me} />
        </button>
        <button
          className="contacts-open secondary"
          data-testid="open-contacts"
          aria-label={
            requests > 0 ? `Contacts, ${requests} new ${requests === 1 ? "request" : "requests"}` : "Contacts"
          }
          aria-pressed={contactsShown}
          onClick={() => showContacts(!contactsShown)}
        >
          Contacts
          {requests > 0 && (
            <span className="unread" data-testid="requests-badge" aria-hidden="true">
              {requests}
            </span>
          )}
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
      <SecurityBanners />
      <div className="panes">
        <ConversationList />
        {settings ? (
          <Settings onClose={() => showSettings(false)} />
        ) : contactsShown ? (
          <Contacts onClose={() => showContacts(false)} />
        ) : current ? (
          <ConversationView key={current} id={current} />
        ) : (
          <p className="empty">Pick a conversation or start one.</p>
        )}
      </div>
    </div>
  );
}
