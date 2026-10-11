import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Avatar } from "../account/Avatar";
import { AttachmentsView, PendingFiles } from "../attachments/AttachmentsView";
import { formatSize, MAX_ATTACHMENTS, MAX_CAPTION, MAX_FILE_BYTES } from "../attachments/model";
import { displayName } from "../account/icons";
import type { Message, User } from "../gen/wrinkle/v1/model_pb";
import { GroupAvatar } from "../groups/GroupAvatar";
import { GroupInfo } from "../groups/GroupInfo";
import { isFormer, isGroup, noticeOf, noticeText, readBy } from "../groups/model";
import { hex, sameId } from "../ids";
import { useSession } from "../session/session";
import { FingerprintWarning } from "../e2ee/FingerprintWarning";
import { Verify } from "../e2ee/Verify";
import { chips, isEmoji, MAX_OWN, ownEmoji, QUICK, toggled } from "../reactions/model";
import type { Entry } from "../reactions/model";
import { StickerImage } from "../stickers/StickerImage";
import { StickerPicker } from "../stickers/StickerPicker";
import { attachmentsOf, inOrder, isDeleted, isUndecryptable, REMOVED, stickerOf, textKey, textOf } from "./model";
import type { AttachmentsByMessage, Stickers, Texts } from "./model";
import type { Pending } from "./pending";

/** How close to the end still counts as scrolled to the bottom, in pixels. */
const BOTTOM_SLACK = 24;
/** A sticker's side in a conversation, in pixels. */
const STICKER_SIZE = 144;

export function ConversationView({ id }: { id: string }) {
  const conversation = useSession((s) => s.conversations[id]);
  const messageMap = useSession((s) => s.messages[id]);
  const pending = useSession((s) => s.pending[id]);
  const members = useSession((s) => s.members[id]);
  const users = useSession((s) => s.users);
  const texts = useSession((s) => s.texts);
  const stickers = useSession((s) => s.stickers);
  const attachments = useSession((s) => s.attachments);
  const palette = useSession((s) => s.palette);
  const reactions = useSession((s) => s.reactions);
  const complete = useSession((s) => s.complete[id]);
  const me = useSession((s) => s.me);
  const show = useSession((s) => s.show);
  const send = useSession((s) => s.send);
  const sendSticker = useSession((s) => s.sendSticker);
  const sendAttachments = useSession((s) => s.sendAttachments);
  const retrySend = useSession((s) => s.retrySend);
  const saveSticker = useSession((s) => s.saveSticker);
  const deleteMessage = useSession((s) => s.deleteMessage);
  const react = useSession((s) => s.react);
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

  const former = isFormer(conversation);
  const nameOf = (u: string): string => (me && u === hex(me.userId) ? "You" : displayName(users[u]) || "Someone");

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
          const sticker = stickerOf(m, stickers);
          return (
            <MessageItem
              key={m.seq.toString()}
              message={m}
              texts={texts}
              stickers={stickers}
              attachments={attachments}
              saved={sticker && !mine && palette ? palette.includes(sticker) : undefined}
              onSave={sticker && !mine && palette ? () => saveSticker(sticker) : undefined}
              mine={mine}
              sender={group && !mine ? (users[hex(m.senderUserId)] ?? null) : undefined}
              receipt={mine ? receipt(m) : ""}
              onDelete={mine && !isDeleted(m) ? () => deleteMessage(m.seq) : undefined}
              reactions={isDeleted(m) ? undefined : reactions[textKey(m)]}
              me={me?.userId}
              nameOf={nameOf}
              onReact={former || isDeleted(m) ? undefined : (emoji) => react(m.seq, emoji)}
            />
          );
        })}
        {pending?.map((p) => (
          <PendingItem
            key={p.clientMessageId}
            pending={p}
            onRetry={() => retrySend(p.clientMessageId).catch(() => {})}
          />
        ))}
      </ol>
      {former ? (
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
          onSticker={(sticker) => {
            atBottom.current = true;
            sendSticker(sticker).catch(() => {});
          }}
          onAttach={(files, caption) => {
            atBottom.current = true;
            sendAttachments(files, caption).catch(() => {});
          }}
        />
      )}
    </section>
  );
}

