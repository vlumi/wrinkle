import { addPending, confirmPending, failPending, newMessageId } from "./pending";

test("message ids are 32 hex characters and differ", () => {
  const a = newMessageId();
  expect(a).toMatch(/^[0-9a-f]{32}$/);
  expect(newMessageId()).not.toBe(a);
});

test("a confirmed message leaves the pending list, a failed one stays marked", () => {
  let pending = addPending({}, "7", { clientMessageId: "a", text: "one" });
  pending = addPending(pending, "7", { clientMessageId: "b", text: "two" });
  pending = failPending(pending, "7", "b");
  pending = confirmPending(pending, 7n, "a");
  expect(pending["7"]).toEqual([{ clientMessageId: "b", text: "two", failed: true }]);
});

test("confirming a message sent elsewhere changes nothing", () => {
  const pending = addPending({}, "7", { clientMessageId: "a", text: "one" });
  expect(confirmPending(pending, 7n, "other")).toBe(pending);
});
