import type { User } from "../gen/wrinkle/v1/model_pb";
import { ICONS, avatarText } from "./icons";

export function Avatar({ user, large }: { user: User | null | undefined; large?: boolean }) {
  const icon = user ? ICONS[user.icon] : undefined;
  return (
    <span className={`avatar${icon ? " icon" : ""}${large ? " large" : ""}`} data-testid="avatar" aria-hidden="true">
      {avatarText(user ?? undefined)}
    </span>
  );
}
