import { useState } from "react";
import { byActivity, key, unread } from "../lib/model";
import { useSession } from "../lib/session";

export function ConversationList() {
  const conversations = useSession((s) => s.conversations);
  const current = useSession((s) => s.current);
  const me = useSession((s) => s.me);
  const show = useSession((s) => s.show);
  const openConversation = useSession((s) => s.openConversation);
  const [peer, setPeer] = useState("");
  const [error, setError] = useState<string | null>(null);

  return (
    <section className="list" aria-label="Conversations">
      <form
        className="open"
        onSubmit={(e) => {
          e.preventDefault();
          const name = peer.trim();
          if (!name) return;
          openConversation(name)
            .then(() => {
              setPeer("");
              setError(null);
            })
            .catch((err: Error) => setError(err.message));
        }}
      >
        <input data-testid="peer" value={peer} onChange={(e) => setPeer(e.target.value)} placeholder="Start a conversation with…" maxLength={64} autoCapitalize="none" />
        <button type="submit" disabled={!peer.trim()}>
          Open
        </button>
      </form>
      {error && <p className="error">{error}</p>}
      <ul data-testid="conversations">
        {byActivity(conversations).map((c) => {
          const id = key(c.conversationId);
          const n = unread(c);
          const last = c.lastMessage ? `${c.lastMessage.senderUserId === me?.userId ? "You: " : ""}${c.lastMessage.text}` : "No messages yet";
          return (
            <li key={id}>
              <button className={id === current ? "row current" : "row"} onClick={() => show(id)}>
                <span className="peer">
                  {c.peer?.name}
                  {n > 0n && (
                    <span className="unread" data-testid="unread">
                      {n.toString()}
                    </span>
                  )}
                </span>
                <span className="last">{last}</span>
              </button>
            </li>
          );
        })}
      </ul>
      {Object.keys(conversations).length === 0 && <p className="hint">No conversations yet. Start one above with another user's name.</p>}
    </section>
  );
}
