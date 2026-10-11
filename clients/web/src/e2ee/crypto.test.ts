// @vitest-environment node
// The shared test vectors of clients/go/e2ee/testdata/vectors.json, which
// every client checks so their cryptography agrees bit for bit.

import { create, toBinary } from "@bufbuild/protobuf";
import { ContentSchema, ReactionsSchema } from "../gen/wrinkle/v1/content_pb";
import { fromHex, hex } from "../ids";
import {
  contentAad,
  decryptBlob,
  encryptBlob,
  epochKey,
  fingerprint,
  generateDeviceKey,
  hkdf,
  importPrivateKey,
  open,
  openReactions,
  reactionAad,
  safetyNumber,
  seal,
  sealReactions,
  sha256,
  sharedSecret,
  unwrapKey,
  wrapInfo,
  wrapKey,
} from "./crypto";

const V = {
  rfc7748: {
    alicePrivate: "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a",
    bobPublic: "de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f",
    shared: "4a5d9d5ba4ce2de1728e3bf480350f25e07e21c947d19e3376f09b3c1e161742",
  },
  devices: {
    a1: {
      userId: "a0a1a2a3a4a5a6a7a8a9aaabacadaeaf",
      deviceId: 9223372036854775809n,
      privateKey: "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
      publicKey: "07a37cbc142093c8b755dc1b10e86cb426374ad16aa853ed0bdfc0b2b86d1c7c",
    },
    a2: {
      userId: "a0a1a2a3a4a5a6a7a8a9aaabacadaeaf",
      deviceId: 81985529216486895n,
      privateKey: "2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40",
      publicKey: "5869aff450549732cbaaed5e5df9b30a6da31cb0e5742bad5ad4a1a768f1a67b",
    },
    b1: {
      userId: "b0b1b2b3b4b5b6b7b8b9babbbcbdbebf",
      deviceId: 18364758544493064720n,
      privateKey: "4142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f60",
      publicKey: "64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d48466",
    },
  },
  wrap: {
    conversationId: "c0c1c2c3c4c5c6c7c8c9cacbcccdcecf",
    epoch: 1,
    key: "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f",
    nonce: "101112131415161718191a1b",
    sharedSecret: "26c2c17fdb82161cb21ad16e721315355b64d1763119b10bfc962530dc7cc163",
    info: "7772696e6b6c652077726170207631c0c1c2c3c4c5c6c7c8c9cacbcccdcecf00000001a0a1a2a3a4a5a6a7a8a9aaabacadaeaf8000000000000001b0b1b2b3b4b5b6b7b8b9babbbcbdbebffedcba9876543210",
    wrappingKey: "ecf693d772f86f77a207dace9faf5549d85f6dffa478399ae033420b0698ee05",
    wrapped:
      "101112131415161718191a1be91061dfe93edfe08a2cde959f649d2ffa77902986e697f3f900b36a477dc26a8f89b1c261700531425d918ccf7a73ac",
  },
  seal: {
    key: "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f",
    nonce: "303132333435363738393a3b",
    conversationId: "c0c1c2c3c4c5c6c7c8c9cacbcccdcecf",
    epoch: 1,
    senderUserId: "a0a1a2a3a4a5a6a7a8a9aaabacadaeaf",
    clientMessageId: "client-message-1",
    text: "hello",
    plaintext: "0a0568656c6c6f",
    aad: "7772696e6b6c6520636f6e74656e74207631c0c1c2c3c4c5c6c7c8c9cacbcccdcecf00000001a0a1a2a3a4a5a6a7a8a9aaabacadaeaf636c69656e742d6d6573736167652d31",
    ciphertext: "1fff712ff7e3e70fa783c1fc72407a404036b0a88eaf15",
  },
  reaction: {
    key: "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f",
    nonce: "505152535455565758595a5b",
    conversationId: "c0c1c2c3c4c5c6c7c8c9cacbcccdcecf",
    epoch: 2,
    userId: "b0b1b2b3b4b5b6b7b8b9babbbcbdbebf",
    seq: 4294967298n,
    emoji: ["\u{1f44d}", "\u2764\ufe0f", "\u{1f469}\u200d\u{1f469}\u200d\u{1f467}"],
    plaintext: "0a04f09f918d0a06e29da4efb88f0a12f09f91a9e2808df09f91a9e2808df09f91a7",
    aad: "7772696e6b6c65207265616374696f6e207631c0c1c2c3c4c5c6c7c8c9cacbcccdcecf00000002b0b1b2b3b4b5b6b7b8b9babbbcbdbebf0000000100000002",
    ciphertext: "b88166dab6f40be08cc6afc43711da8db6eb856edea765d83d5ae6b3551376b6bed6b13a5bb740760a174caab460ee45e330",
  },
  blob: {
    key: "606162636465666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f",
    nonce: "707172737475767778797a7b",
    plaintext: "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f2021222324252627",
    aad: "7772696e6b6c6520626c6f62207631",
    blob: "707172737475767778797a7b19fb13fc676977dc5afe2d019e36fe7504bbf65a2a23b9348affc6f9d572e3b949e9edfb8bf0f3a39d7cca5f7501b448f8adbaa294c33031",
    digest: "3a7bc0ed87733b5b648622f83b739c8e64d7efd10182dbd25f2c87f036235373",
  },
  fingerprints: {
    a: "83f8d669a171e66fe9f42fa06af663015c895d9e6b63b6a577514567ca391efc",
    b: "b091119e7a6e849daf44d9740e1f4b585658facb81c69611fa0290457d0b4ff5",
    safetyNumber: "50618 81616 18083 62639 81203 54787",
  },
};

