// User and conversation ids are 16 bytes on the wire; the client keys its
// maps, routes and storage by their lowercase hex.

export function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

export function fromHex(s: string): Uint8Array {
  const bytes = new Uint8Array(s.length >> 1);
  for (let i = 0; i < bytes.length; i++) bytes[i] = parseInt(s.slice(2 * i, 2 * i + 2), 16);
  return bytes;
}

export function sameId(a: Uint8Array | undefined, b: Uint8Array | undefined): boolean {
  if (!a || !b || a.length !== b.length) return false;
  return a.every((x, i) => x === b[i]);
}
