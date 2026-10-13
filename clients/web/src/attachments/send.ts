// Sending files: each scaled down if a picture, encrypted with a key of its
// own and uploaded, then the message with their keys, shown pending with
// the uploads' progress.

import { upload } from "../blobs/upload";
import { create as createMessage } from "@bufbuild/protobuf";
import type { MessageInitShape } from "@bufbuild/protobuf";
import { withAttachments } from "../conversations/model";
import { addPending, newMessageId, withProgress } from "../conversations/pending";
import type { sendActions } from "../conversations/send";
import { encryptBlob, KEY_BYTES, randomBytes, sha256 } from "../e2ee/crypto";
import type { Bytes } from "../e2ee/crypto";
import { AttachmentsSchema } from "../gen/wrinkle/v1/content_pb";
import { BlobPurpose } from "../gen/wrinkle/v1/messaging_pb";
import { fromHex } from "../ids";
import type { Api } from "../session/api";
import type { Session } from "../session/session";
import { keep } from "./files";
import { cleanName, MAX_ATTACHMENTS, MAX_FILE_BYTES, thumbnailBudget } from "./model";
import { jpegName, prepare } from "./picture";
import type { Prepared } from "./picture";

/** A chosen file encrypted, ready to upload. */
interface Encrypted extends Prepared {
  name: string;
  key: Uint8Array;
  digest: Uint8Array;
  /** The blob to upload, until uploaded. */
  stored: Bytes | null;
  blobId: Uint8Array | null;
}

async function encrypted(file: File, budget: number): Promise<Encrypted> {
  const p = await prepare(file, budget);
  const name = cleanName(p.mediaType !== file.type ? jpegName(file.name) : file.name);
  if (p.bytes.length > MAX_FILE_BYTES) throw new Error(`${name} is larger than 25 MB.`);
  const fileKey = randomBytes(KEY_BYTES);
  const stored = await encryptBlob(fileKey, p.bytes);
  return { ...p, name, key: fileKey, digest: await sha256(stored), stored, blobId: null };
}

export function attachmentActions({ set, get, link }: Api, send: ReturnType<typeof sendActions>) {
  const actions = {
    /** Encrypts, uploads and sends files to the current conversation, shown pending with their progress. */
    async sendAttachments(files, caption) {
      const id = get().current;
      if (!id || !link.conn) return;
      if (files.length === 0 || files.length > MAX_ATTACHMENTS) throw new Error("Send 1 to 10 files at a time.");
      const clientMessageId = newMessageId();
      const previews = files.map((f) => (f.type.startsWith("image/") ? URL.createObjectURL(f) : ""));
      set({
        pending: addPending(get().pending, id, {
          clientMessageId,
          text: caption,
          attachments: files.map((f, i) => ({ name: f.name, preview: previews[i], progress: 0 })),
        }),
      });
      let ready: Encrypted[] | null = null;
      await send.attempt(id, clientMessageId, async () => {
        if (!ready) {
          const items = files.map((f) => ({ name: cleanName(f.name), mediaType: f.type }));
          const images = files.filter((f) => f.type.startsWith("image/")).length;
          const budget = thumbnailBudget(caption, items, images);
          ready = [];
          for (const f of files) ready.push(await encrypted(f, budget));
        }
        const all = ready;
        const blobIds: Uint8Array[] = [];
        for (const [i, e] of all.entries()) {
          if (e.stored) {
            const progress = (share: number) =>
              set({ pending: withProgress(get().pending, id, clientMessageId, i, share) });
            e.blobId = await upload(
              link.conn,
              BlobPurpose.ATTACHMENT,
              new Blob([e.stored], { type: "application/octet-stream" }),
              progress,
            );
            e.stored = null;
            progress(1);
          }
          if (e.blobId) blobIds.push(e.blobId);
        }
        const value: MessageInitShape<typeof AttachmentsSchema> = {
          caption,
          items: all.map((e, i) => ({
            blobId: blobIds[i],
            key: e.key,
            digest: e.digest,
            mediaType: e.mediaType,
            size: BigInt(e.bytes.length),
            name: e.name,
            width: e.width,
            height: e.height,
            thumbnail: e.thumbnail,
          })),
        };
        const m = await send.deliver(fromHex(id), clientMessageId, { case: "attachments", value }, blobIds);
        // The sender has the files already: showing them needs no download.
        for (const [i, e] of all.entries()) keep(blobIds[i], new Blob([e.bytes], { type: e.mediaType }));
        if (m) send.sent(m, (data, t) => withAttachments(data, { [t]: createMessage(AttachmentsSchema, value) }));
        for (const url of previews) if (url) URL.revokeObjectURL(url);
      });
    },
  } satisfies Partial<Session>;

  return { actions };
}
