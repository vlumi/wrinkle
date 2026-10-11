import { squareCrop } from "./picture";

test("a landscape picture is cropped to its center and scaled down", () => {
  expect(squareCrop(1600, 1200)).toEqual({ sx: 200, sy: 0, side: 1200, size: 512 });
});

test("a portrait picture is cropped to its vertical center", () => {
  expect(squareCrop(300, 501)).toEqual({ sx: 0, sy: 100, side: 300, size: 300 });
});

test("a small square is kept as it is", () => {
  expect(squareCrop(64, 64)).toEqual({ sx: 0, sy: 0, side: 64, size: 64 });
  expect(squareCrop(512, 512)).toEqual({ sx: 0, sy: 0, side: 512, size: 512 });
});

test("the limit can be set", () => {
  expect(squareCrop(1000, 800, 256)).toEqual({ sx: 100, sy: 0, side: 800, size: 256 });
});
