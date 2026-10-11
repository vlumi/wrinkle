import { useState } from "react";
import { blobAddress, blobURL } from "../blobs/blobs";
import type { User } from "../gen/wrinkle/v1/model_pb";
import { useSession } from "../session/session";
import { ICONS, avatarText } from "./icons";

export function Avatar({ user, large }: { user: User | null | undefined; large?: boolean }) {
  const gateway = useSession((s) => s.credentials?.url);
  // A picture that would not load falls back to the icon.
  const [failed, setFailed] = useState<string | null>(null);
  const address = gateway ? blobAddress(gateway) : null;
  const picture = user && !user.deleted && user.avatar.length > 0 && address ? blobURL(address, user.avatar) : null;
  const shown = picture && picture !== failed ? picture : null;
  const icon = user && !shown ? ICONS[user.icon] : undefined;
  const kind = user?.deleted ? " deleted" : shown ? " picture" : icon ? " icon" : "";

  return (
    <span className={`avatar${kind}${large ? " large" : ""}`} data-testid="avatar" aria-hidden="true">
      {shown ? <img src={shown} alt="" onError={() => setFailed(shown)} /> : avatarText(user ?? undefined)}
    </span>
  );
}
