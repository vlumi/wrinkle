import { avatarText } from "./icons";

test("a user is drawn as their icon, or else their initial", () => {
  expect(avatarText({ name: "bob", icon: "fox" })).toBe("🦊");
  expect(avatarText({ name: "bob", icon: "" })).toBe("B");
  expect(avatarText({ name: "bob", icon: "dragon" })).toBe("B");
  expect(avatarText({ name: "😀 smile", icon: "" })).toBe("😀");
  expect(avatarText(undefined)).toBe("");
});
