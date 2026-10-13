import { useState } from "react";
import { blobAddress, blobURL } from "../blobs/blobs";
import { fromHex } from "../ids";
import { useSession } from "../session/session";

/** A sticker's side in a conversation, in pixels. */
export const STICKER_SIZE = 144;

/** A sticker by its id in hex, read from the blob address; the browser caches it by its address. */
export function StickerImage({ id, size }: { id: string; size: number }) {
  const gateway = useSession((s) => s.credentials?.url);
  const [failed, setFailed] = useState(false);
  const address = gateway ? blobAddress(gateway) : null;
  if (!address || failed) {
    return (
      <span className="sticker-image missing" style={{ width: size, height: size }} data-sticker={id}>
        Sticker
      </span>
    );
  }
  return (
    <img
      className="sticker-image"
      src={blobURL(address, fromHex(id))}
      alt="Sticker"
      width={size}
      height={size}
      draggable={false}
      data-sticker={id}
      onError={() => setFailed(true)}
    />
  );
}
