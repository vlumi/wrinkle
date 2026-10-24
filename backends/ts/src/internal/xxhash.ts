// XXH64, for the keyspace ids of Vitess's xxhash vindex.
const mask = (1n << 64n) - 1n;
const p1 = 0x9e3779b185ebca87n;
const p2 = 0xc2b2ae3d27d4eb4fn;
const p3 = 0x165667b19e3779f9n;
const p4 = 0x85ebca77c2b2ae63n;
const p5 = 0x27d4eb2f165667c5n;

const rotl = (x: bigint, r: bigint) => ((x << r) | (x >> (64n - r))) & mask;

function round(acc: bigint, input: bigint): bigint {
  acc = (acc + input * p2) & mask;
  return (rotl(acc, 31n) * p1) & mask;
}

function merge(acc: bigint, val: bigint): bigint {
  acc ^= round(0n, val);
  return (acc * p1 + p4) & mask;
}

/** The 64-bit xxhash of the bytes, seed 0. */
export function xxh64(b: Uint8Array): bigint {
  const view = new DataView(b.buffer, b.byteOffset, b.byteLength);
  const n = b.length;
  let i = 0;
  let h: bigint;
  if (n >= 32) {
    let v1 = (p1 + p2) & mask;
    let v2 = p2;
    let v3 = 0n;
    let v4 = -p1 & mask;
    for (; i + 32 <= n; i += 32) {
      v1 = round(v1, view.getBigUint64(i, true));
      v2 = round(v2, view.getBigUint64(i + 8, true));
      v3 = round(v3, view.getBigUint64(i + 16, true));
      v4 = round(v4, view.getBigUint64(i + 24, true));
    }
    h = (rotl(v1, 1n) + rotl(v2, 7n) + rotl(v3, 12n) + rotl(v4, 18n)) & mask;
    h = merge(h, v1);
    h = merge(h, v2);
    h = merge(h, v3);
    h = merge(h, v4);
  } else {
    h = p5;
  }
  h = (h + BigInt(n)) & mask;
  for (; i + 8 <= n; i += 8) {
    h ^= round(0n, view.getBigUint64(i, true));
    h = (rotl(h, 27n) * p1 + p4) & mask;
  }
  if (i + 4 <= n) {
    h ^= (BigInt(view.getUint32(i, true)) * p1) & mask;
    h = (rotl(h, 23n) * p2 + p3) & mask;
    i += 4;
  }
  for (; i < n; i++) {
    h ^= (BigInt(b[i]!) * p5) & mask;
    h = (rotl(h, 11n) * p1) & mask;
  }
  h ^= h >> 33n;
  h = (h * p2) & mask;
  h ^= h >> 29n;
  h = (h * p3) & mask;
  h ^= h >> 32n;
  return h;
}
