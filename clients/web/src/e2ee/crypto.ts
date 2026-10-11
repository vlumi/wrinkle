// The end-to-end cryptography of content.proto with WebCrypto: device key
// pairs, wrapping epoch keys, sealing content and reactions, fingerprints
// and safety numbers, and attachment blobs. Nonces are parameters so the shared test vectors can fix them.

/** A byte array WebCrypto accepts. */
export type Bytes = Uint8Array<ArrayBuffer>;

const encoder = new TextEncoder();
const WRAP_INFO = encoder.encode("wrinkle wrap v1");
const CONTENT_AAD = encoder.encode("wrinkle content v1");
const REACTION_AAD = encoder.encode("wrinkle reaction v1");
const BLOB_AAD = encoder.encode("wrinkle blob v1");
const FINGERPRINT = encoder.encode("wrinkle fingerprint v1");
// The DER prefix of a PKCS #8 X25519 private key; the 32-byte scalar follows.
const PKCS8_X25519 = Uint8Array.of(
  0x30,
  0x2e,
  0x02,
  0x01,
  0x00,
  0x30,
  0x05,
  0x06,
  0x03,
  0x2b,
  0x65,
  0x6e,
  0x04,
  0x22,
  0x04,
  0x20,
);

export const NONCE_BYTES = 12;
export const KEY_BYTES = 32;
export const WRAPPED_BYTES = NONCE_BYTES + KEY_BYTES + 16;

/** Whether this page can use the cryptography: WebCrypto exists only in secure contexts. */
export const available = (): boolean => typeof crypto !== "undefined" && !!crypto.subtle;

export function concat(...parts: Uint8Array[]): Bytes {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let i = 0;
  for (const p of parts) {
    out.set(p, i);
    i += p.length;
  }
  return out;
}

export function u32(n: number): Bytes {
  const b = new Uint8Array(4);
  new DataView(b.buffer).setUint32(0, n);
  return b;
}

export function u64(n: bigint): Bytes {
  const b = new Uint8Array(8);
  new DataView(b.buffer).setBigUint64(0, n);
  return b;
}

export const randomBytes = (n: number): Bytes => crypto.getRandomValues(new Uint8Array(n));

export interface DeviceKeyPair {
  privateKey: CryptoKey;
  /** Raw, 32 bytes. */
  publicKey: Bytes;
}

/** A new device key pair whose private key cannot be exported. */
export async function generateDeviceKey(): Promise<DeviceKeyPair> {
  const pair = await crypto.subtle.generateKey({ name: "X25519" }, false, ["deriveBits"]);
  return {
    privateKey: pair.privateKey,
    publicKey: new Uint8Array(await crypto.subtle.exportKey("raw", pair.publicKey)),
  };
}

/** A private key from its 32-byte scalar, for the test vectors. */
export async function importPrivateKey(scalar: Uint8Array): Promise<CryptoKey> {
  return crypto.subtle.importKey("pkcs8", concat(PKCS8_X25519, scalar), { name: "X25519" }, false, ["deriveBits"]);
}

/** X25519(own private, other public), 32 bytes. */
export async function sharedSecret(privateKey: CryptoKey, publicKey: Uint8Array): Promise<Bytes> {
  const pub = await crypto.subtle.importKey("raw", concat(publicKey), { name: "X25519" }, true, []);
  return new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: pub }, privateKey, 256));
}

/** HKDF-SHA256 with a salt of 32 zero bytes, 32 bytes out. */
export async function hkdf(ikm: Uint8Array, info: Uint8Array): Promise<Bytes> {
  const key = await crypto.subtle.importKey("raw", concat(ikm), "HKDF", false, ["deriveBits"]);
  const bits = await crypto.subtle.deriveBits(
    { name: "HKDF", hash: "SHA-256", salt: new Uint8Array(32), info: concat(info) },
    key,
    256,
  );
  return new Uint8Array(bits);
}

async function aesKey(raw: Uint8Array, extractable = false): Promise<CryptoKey> {
  return crypto.subtle.importKey("raw", concat(raw), "AES-GCM", extractable, ["encrypt", "decrypt"]);
}

async function encrypt(key: CryptoKey, nonce: Uint8Array, plaintext: Uint8Array, aad: Uint8Array): Promise<Bytes> {
  const params = { name: "AES-GCM", iv: concat(nonce), additionalData: concat(aad) };
  return new Uint8Array(await crypto.subtle.encrypt(params, key, concat(plaintext)));
}

async function decrypt(key: CryptoKey, nonce: Uint8Array, ciphertext: Uint8Array, aad: Uint8Array): Promise<Bytes> {
  const params = { name: "AES-GCM", iv: concat(nonce), additionalData: concat(aad) };
  return new Uint8Array(await crypto.subtle.decrypt(params, key, concat(ciphertext)));
}

/** A device: its user's id (16 bytes) and its own id. */
export interface DeviceRef {
  userId: Uint8Array;
  deviceId: bigint;
}

export interface WrapParams {
  conversationId: Uint8Array;
  epoch: number;
  sender: DeviceRef;
  recipient: DeviceRef;
}

export const wrapInfo = (p: WrapParams): Bytes =>
  concat(
    WRAP_INFO,
    p.conversationId,
    u32(p.epoch),
    p.sender.userId,
    u64(p.sender.deviceId),
    p.recipient.userId,
    u64(p.recipient.deviceId),
  );

/** An epoch key wrapped for the recipient device by the sender's: nonce || AES-GCM, 60 bytes. */
export async function wrapKey(
  epochKey: Uint8Array,
  senderPrivate: CryptoKey,
  recipientPublic: Uint8Array,
  p: WrapParams,
  nonce: Uint8Array = randomBytes(NONCE_BYTES),
): Promise<Bytes> {
  const wk = await hkdf(await sharedSecret(senderPrivate, recipientPublic), wrapInfo(p));
  return concat(nonce, await encrypt(await aesKey(wk), nonce, epochKey, new Uint8Array()));
}

