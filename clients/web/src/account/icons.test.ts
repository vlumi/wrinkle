import { avatarText, displayName } from "./icons";

test("a user is drawn as their icon, or else their initial", () => {
  expect(avatarText({ name: "bob", icon: "fox" })).toBe("🦊");
  expect(avatarText({ name: "bob", icon: "" })).toBe("B");
  expect(avatarText({ name: "bob", icon: "dragon" })).toBe("B");
  expect(avatarText({ name: "😀 smile", icon: "" })).toBe("😀");
  expect(avatarText(undefined)).toBe("");
});

test("a deleted account has a fixed name and no initial", () => {
  expect(displayName({ name: "bob" })).toBe("bob");
  expect(displayName({ name: "", deleted: true })).toBe("Deleted account");
  expect(displayName(undefined)).toBe("");
  expect(avatarText({ name: "", icon: "", deleted: true })).toBe("");
});