const { a1, a2, b1 } = V.devices;
const ref = (d: typeof a1) => ({ userId: fromHex(d.userId), deviceId: d.deviceId });
const wrapParams = {
  conversationId: fromHex(V.wrap.conversationId),
  epoch: V.wrap.epoch,
  sender: ref(a1),
  recipient: ref(b1),
};
const sealParams = {
  conversationId: fromHex(V.seal.conversationId),
  epoch: V.seal.epoch,
  senderUserId: fromHex(V.seal.senderUserId),
  clientMessageId: V.seal.clientMessageId,
};

test("X25519 as in RFC 7748", async () => {
  const shared = await sharedSecret(
    await importPrivateKey(fromHex(V.rfc7748.alicePrivate)),
    fromHex(V.rfc7748.bobPublic),
  );
  expect(hex(shared)).toBe(V.rfc7748.shared);
});

test("wrapping an epoch key", async () => {
  const a1Private = await importPrivateKey(fromHex(a1.privateKey));
  const b1Private = await importPrivateKey(fromHex(b1.privateKey));
  expect(hex(await sharedSecret(a1Private, fromHex(b1.publicKey)))).toBe(V.wrap.sharedSecret);
  expect(hex(await sharedSecret(b1Private, fromHex(a1.publicKey)))).toBe(V.wrap.sharedSecret);
  expect(hex(wrapInfo(wrapParams))).toBe(V.wrap.info);
  expect(hex(await hkdf(fromHex(V.wrap.sharedSecret), fromHex(V.wrap.info)))).toBe(V.wrap.wrappingKey);
  const wrapped = await wrapKey(
    fromHex(V.wrap.key),
    a1Private,
    fromHex(b1.publicKey),
    wrapParams,
    fromHex(V.wrap.nonce),
  );
  expect(hex(wrapped)).toBe(V.wrap.wrapped);
  expect(hex(await unwrapKey(wrapped, b1Private, fromHex(a1.publicKey), wrapParams))).toBe(V.wrap.key);
  // Made for another device, it does not open.
  await expect(
    unwrapKey(wrapped, b1Private, fromHex(a1.publicKey), { ...wrapParams, recipient: ref(a2) }),
  ).rejects.toThrow();
});

