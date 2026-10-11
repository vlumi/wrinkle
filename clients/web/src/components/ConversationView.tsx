import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { Message } from "../gen/wrinkle/v1/model_pb";
import { inOrder } from "../lib/model";
import { useSession } from "../lib/session";

function time(m: Message): string {
  const s = m.sentAt?.seconds;
  return s === undefined ? "" : new Date(Number(s) * 1000).toLocaleTimeString([], { timeStyle: "short" });
}

export function ConversationView({ id }: { id: string }) {
  const conversation = useSession((s) => s.conversations[id]);
  const messageMap = useSession((s) => s.messages[id]);
  const pending = useSession((s) => s.pending[id]);
  const me = useSession((s) => s.me);
  const show = useSession((s) => s.show);
  const send = useSession((s) => s.send);
  const loadEarlier = useSession((s) => s.loadEarlier);
  const [text, setText] = useState("");
  const scroller = useRef<HTMLOListElement>(null);
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

  return (
    <section className="conversation" aria-label={`Conversation with ${conversation.peer?.name}`}>
      <header>
        <button className="back" onClick={() => show(null)} aria-label="Back to conversations">
          ‹
        </button>
        <h2 data-testid="peer-name">{conversation.peer?.name}</h2>
      </header>
      <ol
        ref={scroller}
        className="messages"
        data-testid="messages"
        onScroll={(e) => {
          const el = e.currentTarget;
          atBottom.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 24;
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
          const mine = m.senderUserId === me?.userId;
          const read = mine && conversation.peerReadSeq >= m.seq;
          return (
            <li key={m.seq.toString()} className={mine ? "message mine" : "message"} data-testid="message">
              <span className="text">{m.text}</span>
              <span className="meta">
                {time(m)}
                {read ? " · read" : ""}
              </span>
            </li>
          );
        })}
        {pending?.map((p) => (
          <li key={p.clientMessageId} className="message mine pending" data-testid="pending">
            <span className="text">{p.text}</span>
            <span className="meta">{p.failed ? "failed" : "sending…"}</span>
          </li>
        ))}
      </ol>
      <form
        className="composer"
        onSubmit={(e) => {
          e.preventDefault();
          const t = text.trim();
          if (!t) return;
          setText("");
          atBottom.current = true;
          send(t).catch(() => {});
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
    </section>
  );
}
