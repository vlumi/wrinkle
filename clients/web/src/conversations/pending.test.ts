import { hex } from "../ids";
import { addPending, confirmPending, failPending, newMessageId } from "./pending";

test("message ids are 32 hex characters and differ", () => {
  const a = newMessageId();
  expect(a).toMatch(/^[0-9a-f]{32}$/);
  expect(newMessageId()).not.toBe(a);
});

const c7 = new Uint8Array(16).fill(7);
const k7 = hex(c7);

test("a confirmed message leaves the pending list, a failed one stays marked", () => {
  let pending = addPending({}, k7, { clientMessageId: "a", text: "one" });
  pending = addPending(pending, k7, { clientMessageId: "b", text: "two" });
  pending = failPending(pending, k7, "b");
  pending = confirmPending(pending, c7, "a");
  expect(pending[k7]).toEqual([{ clientMessageId: "b", text: "two", failed: true }]);
});

test("confirming a message sent elsewhere changes nothing", () => {
  const pending = addPending({}, k7, { clientMessageId: "a", text: "one" });
  expect(confirmPending(pending, c7, "other")).toBe(pending);
});
