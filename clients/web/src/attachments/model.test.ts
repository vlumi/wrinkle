import { create } from "@bufbuild/protobuf";
import { AttachmentsSchema } from "../gen/wrinkle/v1/content_pb";
import {
  attachmentsText,
  cleanName,
  CONTENT_BUDGET,
  fitted,
  formatSize,
  isImage,
  isResizable,
  isValid,
  MAX_THUMBNAIL_BYTES,
  thumbnailBudget,
} from "./model";

const photo = { mediaType: "image/jpeg", width: 2048, height: 1536, name: "a.jpg" };
const file = { mediaType: "application/pdf", width: 0, height: 0, name: "report.pdf" };

test("images are scaled down to fit the longest side, never up", () => {
  expect(fitted(4000, 3000, 2048)).toEqual({ width: 2048, height: 1536 });
  expect(fitted(1000, 4000, 2048)).toEqual({ width: 512, height: 2048 });
  expect(fitted(300, 200, 2048)).toEqual({ width: 300, height: 200 });
  expect(fitted(10000, 1, 320)).toEqual({ width: 320, height: 1 });
});

test("GIFs go as they are; photos are resized", () => {
  expect(isResizable("image/png")).toBe(true);
  expect(isResizable("image/gif")).toBe(false);
  expect(isImage(photo)).toBe(true);
  expect(isImage({ ...photo, width: 0 })).toBe(false);
  expect(isImage(file)).toBe(false);
});

test("thumbnails share what the content leaves", () => {
  const items = (n: number) => Array.from({ length: n }, () => photo);
  expect(thumbnailBudget("", items(1), 1)).toBe(MAX_THUMBNAIL_BYTES);
  expect(thumbnailBudget("", [file], 0)).toBe(0);
  const ten = thumbnailBudget("", items(10), 10);
  expect(ten).toBeLessThan(MAX_THUMBNAIL_BYTES);
  expect(ten * 10).toBeLessThan(CONTENT_BUDGET);
  // A long caption leaves less.
  expect(thumbnailBudget("é".repeat(4000), items(10), 10)).toBeLessThan(ten);
});

test("names lose control characters and are cut", () => {
  expect(cleanName(" a\nb.txt ")).toBe("ab.txt");
  expect(cleanName("")).toBe("file");
  expect([...cleanName("x".repeat(500))].length).toBe(200);
});

test("what a message with attachments reads as", () => {
  const of = (items: (typeof photo)[], caption = "") =>
    attachmentsText({ items: items.map((i) => ({ ...i })) as never, caption });
  expect(of([photo])).toBe("Photo");
  expect(of([photo, photo, photo])).toBe("3 photos");
  expect(of([file])).toBe("File: report.pdf");
  expect(of([photo, file])).toBe("2 attachments");
  expect(of([photo], "look")).toBe("look");
});

test("sizes for people", () => {
  expect(formatSize(512)).toBe("512 B");
  expect(formatSize(1536)).toBe("1.5 KB");
  expect(formatSize(50 * 1024)).toBe("50 KB");
  expect(formatSize(3n * 1024n * 1024n)).toBe("3.0 MB");
});

test("received attachments need ids, keys and digests", () => {
  const item = { blobId: new Uint8Array(16), key: new Uint8Array(32), digest: new Uint8Array(32) };
  expect(isValid(create(AttachmentsSchema, { items: [item] }))).toBe(true);
  expect(isValid(create(AttachmentsSchema, { items: [] }))).toBe(false);
  expect(isValid(create(AttachmentsSchema, { items: [{ ...item, key: new Uint8Array(16) }] }))).toBe(false);
  expect(isValid(create(AttachmentsSchema, { items: Array.from({ length: 11 }, () => item) }))).toBe(false);
});
