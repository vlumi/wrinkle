// The standard icons, by key, and the emoji they are drawn as.

export const ICONS: Record<string, string> = {
  cat: "🐱",
  dog: "🐶",
  fox: "🦊",
  owl: "🦉",
  bear: "🐻",
  frog: "🐸",
  panda: "🐼",
  rabbit: "🐰",
  tiger: "🐯",
  penguin: "🐧",
  octopus: "🐙",
  unicorn: "🦄",
};

/** How a user is named on screen. */
export function displayName(user: { name: string; deleted?: boolean } | undefined): string {
  if (!user) return "";
  return user.deleted ? "Deleted account" : user.name;
}

/** What stands for a user: their icon, or else the first letter of their name; nothing for a deleted account. */
export function avatarText(user: { name: string; icon: string; deleted?: boolean } | undefined): string {
  if (!user || user.deleted) return "";
  return ICONS[user.icon] ?? Array.from(user.name)[0]?.toUpperCase() ?? "";
}
