// Notes: the user's conversation with themself, pinned at the top of the
// list. Every user has one, made on the backend when first opened.

import { ConversationKind } from "../gen/wrinkle/v1/model_pb";
import type { Conversation } from "../gen/wrinkle/v1/model_pb";

/** The notes' title, in the list and on screen. */
export const NOTES = "Notes";

export const isNotes = (c: Conversation | undefined): boolean => c?.kind === ConversationKind.NOTES;

/** The user's notes, once known. */
export function notesOf(conversations: Record<string, Conversation>): Conversation | undefined {
  return Object.values(conversations).find(isNotes);
}
