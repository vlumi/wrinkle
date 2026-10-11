import { changedSinceVerified } from "./verified";

test("a verified contact's warning shows only when the fingerprint differs from the one verified", () => {
  expect(changedSinceVerified({ a: "01" }, { a: "01" }, "a")).toBe(false);
  expect(changedSinceVerified({ a: "01" }, { a: "02" }, "a")).toBe(true);
  expect(changedSinceVerified({}, { a: "02" }, "a")).toBe(false);
  // Not looked up yet: nothing to compare.
  expect(changedSinceVerified({ a: "01" }, {}, "a")).toBe(false);
});
