import { smallest, stickerSides } from "./picture";

test("sides start at the picture's, at most 512, and get smaller", () => {
  expect(stickerSides(1200)).toEqual([512, 384, 256, 192, 128]);
  expect(stickerSides(100)).toEqual([100, 75, 50, 38, 25]);
  expect(stickerSides(2)).toEqual([2, 1]);
});

test("the smallest encoding of the types asked wins", () => {
  const png = new Blob(["xxxx"], { type: "image/png" });
  const webp = new Blob(["xx"], { type: "image/webp" });
  const jpeg = new Blob(["x"], { type: "image/jpeg" });
  expect(smallest([png, webp], ["image/png", "image/webp"])).toBe(webp);
  expect(smallest([png, null, jpeg], ["image/png", "image/webp"])).toBe(png);
  expect(smallest([null], ["image/png"])).toBeNull();
});
