import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Avatar } from "../account/Avatar";
import { displayName } from "../account/icons";
import type { Message, User } from "../gen/wrinkle/v1/model_pb";
import { GroupAvatar } from "../groups/GroupAvatar";
import { GroupInfo } from "../groups/GroupInfo";
import { isFormer, isGroup, noticeOf, noticeText, readBy } from "../groups/model";
import { hex, sameId } from "../ids";
import { useSession } from "../session/session";
import { FingerprintWarning } from "../e2ee/FingerprintWarning";
import { Verify } from "../e2ee/Verify";
import { inOrder, isDeleted, isUndecryptable, REMOVED, textOf } from "./model";
import type { Texts } from "./model";
import type { Pending } from "./pending";

/** How close to the end still counts as scrolled to the bottom, in pixels. */
const BOTTOM_SLACK = 24;

export function ConversationView({ id }: { id: string }) {
  const conversation = useSession((s) => s.conversations[id]);
  const messageMap = useSession((s) => s.messages[id]);
  const pending = useSession((s) => s.pending[id]);
  const members = useSession((s) => s.members[id]);
  const users = useSession((s) => s.users);
  const texts = useSession((s) => s.texts);
  const complete = useSession((s) => s.complete[id]);
  const me = useSession((s) => s.me);
  const show = useSession((s) => s.show);
  const send = useSession((s) => s.send);
  const deleteMessage = useSession((s) => s.deleteMessage);
  const loadEarlier = useSession((s) => s.loadEarlier);
  const scroller = useRef<HTMLOListElement>(null);
  // Whether to follow new messages: not while the user reads further up.
  const atBottom = useRef(true);
  const [info, setInfo] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const messages = inOrder(messageMap);
  const count = messages.length + (pending?.length ?? 0);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && atBottom.current) el.scrollTop = el.scrollHeight;
  }, [count, info]);

  useEffect(() => {
    atBottom.current = true;
  }, [id]);

  if (!conversation) return null;
  const group = isGroup(conversation);
  if (group && info) return <GroupInfo id={id} onClose={() => setInfo(false)} />;
  if (!group && verifying && conversation.peer) {
    return <Verify user={conversation.peer} onClose={() => setVerifying(false)} />;
  }
  const oldest = messages[0];
  const peerName = displayName(conversation.peer);
  const lastMine = [...messages]
    .reverse()
    .find((m) => !noticeOf(m) && !isDeleted(m) && sameId(m.senderUserId, me?.userId));

  /** What the user's own message shows about who read it. */
  function receipt(m: Message): string {
    if (isDeleted(m)) return "";
    if (!group) return conversation.peerReadSeq >= m.seq ? "read" : "";
    if (m !== lastMine) return "";
    const n = readBy(members, me?.userId, m.seq);
    return n > 0 ? `Read by ${n}` : "";
  }

  return (
    <section
      className="conversation"
      aria-label={group ? `Group ${conversation.topic}` : `Conversation with ${peerName}`}
    >
      <header>
        <button className="back" onClick={() => show(null)} aria-label="Back to conversations">
          ‹
        </button>
        {group ? (
          <>
            <GroupAvatar />
            <span className="titles">
              <h2 data-testid="topic">{conversation.topic}</h2>
              <span className="subtitle" data-testid="member-count">
                {conversation.memberCount === 1 ? "1 member" : `${conversation.memberCount} members`}
              </span>
            </span>
            <button className="info secondary" data-testid="group-info" onClick={() => setInfo(true)}>
              Info
            </button>
          </>
        ) : (
          <>
            <Avatar user={conversation.peer} />
            <h2 data-testid="peer-name">{peerName}</h2>
            {!conversation.peer?.deleted && (
              <button className="info secondary" data-testid="verify" onClick={() => setVerifying(true)}>
                Verify
              </button>
            )}
          </>
        )}
      </header>
      <FingerprintWarning
        users={
          group
            ? (members ?? []).flatMap((m) => (m.user ? [m.user] : []))
            : conversation.peer
              ? [conversation.peer]
              : []
        }
        me={me?.userId}
        onVerify={group ? () => setInfo(true) : () => setVerifying(true)}
      />
      <ol
        ref={scroller}
        className="messages"
        data-testid="messages"
        onScroll={(e) => {
          const el = e.currentTarget;
          atBottom.current = el.scrollTop + el.clientHeight >= el.scrollHeight - BOTTOM_SLACK;
        }}
      >
        {oldest && oldest.seq > 1n && !complete && (
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
          const notice = noticeOf(m);
          if (notice) {
            return (
              <li key={m.seq.toString()} className="system" data-testid="notice">
                {noticeText(notice, m.senderUserId, me?.userId, users)}
              </li>
            );
          }
          const mine = sameId(m.senderUserId, me?.userId);
          return (
            <MessageItem
              key={m.seq.toString()}
              message={m}
              texts={texts}
              mine={mine}
              sender={group && !mine ? (users[hex(m.senderUserId)] ?? null) : undefined}
              receipt={mine ? receipt(m) : ""}
              onDelete={mine && !isDeleted(m) ? () => deleteMessage(m.seq) : undefined}
            />
          );
        })}
        {pending?.map((p) => (
          <PendingItem key={p.clientMessageId} pending={p} />
        ))}
      </ol>
      {isFormer(conversation) ? (
        <p className="hint closed" data-testid="former-member">
          You are no longer in this group
        </p>
      ) : !group && conversation.peer?.deleted ? (
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

function MessageItem({
  message: m,
  texts,
  mine,
  sender,
  receipt,
  onDelete,
}: {
  message: Message;
  texts: Texts;
  mine: boolean;
  /** In groups, others' messages show who sent them; null for a sender not known yet. */
  sender?: User | null;
  receipt: string;
  /** Present for the user's own messages that can still be deleted. */
  onDelete?: () => Promise<void>;
}) {
  // Actions show on hover, or after a tap on touch screens.
  const [open, setOpen] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [failed, setFailed] = useState(false);
  const removed = isDeleted(m);
  const undecryptable = isUndecryptable(m, texts);
  const classes = ["message", mine && "mine", removed && "removed", undecryptable && "undecryptable", open && "open"]
    .filter(Boolean)
    .join(" ");
  return (
    <li
      className={classes}
      data-testid="message"
      data-epoch={m.content.case === "sealed" ? m.content.value.epoch : undefined}
      onClick={onDelete ? () => setOpen(!open) : undefined}
    >
      {sender !== undefined && (
        <span className="sender" data-testid="sender">
          <Avatar user={sender} />
          <span className="sender-name">{sender ? displayName(sender) : "…"}</span>
        </span>
      )}
      <span className="text">{removed ? REMOVED : textOf(m, texts)}</span>
      <span className="meta">
        {onDelete && !confirming && (
          <button
            className="message-delete"
            data-testid="delete-message"
            aria-label="Delete message"
            onClick={(e) => {
              e.stopPropagation();
              setFailed(false);
              setConfirming(true);
            }}
          >
            Delete
          </button>
        )}
        {sentTime(m)}
        {receipt ? ` · ${receipt}` : ""}
      </span>
      {onDelete && confirming && (
        <span
          className="message-confirm"
          role="alertdialog"
          aria-label="Delete the message for everyone?"
          onClick={(e) => e.stopPropagation()}
        >
          <span>{failed ? "Could not delete. Try again?" : "Delete for everyone?"}</span>
          <button
            className="danger"
            data-testid="confirm-delete"
            onClick={() =>
              onDelete().catch(() => {
                setFailed(true);
              })
            }
          >
            Delete
          </button>
          <button className="secondary" data-testid="cancel-delete" onClick={() => setConfirming(false)}>
            Cancel
          </button>
        </span>
      )}
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
