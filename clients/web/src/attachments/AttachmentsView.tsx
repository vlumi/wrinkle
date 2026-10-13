import { useEffect, useState } from "react";
import { blobAddress } from "../blobs/blobs";
import type { Attachment, Attachments } from "../gen/wrinkle/v1/content_pb";
import { hex } from "../ids";
import { useSession } from "../session/session";
import { fileOf, save, thumbnailURL } from "./files";
import { formatSize, isImage } from "./model";
import type { PendingFile } from "../conversations/pending";

/** The longest side of a lone image's thumbnail in a bubble, in pixels. */
const SINGLE_SIDE = 240;

function useAddress(): string | null {
  const gateway = useSession((s) => s.credentials?.url);
  return gateway ? blobAddress(gateway) : null;
}

/** A message's images as thumbnails, a grid for several, and its other files as rows. */
export function AttachmentsView({ attachments }: { attachments: Attachments }) {
  const [viewing, setViewing] = useState<Attachment | null>(null);
  const images = attachments.items.filter(isImage);
  const others = attachments.items.filter((a) => !isImage(a));
  return (
    <span className="attachments">
      {images.length > 0 && (
        <span className={images.length === 1 ? "attachment-grid single" : "attachment-grid"}>
          {images.map((a) => (
            <ImageThumb key={hex(a.blobId)} attachment={a} single={images.length === 1} onOpen={() => setViewing(a)} />
          ))}
        </span>
      )}
      {others.map((a) => (
        <FileRow key={hex(a.blobId)} attachment={a} />
      ))}
      {viewing && <Viewer attachment={viewing} onClose={() => setViewing(null)} />}
    </span>
  );
}

function ImageThumb({
  attachment: a,
  single,
  onOpen,
}: {
  attachment: Attachment;
  single: boolean;
  onOpen: () => void;
}) {
  const url = thumbnailURL(a);
  const scale = single ? Math.min(1, SINGLE_SIDE / Math.max(a.width, a.height)) : 0;
  const style = single ? { width: Math.round(a.width * scale), aspectRatio: `${a.width} / ${a.height}` } : undefined;
  return (
    <button
      type="button"
      className="attachment-image"
      data-testid="attachment-image"
      data-blob={hex(a.blobId)}
      aria-label={`Open ${a.name}`}
      style={style}
      onClick={onOpen}
    >
      {url ? <img src={url} alt={a.name} draggable={false} /> : <span className="attachment-placeholder">Photo</span>}
    </button>
  );
}

function FileRow({ attachment: a }: { attachment: Attachment }) {
  const address = useAddress();
  const [state, setState] = useState<"idle" | "loading" | "failed">("idle");
  return (
    <button
      type="button"
      className="attachment-file"
      data-testid="attachment-file"
      data-blob={hex(a.blobId)}
      disabled={!address || state === "loading"}
      onClick={() => {
        if (!address) return;
        setState("loading");
        fileOf(address, a)
          .then((blob) => {
            save(blob, a.name);
            setState("idle");
          })
          .catch(() => setState("failed"));
      }}
    >
      <span className="attachment-icon" aria-hidden>
        📄
      </span>
      <span className="attachment-name">{a.name}</span>
      <span className="attachment-size">
        {state === "loading"
          ? "Downloading…"
          : state === "failed"
            ? "Could not download. Try again."
            : formatSize(a.size)}
      </span>
    </button>
  );
}

/** An image full screen: downloaded, checked, decrypted and shown. */
function Viewer({ attachment: a, onClose }: { attachment: Attachment; onClose: () => void }) {
  const address = useAddress();
  const [url, setUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!address) return;
    let live = true;
    let made: string | null = null;
    fileOf(address, a)
      .then((blob) => {
        if (!live) return;
        made = URL.createObjectURL(blob);
        setUrl(made);
      })
      .catch(() => live && setFailed(true));
    return () => {
      live = false;
      if (made) URL.revokeObjectURL(made);
    };
  }, [address, a]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="viewer" role="dialog" aria-label={a.name} data-testid="viewer">
      <button type="button" className="viewer-backdrop" aria-label="Close" onClick={onClose} />
      {url ? (
        <img src={url} alt={a.name} data-testid="viewer-image" />
      ) : failed ? (
        <p className="viewer-status">Could not load the picture.</p>
      ) : (
        <>
          {thumbnailURL(a) && <img className="viewer-preview" src={thumbnailURL(a)} alt="" />}
          <p className="viewer-status">Loading…</p>
        </>
      )}
      <span className="viewer-actions">
        {url && (
          <button
            type="button"
            className="secondary"
            onClick={() => address && fileOf(address, a).then((blob) => save(blob, a.name))}
          >
            Save
          </button>
        )}
        <button type="button" className="secondary" data-testid="close-viewer" onClick={onClose}>
          Close
        </button>
      </span>
    </div>
  );
}

/** Files of a message being sent, with how much of each is uploaded. */
export function PendingFiles({ files }: { files: PendingFile[] }) {
  return (
    <span className="attachments">
      <span className="attachment-grid">
        {files.map((f, i) => (
          <span key={i} className="attachment-pending" data-testid="pending-file" title={f.name}>
            {f.preview ? <img src={f.preview} alt={f.name} /> : <span className="attachment-name">{f.name}</span>}
            <progress value={f.progress} max={1} aria-label={`Uploading ${f.name}`} />
          </span>
        ))}
      </span>
    </span>
  );
}
