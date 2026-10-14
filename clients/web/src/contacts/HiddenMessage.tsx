import { useState } from "react";
import type { ReactNode } from "react";
import { BLOCKED_MESSAGE } from "./model";

/** A blocked user's message in a group: collapsed until the user asks to see it. */
export function HiddenMessage({ children }: { children: ReactNode }) {
  const [shown, setShown] = useState(false);
  if (shown) return children;
  return (
    <li className="system hidden-message">
      <button type="button" data-testid="hidden-message" aria-expanded={false} onClick={() => setShown(true)}>
        {BLOCKED_MESSAGE}
      </button>
    </li>
  );
}
