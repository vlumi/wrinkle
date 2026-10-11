import { useState } from "react";
import { Avatar } from "../account/Avatar";
import { displayName } from "../account/icons";
import { errorText } from "../connection/connection";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import type { Conversation } from "../gen/wrinkle/v1/model_pb";
import { GroupAvatar } from "../groups/GroupAvatar";
import { isGroup } from "../groups/model";
import type { Users } from "../groups/model";
import { NewGroup } from "../groups/NewGroup";
import { useSession } from "../session/session";
import { byActivity, isDeleted, isUndecryptable, key, preview, unread } from "./model";
import type { Texts } from "./model";

export function ConversationList() {
  const conversations = useSession((s) => s.conversations);
  const current = useSession((s) => s.current);
  const me = useSession((s) => s.me);
  const users = useSession((s) => s.users);
  const texts = useSession((s) => s.texts);
  const show = useSession((s) => s.show);
  const [creating, setCreating] = useState(false);

  return (
    <section className="list" aria-label="Conversations">
      <OpenConversation />
      {creating ? (
        <NewGroup onDone={() => setCreating(false)} />
      ) : (
        <button className="new-group-open" data-testid="new-group" onClick={() => setCreating(true)}>
          New group
        </button>
      )}
      <ul data-testid="conversations">
        {byActivity(conversations).map((c) => {
          const id = key(c.conversationId);
          return (
            <li key={id}>
              <ConversationRow
                conversation={c}
                me={me?.userId}
                users={users}
                texts={texts}
                current={id === current}
                onClick={() => show(id)}
              />
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
  users,
  texts,
  current,
  onClick,
}: {
  conversation: Conversation;
  me: Uint8Array | undefined;
  users: Users;
  texts: Texts;
  current: boolean;
  onClick(): void;
}) {
  const n = unread(c);
  const group = isGroup(c);
  return (
    <button className={current ? "row current" : "row"} onClick={onClick} data-testid="row">
      {group ? <GroupAvatar /> : <Avatar user={c.peer} />}
      <span className="peer">
        <span className="name">{group ? c.topic : displayName(c.peer)}</span>
        {n > 0n && (
          <span className="unread" data-testid="unread">
            {n.toString()}
          </span>
        )}
      </span>
      <span
        className={
          c.lastMessage && (isDeleted(c.lastMessage) || isUndecryptable(c.lastMessage, texts)) ? "last removed" : "last"
        }
      >
        {preview(c, me, users, texts)}
      </span>
    </button>
  );
}