/** The epoch key from a wrap for this device; rejects if it was not made for it. */
export async function unwrapKey(
  wrapped: Uint8Array,
  recipientPrivate: CryptoKey,
  senderPublic: Uint8Array,
  p: WrapParams,
): Promise<Bytes> {
  if (wrapped.length !== WRAPPED_BYTES) throw new Error("a wrapped key is 60 bytes");
  const wk = await hkdf(await sharedSecret(recipientPrivate, senderPublic), wrapInfo(p));
  return decrypt(await aesKey(wk), wrapped.subarray(0, NONCE_BYTES), wrapped.subarray(NONCE_BYTES), new Uint8Array());
}

/** An epoch key as kept: AES-GCM, exportable so it can be wrapped for the user's new devices. */
export const epochKey = (raw: Uint8Array): Promise<CryptoKey> => aesKey(raw, true);

export const rawKey = async (key: CryptoKey): Promise<Bytes> =>
  new Uint8Array(await crypto.subtle.exportKey("raw", key));

export interface SealParams {
  conversationId: Uint8Array;
  epoch: number;
  senderUserId: Uint8Array;
  clientMessageId: string;
}

export const contentAad = (p: SealParams): Bytes =>
  concat(CONTENT_AAD, p.conversationId, u32(p.epoch), p.senderUserId, encoder.encode(p.clientMessageId));

/** The ciphertext, with its tag, of a serialized Content. */
export async function seal(key: CryptoKey, nonce: Uint8Array, plaintext: Uint8Array, p: SealParams): Promise<Bytes> {
  return encrypt(key, nonce, plaintext, contentAad(p));
}

/** The serialized Content; rejects if the ciphertext or its context was changed. */
export async function open(key: CryptoKey, nonce: Uint8Array, ciphertext: Uint8Array, p: SealParams): Promise<Bytes> {
  return decrypt(key, nonce, ciphertext, contentAad(p));
}

export interface ReactionParams {
  conversationId: Uint8Array;
  epoch: number;
  reactorUserId: Uint8Array;
  seq: bigint;
}

export const reactionAad = (p: ReactionParams): Bytes =>
  concat(REACTION_AAD, p.conversationId, u32(p.epoch), p.reactorUserId, u64(p.seq));

/** The ciphertext, with its tag, of serialized Reactions. */
export async function sealReactions(
  key: CryptoKey,
  nonce: Uint8Array,
  plaintext: Uint8Array,
  p: ReactionParams,
): Promise<Bytes> {
  return encrypt(key, nonce, plaintext, reactionAad(p));
}

/** The serialized Reactions; rejects if the ciphertext or its context was changed. */
export async function openReactions(
  key: CryptoKey,
  nonce: Uint8Array,
  ciphertext: Uint8Array,
  p: ReactionParams,
): Promise<Bytes> {
  return decrypt(key, nonce, ciphertext, reactionAad(p));
}

/** SHA-256 of the bytes. */
export async function sha256(data: Uint8Array): Promise<Bytes> {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", concat(data)));
}

/** An attachment's file encrypted with its own key: nonce || AES-GCM, as uploaded. */
export async function encryptBlob(
  key: Uint8Array,
  file: Uint8Array,
  nonce: Uint8Array = randomBytes(NONCE_BYTES),
): Promise<Bytes> {
  return concat(nonce, await encrypt(await aesKey(key), nonce, file, BLOB_AAD));
}

/** The file of a blob as uploaded, after checking its digest; rejects if either does not match. */
export async function decryptBlob(key: Uint8Array, digest: Uint8Array, blob: Uint8Array): Promise<Bytes> {
  if (compareBytes(await sha256(blob), digest) !== 0) throw new Error("the attachment does not match its digest");
  if (blob.length < NONCE_BYTES + 16) throw new Error("the attachment is too short");
  return decrypt(await aesKey(key), blob.subarray(0, NONCE_BYTES), blob.subarray(NONCE_BYTES), BLOB_AAD);
}

export interface DeviceKey {
  deviceId: bigint;
  publicKey: Uint8Array;
}

/** A user's fingerprint over their devices with keys, by ascending device id. */
export async function fingerprint(userId: Uint8Array, devices: DeviceKey[]): Promise<Bytes> {
  const sorted = [...devices].sort((a, b) => (a.deviceId < b.deviceId ? -1 : a.deviceId > b.deviceId ? 1 : 0));
  const data = concat(FINGERPRINT, userId, ...sorted.flatMap((d) => [u64(d.deviceId), d.publicKey]));
  return new Uint8Array(await crypto.subtle.digest("SHA-256", data));
}

function compareBytes(a: Uint8Array, b: Uint8Array): number {
  for (let i = 0; i < Math.min(a.length, b.length); i++) if (a[i] !== b[i]) return a[i] - b[i];
  return a.length - b.length;
}

/** Six groups of five digits, the same for both users. */
export async function safetyNumber(a: Uint8Array, b: Uint8Array): Promise<string> {
  const [lo, hi] = compareBytes(a, b) <= 0 ? [a, b] : [b, a];
  const h = new Uint8Array(await crypto.subtle.digest("SHA-256", concat(lo, hi)));
  const groups: string[] = [];
  for (let i = 0; i < 30; i += 5) {
    let n = 0;
    for (let j = 0; j < 5; j++) n = n * 256 + h[i + j];
    groups.push((n % 100000).toString().padStart(5, "0"));
  }
  return groups.join(" ");
}
