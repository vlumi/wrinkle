import { fromHex, hex, sameId } from "./ids";

test("ids round-trip through lowercase hex", () => {
  const id = new Uint8Array([0xff, 0, 1, 0xab, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x10, 0x01]);
  expect(hex(id)).toBe("ff0001ab000000000000000000001001");
  expect(fromHex(hex(id))).toEqual(id);
});

test("ids compare by their bytes", () => {
  expect(sameId(new Uint8Array([1, 2]), new Uint8Array([1, 2]))).toBe(true);
  expect(sameId(new Uint8Array([1, 2]), new Uint8Array([1, 3]))).toBe(false);
  expect(sameId(new Uint8Array([1, 2]), undefined)).toBe(false);
});