function MessageItem({
  message: m,
  texts,
  stickers,
  attachments,
  saved,
  onSave,
  mine,
  sender,
  receipt,
  onDelete,
  reactions,
  me,
  nameOf,
  onReact,
}: {
  message: Message;
  texts: Texts;
  stickers: Stickers;
  attachments: AttachmentsByMessage;
  /** For others' stickers: whether the sticker is in the user's palette. */
  saved?: boolean;
  onSave?: () => Promise<void>;
  mine: boolean;
  /** In groups, others' messages show who sent them; null for a sender not known yet. */
  sender?: User | null;
  receipt: string;
  /** Present for the user's own messages that can still be deleted. */
  onDelete?: () => Promise<void>;
  reactions?: Record<string, Entry>;
  me?: Uint8Array;
  nameOf(userId: string): string;
  /** Present while the user may react to the message. */
  onReact?: (emoji: string) => Promise<void>;
}) {
  // Actions show on hover, or after a tap on touch screens.
  const [open, setOpen] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [failed, setFailed] = useState(false);
  const [picking, setPicking] = useState(false);
  const [reactFailed, setReactFailed] = useState(false);
  const [saveFailed, setSaveFailed] = useState(false);
  const shown = chips(reactions, me);
  const own = ownEmoji(reactions, me);
  const toggle = (emoji: string) => {
    if (!onReact) return;
    setReactFailed(false);
    onReact(emoji).catch(() => setReactFailed(true));
  };
  const removed = isDeleted(m);
  const undecryptable = isUndecryptable(m, texts);
  const sticker = stickerOf(m, stickers);
  const files = attachmentsOf(m, attachments);
  const classes = [
    "message",
    mine && "mine",
    sticker && "sticker",
    files && "with-attachments",
    removed && "removed",
    undecryptable && "undecryptable",
    open && "open",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <li
      className={classes}
      data-testid="message"
      data-epoch={m.content.case === "sealed" ? m.content.value.epoch : undefined}
      onClick={onDelete || onReact || shown.length > 0 ? () => setOpen(!open) : undefined}
    >
      {sender !== undefined && (
        <span className="sender" data-testid="sender">
          <Avatar user={sender} />
          <span className="sender-name">{sender ? displayName(sender) : "…"}</span>
        </span>
      )}
      {sticker ? (
        <span className="text">
          <StickerImage id={sticker} size={STICKER_SIZE} />
        </span>
      ) : files ? (
        <>
          <AttachmentsView attachments={files} />
          {files.caption && <span className="text">{files.caption}</span>}
        </>
      ) : (
        <span className="text">{removed ? REMOVED : textOf(m, texts)}</span>
      )}
      {saved !== undefined &&
        (saved ? (
          <span className="sticker-saved" data-testid="sticker-saved">
            Saved
          </span>
        ) : (
          <button
            className="sticker-save"
            data-testid="save-sticker"
            onClick={(e) => {
              e.stopPropagation();
              setSaveFailed(false);
              onSave?.().catch(() => setSaveFailed(true));
            }}
          >
            Save to palette
          </button>
        ))}
      {saveFailed && <span className="reaction-error">Could not save. Is your palette full?</span>}
      {shown.length > 0 && (
        <span className="reactions">
          {shown.map((c) => (
            <button
              key={c.emoji}
              className={c.mine ? "reaction mine" : "reaction"}
              data-testid="reaction"
              data-emoji={c.emoji}
              aria-pressed={c.mine}
              title={c.users.map(nameOf).join(", ")}
              disabled={!onReact || (!c.mine && own.length >= MAX_OWN)}
              onClick={(e) => {
                e.stopPropagation();
                toggle(c.emoji);
              }}
            >
              {c.emoji} <span className="count">{c.users.length}</span>
            </button>
          ))}
        </span>
      )}
      {open && shown.length > 0 && (
        <span className="reacted-by" data-testid="reacted-by">
          {shown.map((c) => (
            <span key={c.emoji}>
              {c.emoji} {c.users.map(nameOf).join(", ")}
            </span>
          ))}
        </span>
      )}
      {onReact && picking && (
        <ReactionPicker
          own={own}
          onPick={(emoji) => {
            setPicking(false);
            toggle(emoji);
          }}
        />
      )}
      {reactFailed && <span className="reaction-error">Could not react. Try again.</span>}
      <span className="meta">
        {onReact && (
          <button
            className="message-react"
            data-testid="react"
            aria-label="React"
            aria-expanded={picking}
            onClick={(e) => {
              e.stopPropagation();
              setPicking(!picking);
            }}
          >
            React
          </button>
        )}
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

function ReactionPicker({ own, onPick }: { own: string[]; onPick(emoji: string): void }) {
  const [custom, setCustom] = useState("");
  const value = custom.trim();
  const allowed = (emoji: string) => toggled(own, emoji) !== null;
  return (
    <span className="reaction-picker" data-testid="reaction-picker" onClick={(e) => e.stopPropagation()}>
      {QUICK.map((emoji) => (
        <button
          key={emoji}
          className={own.includes(emoji) ? "mine" : undefined}
          data-testid="react-emoji"
          data-emoji={emoji}
          disabled={!allowed(emoji)}
          onClick={() => onPick(emoji)}
        >
          {emoji}
        </button>
      ))}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (isEmoji(value) && allowed(value)) onPick(value);
        }}
      >
        <input
          data-testid="react-custom"
          value={custom}
          onChange={(e) => setCustom(e.target.value)}
          placeholder="Any emoji"
          aria-label="Any emoji"
          autoComplete="off"
          size={6}
        />
        <button type="submit" disabled={!isEmoji(value) || !allowed(value)}>
          Add
        </button>
      </form>
    </span>
  );
}

