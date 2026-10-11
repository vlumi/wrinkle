import { useSession } from "../session/session";
import { StickerImage } from "./StickerImage";

/** The composer's palette: a tap sends the sticker. */
export function StickerPicker({ onPick, onClose }: { onPick(id: string): void; onClose(): void }) {
  const palette = useSession((s) => s.palette);
  const showSettings = useSession((s) => s.showSettings);

  return (
    <div className="sticker-picker" data-testid="sticker-picker" role="dialog" aria-label="Stickers">
      {palette === null ? (
        <p className="hint">Loading stickers…</p>
      ) : palette.length === 0 ? (
        <p className="hint" data-testid="palette-empty">
          No stickers yet. Save one from a message with Save to palette, or make one with New sticker in Settings.
        </p>
      ) : (
        <div className="sticker-grid">
          {palette.map((id) => (
            <button
              key={id}
              type="button"
              data-testid="pick-sticker"
              data-sticker={id}
              aria-label="Send sticker"
              onClick={() => onPick(id)}
            >
              <StickerImage id={id} size={72} />
            </button>
          ))}
        </div>
      )}
      <span className="sticker-picker-actions">
        <button
          type="button"
          className="secondary"
          data-testid="edit-palette"
          onClick={() => {
            onClose();
            showSettings(true);
          }}
        >
          Edit stickers
        </button>
        <button type="button" className="secondary" onClick={onClose}>
          Close
        </button>
      </span>
    </div>
  );
}
