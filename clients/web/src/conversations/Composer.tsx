import { useRef, useState } from "react";
import { formatSize, MAX_ATTACHMENTS, MAX_CAPTION, MAX_FILE_BYTES } from "../attachments/model";
import { StickerPicker } from "../stickers/StickerPicker";

/** A file chosen to send, with its preview while chosen. */
interface Chosen {
  file: File;
  preview: string;
}

/** The bar for a message: text, a sticker from the palette, or files with a caption. */
export function Composer({
  onSend,
  onSticker,
  onAttach,
}: {
  onSend: (text: string) => void;
  onSticker: (id: string) => void;
  onAttach: (files: File[], caption: string) => void;
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
