import { create } from "@bufbuild/protobuf";
import { ReactionSchema, SealedSchema } from "../gen/wrinkle/v1/model_pb";
import { hex } from "../ids";
import { chips, cleaned, isEmoji, ownEmoji, toggled, withListed, withOpened, withReaction } from "./model";

const id = (n: number) => new Uint8Array(16).fill(n);
const me = id(1);
const bob = id(2);
const sealed = (n: number) => create(SealedSchema, { epoch: 1, nonce: new Uint8Array(12).fill(n) });
const at = (seconds: number) => ({ seconds: BigInt(seconds), nanos: 0 }) as never;
const M = "c7/3";

test("one emoji, of at most 32 bytes", () => {
  for (const e of ["👍", "❤️", "👩‍👩‍👧", "🇫🇮", "1️⃣"]) expect(isEmoji(e)).toBe(true);
  for (const e of ["", "a", "👍👍", "ok", "👍 "]) expect(isEmoji(e)).toBe(false);
  expect(cleaned(["👍", "👍", "❤️", "😂", "😮"])).toEqual(["👍", "❤️", "😂"]);
  expect(cleaned(["👍👍", "🙏"])).toEqual(["🙏"]);
});

test("toggling adds up to three and removes", () => {
  expect(toggled([], "👍")).toEqual(["👍"]);
  expect(toggled(["👍", "❤️"], "👍")).toEqual(["❤️"]);
  expect(toggled(["👍", "❤️", "😂"], "🙏")).toBeNull();
  expect(toggled(["👍", "❤️", "😂"], "😂")).toEqual(["👍", "❤️"]);
});

test("events set, replace and remove a user's set", () => {
  let r = withReaction({}, M, bob, sealed(1), at(10));
  r = withOpened(r, M, hex(bob), sealed(1), ["👍"]);
  r = withReaction(r, M, me, sealed(2), at(20), ["👍", "❤️"]);
  expect(chips(r[M], me)).toEqual([
    { emoji: "👍", users: [hex(bob), hex(me)], mine: true },
    { emoji: "❤️", users: [hex(me)], mine: true },
  ]);
  expect(ownEmoji(r[M], me)).toEqual(["👍", "❤️"]);
  // A replaced set is opened again; until then it shows nothing.
  r = withReaction(r, M, bob, sealed(3), at(30));
  expect(chips(r[M], me).map((c) => c.users.length)).toEqual([1, 1]);
  // The opened emoji of an older set no longer apply.
  expect(withOpened(r, M, hex(bob), sealed(1), ["😂"])).toBe(r);
  r = withReaction(r, M, me, undefined, at(40));
  r = withReaction(r, M, bob, undefined, at(41));
  expect(r).toEqual({});
  expect(withReaction(r, M, bob, undefined, at(42))).toBe(r);
});

test("a listed page replaces the message's sets, keeping what was opened", () => {
  let r = withReaction({}, M, bob, sealed(1), at(10), ["👍"]);
  r = withReaction(r, M, me, sealed(2), at(11), ["❤️"]);
  r = withListed(r, M, [create(ReactionSchema, { userId: bob, sealed: sealed(1), updatedAt: at(10) })]);
  expect(chips(r[M], me)).toEqual([{ emoji: "👍", users: [hex(bob)], mine: false }]);
  expect(withListed(r, M, [])).toEqual({});
});

test("undecryptable sets are ignored", () => {
  let r = withReaction({}, M, bob, sealed(1), at(10));
  r = withOpened(r, M, hex(bob), sealed(1), null);
  expect(chips(r[M], me)).toEqual([]);
});
