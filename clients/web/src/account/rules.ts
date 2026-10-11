// The account rules of the contract, checked before asking the backend, so
// the user learns what is wrong without a round trip.

const bytes = (s: string) => new TextEncoder().encode(s).length;

/** What is wrong with a name, or null. */
export function nameProblem(name: string): string | null {
  if (name === "") return "Enter a name.";
  if (bytes(name) > 64) return "Names are at most 64 bytes.";
  if (/^\p{White_Space}|\p{White_Space}$/u.test(name)) return "Names cannot start or end with a space.";
  if (/\p{Cc}/u.test(name)) return "Names cannot contain control characters.";
  return null;
}

/** What is wrong with a new password, or null. */
export function passwordProblem(password: string): string | null {
  if (bytes(password) < 8) return "Passwords are at least 8 bytes.";
  if (bytes(password) > 256) return "Passwords are at most 256 bytes.";
  return null;
}

export const NAME_RULES = "Names are 1–64 bytes, without leading or trailing spaces.";
export const PASSWORD_RULES = "Passwords are at least 8 bytes.";
