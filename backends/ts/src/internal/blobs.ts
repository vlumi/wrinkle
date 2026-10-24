// The blob storage behind the blob proxy: SigV4 presigned requests, HEAD and
// DELETE. Requests are signed for the host the proxy sets, whatever address
// a device uses.
import { createHash, createHmac } from "node:crypto";
import { hex, type UUID } from "./ids.ts";
import type { BlobConfig } from "../services/deps.ts";

const signingHost = "blobs";
const requestTimeout = 5_000;

export const blobPath = (blobId: UUID): string => "/blobs/" + hex(blobId);

export class Blobs {
  private readonly url: string;
  private readonly cfg: BlobConfig;

  constructor(cfg: BlobConfig) {
    this.cfg = cfg;
    this.url = cfg.url.replace(/\/$/, "");
  }

  /** A path and query a device may PUT exactly this blob to until it expires. */
  presignPut(blobId: UUID, mediaType: string, size: bigint, now: Date, expiresSeconds: number): string {
    return this.presign(
      "PUT",
      blobPath(blobId),
      { "content-type": mediaType, "content-length": size.toString() },
      now,
      expiresSeconds,
    );
  }

  async exists(blobId: UUID): Promise<boolean> {
    const res = await fetch(this.url + blobPath(blobId), {
      method: "HEAD",
      signal: AbortSignal.timeout(requestTimeout),
    });
    if (res.status === 200) return true;
    if (res.status === 404 || res.status === 403) return false;
    throw new Error(`blob ${hex(blobId)}: HEAD answered ${res.status}`);
  }

  async delete(blobId: UUID, now: Date): Promise<void> {
    const target = this.presign("DELETE", blobPath(blobId), {}, now, 60);
    const res = await fetch(this.url + target, { method: "DELETE", signal: AbortSignal.timeout(requestTimeout) });
    if (res.status === 200 || res.status === 204 || res.status === 404) return;
    throw new Error(`blob ${hex(blobId)}: DELETE answered ${res.status}`);
  }

  presign(
    method: string,
    path: string,
    signedHeaders: Record<string, string>,
    now: Date,
    expiresSeconds: number,
  ): string {
    const amzDate = now
      .toISOString()
      .replace(/[-:]/g, "")
      .replace(/\.\d{3}/, "");
    const date = amzDate.slice(0, 8);
    const scope = `${date}/${this.cfg.region}/s3/aws4_request`;
    const headers: Record<string, string> = { host: signingHost };
    for (const [name, value] of Object.entries(signedHeaders)) headers[name.toLowerCase()] = value.trim();
    const names = Object.keys(headers).sort();
    const signed = names.join(";");
    const params: Record<string, string> = {
      "X-Amz-Algorithm": "AWS4-HMAC-SHA256",
      "X-Amz-Credential": `${this.cfg.keyId}/${scope}`,
      "X-Amz-Date": amzDate,
      "X-Amz-Expires": String(expiresSeconds),
      "X-Amz-SignedHeaders": signed,
    };
    const query = Object.keys(params)
      .sort()
      .map((k) => `${uriEncode(k)}=${uriEncode(params[k]!)}`)
      .join("&");
    const canonical =
      `${method}\n${path}\n${query}\n` +
      names.map((n) => `${n}:${headers[n]!}\n`).join("") +
      `\n${signed}\nUNSIGNED-PAYLOAD`;
    const toSign = `AWS4-HMAC-SHA256\n${amzDate}\n${scope}\n${createHash("sha256").update(canonical).digest("hex")}`;
    let key = hmac(Buffer.from("AWS4" + this.cfg.secret), date);
    key = hmac(key, this.cfg.region);
    key = hmac(key, "s3");
    key = hmac(key, "aws4_request");
    return `${path}?${query}&X-Amz-Signature=${hmac(key, toSign).toString("hex")}`;
  }
}

const hmac = (key: Buffer, data: string): Buffer => createHmac("sha256", key).update(data).digest();

function uriEncode(s: string): string {
  let out = "";
  for (const c of Buffer.from(s)) {
    const ch = String.fromCharCode(c);
    out += /[A-Za-z0-9\-_.~]/.test(ch) ? ch : "%" + c.toString(16).toUpperCase().padStart(2, "0");
  }
  return out;
}
