import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Avatar } from "../account/Avatar";
import { displayName } from "../account/icons";
import type { Message } from "../gen/wrinkle/v1/model_pb";
import { sameId } from "../ids";
import { useSession } from "../session/session";
import { inOrder } from "./model";
import type { Pending } from "./pending";

/** How close to the end still counts as scrolled to the bottom, in pixels. */
const BOTTOM_SLACK = 24;

export function ConversationView({ id }: { id: string }) {
  const conversation = useSession((s) => s.conversations[id]);
  const messageMap = useSession((s) => s.messages[id]);
  const pending = useSession((s) => s.pending[id]);
  const me = useSession((s) => s.me);
  const show = useSession((s) => s.show);
  const send = useSession((s) => s.send);
  const loadEarlier = useSession((s) => s.loadEarlier);
  const scroller = useRef<HTMLOListElement>(null);
  // Whether to follow new messages: not while the user reads further up.
  const atBottom = useRef(true);
  const messages = inOrder(messageMap);
  const count = messages.length + (pending?.length ?? 0);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && atBottom.current) el.scrollTop = el.scrollHeight;
  }, [count]);

  useEffect(() => {
    atBottom.current = true;
  }, [id]);

  if (!conversation) return null;
  const oldest = messages[0];
  const peerName = displayName(conversation.peer);

  return (
    <section className="conversation" aria-label={`Conversation with ${peerName}`}>
      <header>
        <button className="back" onClick={() => show(null)} aria-label="Back to conversations">
          ‹
        </button>
        <Avatar user={conversation.peer} />
        <h2 data-testid="peer-name">{peerName}</h2>
      </header>
      <ol
        ref={scroller}
        className="messages"
        data-testid="messages"
        onScroll={(e) => {
          const el = e.currentTarget;
          atBottom.current = el.scrollTop + el.clientHeight >= el.scrollHeight - BOTTOM_SLACK;
        }}
      >
        {oldest && oldest.seq > 1n && (
          <li className="earlier">
            <button
              onClick={() => {
                atBottom.current = false;
                loadEarlier();
              }}
            >
              Load earlier
            </button>
          </li>
        )}
        {messages.map((m) => {
          const mine = sameId(m.senderUserId, me?.userId);
          return (
            <MessageItem
              key={m.seq.toString()}
              message={m}
              mine={mine}
              read={mine && conversation.peerReadSeq >= m.seq}
            />
          );
        })}
        {pending?.map((p) => (
          <PendingItem key={p.clientMessageId} pending={p} />
        ))}
      </ol>
      {conversation.peer?.deleted ? (
        <p className="hint closed">This account was deleted.</p>
      ) : (
        <Composer
          onSend={(text) => {
            atBottom.current = true;
            send(text).catch(() => {});
          }}
        />
      )}
    </section>
  );
}

function MessageItem({ message: m, mine, read }: { message: Message; mine: boolean; read: boolean }) {
  return (
    <li className={mine ? "message mine" : "message"} data-testid="message">
      <span className="text">{m.text}</span>
      <span className="meta">
        {sentTime(m)}
        {read ? " · read" : ""}
      </span>
    </li>
  );
}

function PendingItem({ pending: p }: { pending: Pending }) {
  return (
    <li className="message mine pending" data-testid="pending">
      <span className="text">{p.text}</span>
      <span className="meta">{p.failed ? "failed" : "sending…"}</span>
    </li>
  );
}

function Composer({ onSend }: { onSend(text: string): void }) {
  const [text, setText] = useState("");

  return (
    <form
      className="composer"
      onSubmit={(e) => {
        e.preventDefault();
        const t = text.trim();
        if (!t) return;
        setText("");
        onSend(t);
      }}
    >
      <input
        data-testid="text"
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder="Message"
        maxLength={4000}
        autoComplete="off"
        enterKeyHint="send"
      />
      <button type="submit" disabled={!text.trim()}>
        Send
      </button>
    </form>
  );
}

function sentTime(m: Message): string {
  const s = m.sentAt?.seconds;
  return s === undefined ? "" : new Date(Number(s) * 1000).toLocaleTimeString([], { timeStyle: "short" });
}
