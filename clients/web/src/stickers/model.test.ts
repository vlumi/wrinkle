import { beforeFor, MAX_PALETTE, moved, withoutSticker, withSaved } from "./model";

const palette = ["a", "b", "c", "d"];

test("saving appends once, and not to a full palette", () => {
  expect(withSaved(palette, "e")).toEqual(["a", "b", "c", "d", "e"]);
  expect(withSaved(palette, "b")).toBe(palette);
  const full = Array.from({ length: MAX_PALETTE }, (_, i) => `s${i}`);
  expect(withSaved(full, "x")).toBe(full);
});

test("removing drops the sticker if there", () => {
  expect(withoutSticker(palette, "b")).toEqual(["a", "c", "d"]);
  expect(withoutSticker(palette, "x")).toBe(palette);
});

test("moving goes before another sticker, or to the end", () => {
  expect(moved(palette, "d", "b")).toEqual(["a", "d", "b", "c"]);
  expect(moved(palette, "a", "")).toEqual(["b", "c", "d", "a"]);
  expect(moved(palette, "a", "c")).toEqual(["b", "a", "c", "d"]);
  expect(moved(palette, "b", "b")).toBe(palette);
  expect(moved(palette, "a", "b")).toBe(palette);
  expect(moved(palette, "d", "")).toBe(palette);
  expect(moved(palette, "x", "a")).toBe(palette);
  expect(moved(palette, "a", "x")).toBe(palette);
});

test("one place up or down", () => {
  expect(beforeFor(palette, "c", -1)).toBe("b");
  expect(moved(palette, "c", beforeFor(palette, "c", -1)!)).toEqual(["a", "c", "b", "d"]);
  expect(beforeFor(palette, "b", 1)).toBe("d");
  expect(moved(palette, "b", beforeFor(palette, "b", 1)!)).toEqual(["a", "c", "b", "d"]);
  expect(beforeFor(palette, "c", 1)).toBe("");
  expect(moved(palette, "c", "")).toEqual(["a", "b", "d", "c"]);
  expect(beforeFor(palette, "a", -1)).toBeNull();
  expect(beforeFor(palette, "d", 1)).toBeNull();
  expect(beforeFor(palette, "x", 1)).toBeNull();
});
