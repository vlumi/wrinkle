import { useRef, useState } from "react";
import { errorText } from "../connection/connection";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import { useSession } from "../session/session";
import { beforeFor, MAX_PALETTE } from "./model";
import { prepareSticker } from "./picture";
import { StickerImage } from "./StickerImage";

const FULL = "Your palette is full. Remove a sticker first.";

/** The palette in settings: new stickers, their order, and removing them. */
export function Palette() {
  const palette = useSession((s) => s.palette);
  const createSticker = useSession((s) => s.createSticker);
  const moveSticker = useSession((s) => s.moveSticker);
  const removeSticker = useSession((s) => s.removeSticker);
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  function run(work: Promise<void>, outcome?: string) {
    setError(null);
    setDone(null);
    return work
      .then(() => setDone(outcome ?? null))
      .catch((err: unknown) => setError(errorText(err, { [ErrorCode.FAILED_PRECONDITION]: FULL })));
  }

  const ids = palette ?? [];
  return (
    <div className="field palette">
      <span className="label">Stickers</span>
      {palette !== null && ids.length === 0 && (
        <p className="hint">No stickers yet. Make one from a picture, or save one from a message.</p>
      )}
      {ids.length > 0 && (
        <ol className="palette-list" data-testid="palette">
          {ids.map((id, i) => {
            const up = beforeFor(ids, id, -1);
            const down = beforeFor(ids, id, 1);
            return (
              <li key={id} data-testid="palette-item" data-sticker={id}>
                <StickerImage id={id} size={56} />
                <span className="palette-actions">
                  <button
                    type="button"
                    className="secondary"
                    data-testid="sticker-up"
                    aria-label={`Move sticker ${i + 1} up`}
                    disabled={up === null}
                    onClick={() => up !== null && run(moveSticker(id, up))}
                  >
                    ↑
                  </button>
                  <button
                    type="button"
                    className="secondary"
                    data-testid="sticker-down"
                    aria-label={`Move sticker ${i + 1} down`}
                    disabled={down === null}
                    onClick={() => down !== null && run(moveSticker(id, down))}
                  >
                    ↓
                  </button>
                  <button
                    type="button"
                    className="secondary"
                    data-testid="remove-sticker"
                    aria-label={`Remove sticker ${i + 1}`}
                    onClick={() => run(removeSticker(id))}
                  >
                    Remove
                  </button>
                </span>
              </li>
            );
          })}
        </ol>
      )}
      <span className="picture-actions">
        <button
          type="button"
          data-testid="new-sticker"
          disabled={busy || ids.length >= MAX_PALETTE}
          onClick={() => input.current?.click()}
        >
          {busy ? "Making…" : "New sticker"}
        </button>
      </span>
      <input
        ref={input}
        type="file"
        accept="image/png,image/webp,image/gif,image/jpeg,image/*"
        hidden
        data-testid="sticker-input"
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          if (!file) return;
          setBusy(true);
          void run(prepareSticker(file).then(createSticker), "Sticker made and saved to your palette.").finally(() =>
            setBusy(false),
          );
        }}
      />
      {error && (
        <p className="error" role="alert" data-testid="palette-error">
          {error}
        </p>
      )}
      {done && (
        <p className="hint" data-testid="palette-done">
          {done}
        </p>
      )}
    </div>
  );
}