test("sealing content", async () => {
  const plaintext = toBinary(ContentSchema, create(ContentSchema, { body: { case: "text", value: V.seal.text } }));
  expect(hex(plaintext)).toBe(V.seal.plaintext);
  expect(hex(contentAad(sealParams))).toBe(V.seal.aad);
  const key = await epochKey(fromHex(V.seal.key));
  const ciphertext = await seal(key, fromHex(V.seal.nonce), plaintext, sealParams);
  expect(hex(ciphertext)).toBe(V.seal.ciphertext);
  expect(hex(await open(key, fromHex(V.seal.nonce), ciphertext, sealParams))).toBe(V.seal.plaintext);
  // Moved to another message, it does not open.
  await expect(
    open(key, fromHex(V.seal.nonce), ciphertext, { ...sealParams, clientMessageId: "client-message-2" }),
  ).rejects.toThrow();
});

test("sealing reactions", async () => {
  const r = V.reaction;
  const params = {
    conversationId: fromHex(r.conversationId),
    epoch: r.epoch,
    reactorUserId: fromHex(r.userId),
    seq: r.seq,
  };
  const plaintext = toBinary(ReactionsSchema, create(ReactionsSchema, { emoji: r.emoji }));
  expect(hex(plaintext)).toBe(r.plaintext);
  expect(hex(reactionAad(params))).toBe(r.aad);
  const key = await epochKey(fromHex(r.key));
  const ciphertext = await sealReactions(key, fromHex(r.nonce), plaintext, params);
  expect(hex(ciphertext)).toBe(r.ciphertext);
  expect(hex(await openReactions(key, fromHex(r.nonce), ciphertext, params))).toBe(r.plaintext);
  // Moved to another message, or claimed by another user, it does not open.
  await expect(openReactions(key, fromHex(r.nonce), ciphertext, { ...params, seq: r.seq + 1n })).rejects.toThrow();
  await expect(
    openReactions(key, fromHex(r.nonce), ciphertext, { ...params, reactorUserId: fromHex(V.seal.senderUserId) }),
  ).rejects.toThrow();
});

test("encrypting an attachment's blob", async () => {
  const b = V.blob;
  expect(new TextDecoder().decode(fromHex(b.aad))).toBe("wrinkle blob v1");
  const blob = await encryptBlob(fromHex(b.key), fromHex(b.plaintext), fromHex(b.nonce));
  expect(hex(blob)).toBe(b.blob);
  expect(hex(await sha256(blob))).toBe(b.digest);
  expect(hex(await decryptBlob(fromHex(b.key), fromHex(b.digest), blob))).toBe(b.plaintext);
  // A changed blob fails its digest; with the digest changed too, it does not decrypt.
  const changed = blob.slice();
  changed[changed.length - 1] ^= 1;
  await expect(decryptBlob(fromHex(b.key), fromHex(b.digest), changed)).rejects.toThrow();
  await expect(decryptBlob(fromHex(b.key), await sha256(changed), changed)).rejects.toThrow();
  // A fresh nonce each time.
  const again = await encryptBlob(fromHex(b.key), fromHex(b.plaintext));
  expect(hex(again)).not.toBe(b.blob);
  expect(hex(await decryptBlob(fromHex(b.key), await sha256(again), again))).toBe(b.plaintext);
});

test("fingerprints and the safety number", async () => {
  const keyOf = (d: typeof a1) => ({ deviceId: d.deviceId, publicKey: fromHex(d.publicKey) });
  // Device order does not matter: they are sorted by id.
  const fa = await fingerprint(fromHex(a1.userId), [keyOf(a1), keyOf(a2)]);
  const fb = await fingerprint(fromHex(b1.userId), [keyOf(b1)]);
  expect(hex(fa)).toBe(V.fingerprints.a);
  expect(hex(fb)).toBe(V.fingerprints.b);
  expect(await safetyNumber(fa, fb)).toBe(V.fingerprints.safetyNumber);
  expect(await safetyNumber(fb, fa)).toBe(V.fingerprints.safetyNumber);
});

test("generated device keys agree on a shared secret", async () => {
  const x = await generateDeviceKey();
  const y = await generateDeviceKey();
  expect(x.publicKey.length).toBe(32);
  expect(hex(await sharedSecret(x.privateKey, y.publicKey))).toBe(hex(await sharedSecret(y.privateKey, x.publicKey)));
  expect(x.privateKey.extractable).toBe(false);
});
