// What a feature's actions are built from: the store's set and get, the
// device's link, and what every feature needs of the session.

import type { Conversation } from "../gen/wrinkle/v1/model_pb";
import type { Link } from "./link";
import type { Session } from "./session";

export interface Api {
  set: (partial: Partial<Session>) => void;
  get: () => Session;
  link: Link;
  /** Shows the error in the banner. */
  fail: (err: unknown) => void;
  /** Forgets the device and returns to the sign-in screen. */
  signedOut: (notice: string | null) => void;
  /** Runs the function on sign-out, for a feature's state kept outside the store. */
  onSignOut: (fn: () => void) => void;
  /** The conversation on screen, while connected. */
  current: () => Conversation;
}
