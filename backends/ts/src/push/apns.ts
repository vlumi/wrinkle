// APNs with a token-based provider, over HTTP/2.
import { createPrivateKey, sign, type KeyObject } from "node:crypto";
import { readFileSync } from "node:fs";
import http2 from "node:http2";

// APNs endpoints; the tests point both at a local server.
const productionUrl = "https://api.push.apple.com";
const sandboxUrl = "https://api.sandbox.push.apple.com";

/**
 * A token-based provider: a .p8 key from the developer account, its key id,
 * the team id, and the app's bundle id as the topic. Without a key id, the
 * one in the key's file name is used.
 */
export interface APNsConfig {
  keyFile: string;
  keyId?: string;
  teamId: string;
  topic: string;
  productionUrl?: string;
  sandboxUrl?: string;
  /** Options for connecting, such as the CA of a test server. */
  connect?: http2.SecureClientSessionOptions;
}

/** The token is no longer valid for the topic; the registration should be removed. */
export class UnregisteredError extends Error {}

// Apple accepts a provider token for an hour and rejects one refreshed more
// often than every 20 minutes.
const tokenLifetime = 45 * 60_000;
const requestTimeout = 10_000;

/** What a notification shows, or, when background, a silent notification that wakes the app to act on data. */
export interface Alert {
  title?: string;
  body?: string;
  badge?: number;
  threadId?: string;
  background?: boolean;
  /** The app's notification service extension may change the alert before it shows, as it does to decrypt the message. */
  mutableContent?: boolean;
  /** Custom keys beside "aps", for the app to act on when the alert is tapped or the app woken. */
  data?: Record<string, unknown>;
}

/** The most a notification's JSON may have. */
export const maxPayload = 4096;

/** The notification's JSON. */
export function payload(alert: Alert): Buffer {
  let aps: Record<string, unknown> = {
    alert: { title: alert.title ?? "", body: alert.body ?? "" },
    badge: alert.badge ?? 0,
    sound: "default",
    "thread-id": alert.threadId ?? "",
  };
  if (alert.mutableContent) aps["mutable-content"] = 1;
  if (alert.background) aps = { "content-available": 1 };
  return Buffer.from(JSON.stringify({ aps, ...alert.data }));
}

const keyIdInFileName = /AuthKey_([A-Z0-9]{10})\.p8$/;

/** The key id in the name Apple gives the download, AuthKey_<id>.p8. */
export const keyIdFromFileName = (path: string): string => keyIdInFileName.exec(path)?.[1] ?? "";

export class APNs {
  private readonly key: KeyObject;
  private readonly keyId: string;
  private readonly teamId: string;
  private readonly topic: string;
  private readonly productionUrl: string;
  private readonly sandboxUrl: string;
  private readonly connectOptions: http2.SecureClientSessionOptions | undefined;
  private readonly sessions = new Map<string, http2.ClientHttp2Session>();
  private token = "";
  private tokenTime = 0;

  constructor(cfg: APNsConfig) {
    let key: KeyObject;
    try {
      key = createPrivateKey(readFileSync(cfg.keyFile));
    } catch (err) {
      throw new Error(`apns key: ${(err as Error).message}`, { cause: err });
    }
    if (key.asymmetricKeyType !== "ec") throw new Error("apns key: not an EC key");
    this.key = key;
    this.keyId = cfg.keyId || keyIdFromFileName(cfg.keyFile);
    if (!this.keyId || !cfg.teamId || !cfg.topic) throw new Error("apns: key id, team id and topic are all required");
    this.teamId = cfg.teamId;
    this.topic = cfg.topic;
    this.productionUrl = cfg.productionUrl || productionUrl;
    this.sandboxUrl = cfg.sandboxUrl || sandboxUrl;
    this.connectOptions = cfg.connect;
  }

  /** The current JWT, signing a new one when it is old. */
  providerToken(): string {
    const now = Date.now();
    if (this.token && now - this.tokenTime < tokenLifetime) return this.token;
    const enc = (v: object) => Buffer.from(JSON.stringify(v)).toString("base64url");
    const signingInput = `${enc({ alg: "ES256", kid: this.keyId })}.${enc({ iss: this.teamId, iat: Math.floor(now / 1000) })}`;
    // JWS wants the raw 64-byte r || s, not ASN.1.
    const sig = sign("sha256", Buffer.from(signingInput), { key: this.key, dsaEncoding: "ieee-p1363" });
    this.token = `${signingInput}.${sig.toString("base64url")}`;
    this.tokenTime = now;
    return this.token;
  }

  /** Delivers one alert to one device token. */
  async send(sandbox: boolean, deviceToken: string, alert: Alert, signal?: AbortSignal): Promise<void> {
    const jwt = this.providerToken();
    const body = payload(alert);
    // Apple requires the low priority for background notifications.
    const [pushType, priority] = alert.background ? ["background", "5"] : ["alert", "10"];
    const base = new URL(sandbox ? this.sandboxUrl : this.productionUrl);
    const session = this.session(base.origin);
    const { status, raw } = await new Promise<{ status: number; raw: string }>((resolve, reject) => {
      const req = session.request(
        {
          ":method": "POST",
          ":path": `${base.pathname.replace(/\/$/, "")}/3/device/${deviceToken}`,
          authorization: `bearer ${jwt}`,
          "apns-topic": this.topic,
          "apns-push-type": pushType,
          "apns-priority": priority,
        },
        { signal: signal ?? AbortSignal.timeout(requestTimeout) },
      );
      let status = 0;
      let raw = "";
      req.setEncoding("utf8");
      req.on("response", (headers) => (status = Number(headers[":status"])));
      req.on("data", (chunk: string) => {
        if (raw.length < 4096) raw += chunk;
      });
      req.on("end", () => resolve({ status, raw }));
      req.on("error", reject);
      req.end(body);
    });
    if (status === 200) return;
    let reason = "";
    try {
      const parsed = (JSON.parse(raw) as { reason?: unknown }).reason;
      if (typeof parsed === "string") reason = parsed;
    } catch {
      // No reason given.
    }
    if (status === 410 || reason === "BadDeviceToken" || reason === "DeviceTokenNotForTopic") {
      throw new UnregisteredError(`device token is no longer registered: ${reason}`);
    }
    if (reason === "ExpiredProviderToken") this.token = "";
    throw new Error(`apns: ${status} ${reason}`);
  }

  /** The connection to an endpoint, opened again once it closed. */
  private session(origin: string): http2.ClientHttp2Session {
    const open = this.sessions.get(origin);
    if (open && !open.closed && !open.destroyed) return open;
    const s = http2.connect(origin, this.connectOptions);
    s.on("error", () => undefined); // requests in flight fail on their own
    s.on("close", () => {
      if (this.sessions.get(origin) === s) this.sessions.delete(origin);
    });
    s.on("goaway", () => {
      if (this.sessions.get(origin) === s) this.sessions.delete(origin);
    });
    s.unref();
    this.sessions.set(origin, s);
    return s;
  }

  close(): void {
    for (const s of this.sessions.values()) s.close();
    this.sessions.clear();
  }
}
