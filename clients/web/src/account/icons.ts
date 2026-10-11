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

/** What stands for a user: their icon, or else the first letter of their name. */
export function avatarText(user: { name: string; icon: string } | undefined): string {
  if (!user) return "";
  return ICONS[user.icon] ?? Array.from(user.name)[0]?.toUpperCase() ?? "";
}
