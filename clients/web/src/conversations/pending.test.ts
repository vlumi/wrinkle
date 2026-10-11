import { hex } from "../ids";
import { addPending, confirmPending, failPending, newMessageId, retryPending, withProgress } from "./pending";

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

test("files being sent show their progress, and a failed send can go again", () => {
  const file = { name: "a.jpg", preview: "", progress: 0 };
  let pending = addPending({}, k7, { clientMessageId: "a", text: "", attachments: [file, file] });
  pending = withProgress(pending, k7, "a", 1, 0.5);
  expect(pending[k7][0].attachments?.map((f) => f.progress)).toEqual([0, 0.5]);
  pending = failPending(pending, k7, "a");
  expect(pending[k7][0].failed).toBe(true);
  pending = retryPending(pending, k7, "a");
  expect(pending[k7][0].failed).toBe(false);
  expect(withProgress(pending, k7, "other", 0, 1)).toBe(pending);
});
