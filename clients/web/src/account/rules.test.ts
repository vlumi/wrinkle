import { nameProblem, passwordProblem } from "./rules";

test("names are 1 to 64 bytes without surrounding space or control characters", () => {
  expect(nameProblem("bob")).toBeNull();
  expect(nameProblem("Zoë Ångström")).toBeNull();
  expect(nameProblem("")).not.toBeNull();
  expect(nameProblem("a".repeat(64))).toBeNull();
  expect(nameProblem("a".repeat(65))).not.toBeNull();
  expect(nameProblem("ä".repeat(33))).not.toBeNull();
  expect(nameProblem(" bob")).not.toBeNull();
  expect(nameProblem("bob ")).not.toBeNull();
  expect(nameProblem("bo\u0007b")).not.toBeNull();
});

test("passwords are 8 to 256 bytes", () => {
  expect(passwordProblem("1234567")).not.toBeNull();
  expect(passwordProblem("12345678")).toBeNull();
  expect(passwordProblem("ääää")).toBeNull();
  expect(passwordProblem("x".repeat(257))).not.toBeNull();
});
