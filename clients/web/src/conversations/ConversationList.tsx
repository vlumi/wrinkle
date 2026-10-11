import { useState } from "react";
import { Avatar } from "../account/Avatar";
import { errorText } from "../connection/connection";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import type { Conversation } from "../gen/wrinkle/v1/model_pb";
import { sameId } from "../ids";
import { useSession } from "../session/session";
import { byActivity, key, unread } from "./model";

export function ConversationList() {
  const conversations = useSession((s) => s.conversations);
  const current = useSession((s) => s.current);
  const me = useSession((s) => s.me);
  const show = useSession((s) => s.show);

  return (
    <section className="list" aria-label="Conversations">
      <OpenConversation />
      <ul data-testid="conversations">
        {byActivity(conversations).map((c) => {
          const id = key(c.conversationId);
          return (
            <li key={id}>
              <ConversationRow conversation={c} me={me?.userId} current={id === current} onClick={() => show(id)} />
            </li>
          );
        })}
      </ul>
      {Object.keys(conversations).length === 0 && (
        <p className="hint">No conversations yet. Start one above with another user's name.</p>
      )}
    </section>
  );
}

function OpenConversation() {
  const openConversation = useSession((s) => s.openConversation);
  const [peer, setPeer] = useState("");
  const [error, setError] = useState<string | null>(null);

  return (
    <>
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
            .catch((err) =>
              setError(
                errorText(err, {
                  [ErrorCode.NOT_FOUND]: `No one is called ${name}.`,
                  [ErrorCode.INVALID_ARGUMENT]: "Enter another user's name.",
                }),
              ),
            );
        }}
      >
        <input
          data-testid="peer"
          value={peer}
          onChange={(e) => setPeer(e.target.value)}
          placeholder="Start a conversation with…"
          maxLength={64}
          autoCapitalize="none"
        />
        <button type="submit" disabled={!peer.trim()}>
          Open
        </button>
      </form>
      {error && <p className="error">{error}</p>}
    </>
  );
}

function ConversationRow({
  conversation: c,
  me,
  current,
  onClick,
}: {
  conversation: Conversation;
  me: Uint8Array | undefined;
  current: boolean;
  onClick(): void;
}) {
  const n = unread(c);
  return (
    <button className={current ? "row current" : "row"} onClick={onClick}>
      <Avatar user={c.peer} />
      <span className="peer">
        <span className="name">{c.peer?.name}</span>
        {n > 0n && (
          <span className="unread" data-testid="unread">
            {n.toString()}
          </span>
        )}
      </span>
      <span className="last">{preview(c, me)}</span>
    </button>
  );
}

function preview(c: Conversation, me: Uint8Array | undefined): string {
  const m = c.lastMessage;
  if (!m) return "No messages yet";
  return `${sameId(m.senderUserId, me) ? "You: " : ""}${m.text}`;
}
