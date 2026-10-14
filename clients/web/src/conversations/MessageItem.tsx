import { useState } from "react";
import { Avatar } from "../account/Avatar";
import { displayName } from "../account/icons";
import { AttachmentsView } from "../attachments/AttachmentsView";
import type { Message, User } from "../gen/wrinkle/v1/model_pb";
import { withoutBlocked } from "../contacts/model";
import { hex } from "../ids";
import { chips, isEmoji, MAX_OWN, ownEmoji, QUICK, toggled } from "../reactions/model";
import { useSession } from "../session/session";
import { StickerImage, STICKER_SIZE } from "../stickers/StickerImage";
import { attachmentsOf, isDeleted, isUndecryptable, REMOVED, stickerOf, textKey, textOf } from "./model";

/** A message's own controls, which a tap on does not open or close it. */
const CONTROLS = "button, input, form, [role]";

/** A user's name as shown in a message's details: "You" for the user. */
function useNameOf(): (userId: string) => string {
  const me = useSession((s) => s.me);
  const users = useSession((s) => s.users);
  return (u) => (me && u === hex(me.userId) ? "You" : displayName(users[u]) || "Someone");
}

/** A message in the conversation on screen, with its actions and reactions. */
export function MessageItem({
  message: m,
  mine,
  sender,
  receipt,
  canReact,
  blocked,
}: {
  message: Message;
  mine: boolean;
  /** In groups, others' messages show who sent them; null for a sender not known yet. */
  sender?: User | null;
  receipt: string;
  /** Whether the user may still react to messages here. */
  canReact: boolean;
  /** Hex ids of blocked users, whose reactions are left out. */
  blocked?: ReadonlySet<string>;
}) {
  const t = textKey(m);
  const me = useSession((s) => s.me?.userId);
  const texts = useSession((s) => s.texts);
  const sticker = useSession((s) => stickerOf(m, s.stickers));
  const files = useSession((s) => attachmentsOf(m, s.attachments));
  const listed = useSession((s) => (isDeleted(m) ? undefined : s.reactions[t]));
  const reactions = blocked ? withoutBlocked(listed, blocked) : listed;
  const palette = useSession((s) => s.palette);
  const saveSticker = useSession((s) => s.saveSticker);
  const deleteMessage = useSession((s) => s.deleteMessage);
  const react = useSession((s) => s.react);
  const nameOf = useNameOf();
  // Actions show on hover, or after a tap on touch screens.
  const [open, setOpen] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [failed, setFailed] = useState(false);
  const [picking, setPicking] = useState(false);
  const [reactFailed, setReactFailed] = useState(false);
  const [saveFailed, setSaveFailed] = useState(false);
  const removed = isDeleted(m);
  const canDelete = mine && !removed;
  const reacting = canReact && !removed;
  /** For others' stickers: whether the sticker is in the user's palette. */
  const saved = sticker && !mine && palette ? palette.includes(sticker) : undefined;
  const shown = chips(reactions, me);
  const own = ownEmoji(reactions, me);
  const toggle = (emoji: string) => {
    if (!reacting) return;
    setReactFailed(false);
    react(m.seq, emoji).catch(() => setReactFailed(true));
  };
  const undecryptable = isUndecryptable(m, texts);
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
    // A tap opens the actions and who reacted; the keyboard has buttons for both.
    // eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions, jsx-a11y/click-events-have-key-events
    <li
      className={classes}
      data-testid="message"
      data-epoch={m.content.case === "sealed" ? m.content.value.epoch : undefined}
      onClick={(e) => {
        if ((canDelete || reacting || shown.length > 0) && !(e.target as Element).closest(CONTROLS)) setOpen(!open);
      }}
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
      {sticker &&
        saved !== undefined &&
        (saved ? (
          <span className="sticker-saved" data-testid="sticker-saved">
            Saved
          </span>
        ) : (
          <button
            className="sticker-save"
            data-testid="save-sticker"
            onClick={() => {
              setSaveFailed(false);
              saveSticker(sticker).catch(() => setSaveFailed(true));
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
              disabled={!reacting || (!c.mine && own.length >= MAX_OWN)}
              onClick={() => toggle(c.emoji)}
            >
              {c.emoji} <span className="count">{c.users.length}</span>
            </button>
          ))}
          <button
            className="reaction who"
            data-testid="who-reacted"
            aria-label="Who reacted"
            aria-expanded={open}
            onClick={() => setOpen(!open)}
          >
            …
          </button>
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
      {reacting && picking && (
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
        {reacting && (
          <button
            className="message-react"
            data-testid="react"
            aria-label="React"
            aria-expanded={picking}
            onClick={() => setPicking(!picking)}
          >
            React
          </button>
        )}
        {canDelete && !confirming && (
          <button
            className="message-delete"
            data-testid="delete-message"
            aria-label="Delete message"
            onClick={() => {
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
      {canDelete && confirming && (
        <span className="message-confirm" role="alertdialog" aria-label="Delete the message for everyone?">
          <span>{failed ? "Could not delete. Try again?" : "Delete for everyone?"}</span>
          <button
            className="danger"
            data-testid="confirm-delete"
            onClick={() =>
              deleteMessage(m.seq).catch(() => {
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

function ReactionPicker({ own, onPick }: { own: string[]; onPick: (emoji: string) => void }) {
  const [custom, setCustom] = useState("");
  const value = custom.trim();
  const allowed = (emoji: string) => toggled(own, emoji) !== null;
  return (
    <span className="reaction-picker" data-testid="reaction-picker">
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

function sentTime(m: Message): string {
  const s = m.sentAt?.seconds;
  return s === undefined ? "" : new Date(Number(s) * 1000).toLocaleTimeString([], { timeStyle: "short" });
}
