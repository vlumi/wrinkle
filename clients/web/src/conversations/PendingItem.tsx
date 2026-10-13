import { PendingFiles } from "../attachments/AttachmentsView";
import { StickerImage, STICKER_SIZE } from "../stickers/StickerImage";
import type { Pending } from "./pending";

/** A message of the user's being sent, or failed and to be tried again. */
export function PendingItem({ pending: p, onRetry }: { pending: Pending; onRetry: () => void }) {
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