function PendingItem({ pending: p, onRetry }: { pending: Pending; onRetry(): void }) {
  const classes = ["message", "mine", "pending", p.sticker && "sticker", p.failed && "failed"]
    .filter(Boolean)
    .join(" ");
  return (
    <li className={classes} data-testid="pending">
      {p.attachments && <PendingFiles files={p.attachments} />}
      {p.sticker ? (
        <span className="text">
          <StickerImage id={p.sticker} size={STICKER_SIZE} />
        </span>
      ) : (
        p.text && <span className="text">{p.text}</span>
      )}
      <span className="meta">
        {p.failed ? (
          <>
            failed ·{" "}
            <button type="button" className="message-retry" data-testid="retry" onClick={onRetry}>
              Retry
            </button>
          </>
        ) : (
          "sending…"
        )}
      </span>
    </li>
  );
}

/** A file chosen to send, with its preview while chosen. */
interface Chosen {
  file: File;
  preview: string;
}

function Composer({
  onSend,
  onSticker,
  onAttach,
}: {
  onSend(text: string): void;
  onSticker(id: string): void;
  onAttach(files: File[], caption: string): void;
}) {
  const [text, setText] = useState("");
  const [picking, setPicking] = useState(false);
  const [chosen, setChosen] = useState<Chosen[]>([]);
  const [problem, setProblem] = useState("");
  const input = useRef<HTMLInputElement>(null);

  function choose(files: FileList | null) {
    if (!files) return;
    const list = [...files];
    // Images are scaled down before sending; other files go as they are.
    const large = list.filter((f) => !f.type.startsWith("image/") && f.size > MAX_FILE_BYTES);
    const fitting = list.filter((f) => !large.includes(f));
    const room = MAX_ATTACHMENTS - chosen.length;
    const added = fitting.slice(0, room).map((file) => ({
      file,
      preview: file.type.startsWith("image/") ? URL.createObjectURL(file) : "",
    }));
    setChosen([...chosen, ...added]);
    setProblem(
      large.length > 0
        ? `${large[0].name} is larger than ${formatSize(MAX_FILE_BYTES)}.`
        : fitting.length > room
          ? `Up to ${MAX_ATTACHMENTS} files at a time.`
          : "",
    );
  }

  function remove(c: Chosen) {
    if (c.preview) URL.revokeObjectURL(c.preview);
    setChosen(chosen.filter((x) => x !== c));
    setProblem("");
  }

  const ready = chosen.length > 0 || !!text.trim();
  return (
    <>
      {picking && (
        <StickerPicker
          onPick={(id) => {
            setPicking(false);
            onSticker(id);
          }}
          onClose={() => setPicking(false)}
        />
      )}
      {(chosen.length > 0 || problem) && (
        <div className="chosen" data-testid="chosen">
          {chosen.map((c, i) => (
            <span key={i} className="chosen-file" data-testid="chosen-file" title={c.file.name}>
              {c.preview ? (
                <img src={c.preview} alt={c.file.name} />
              ) : (
                <span className="attachment-name">{c.file.name}</span>
              )}
              <button
                type="button"
                className="chosen-remove"
                data-testid="remove-chosen"
                aria-label={`Remove ${c.file.name}`}
                onClick={() => remove(c)}
              >
                ×
              </button>
            </span>
          ))}
          {problem && <span className="chosen-problem">{problem}</span>}
        </div>
      )}
      <form
        className="composer"
        onSubmit={(e) => {
          e.preventDefault();
          const t = text.trim();
          if (chosen.length > 0) {
            // The previews go to the pending message, which makes its own.
            for (const c of chosen) if (c.preview) URL.revokeObjectURL(c.preview);
            const files = chosen.map((c) => c.file);
            setChosen([]);
            setProblem("");
            setText("");
            onAttach(files, t);
            return;
          }
          if (!t) return;
          setText("");
          onSend(t);
        }}
      >
        <button
          type="button"
          className="sticker-button secondary"
          data-testid="stickers"
          aria-label="Stickers"
          aria-expanded={picking}
          onClick={() => setPicking(!picking)}
        >
          ☺
        </button>
        <button
          type="button"
          className="attach-button secondary"
          data-testid="attach"
          aria-label="Attach files"
          disabled={chosen.length >= MAX_ATTACHMENTS}
          onClick={() => input.current?.click()}
        >
          +
        </button>
        <input
          ref={input}
          type="file"
          multiple
          hidden
          data-testid="attach-input"
          onChange={(e) => {
            choose(e.currentTarget.files);
            e.currentTarget.value = "";
          }}
        />
        <input
          data-testid="text"
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={chosen.length > 0 ? "Caption" : "Message"}
          maxLength={chosen.length > 0 ? MAX_CAPTION : 4000}
          autoComplete="off"
          enterKeyHint="send"
        />
        <button type="submit" disabled={!ready}>
          Send
        </button>
      </form>
    </>
  );
}

function sentTime(m: Message): string {
  const s = m.sentAt?.seconds;
  return s === undefined ? "" : new Date(Number(s) * 1000).toLocaleTimeString([], { timeStyle: "short" });
}
