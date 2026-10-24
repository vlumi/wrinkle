// Conversation list entries known to exist. Entries are never removed, so a
// remembered one stays true; the cache starts over when full.
import type { UUID } from "../internal/ids.ts";

const maxListed = 1 << 20;

const keyOf = (userId: UUID, conversationId: UUID) => userId.toString("latin1") + conversationId.toString("latin1");

export class ListedCache {
  private entries = new Set<string>();

  has(userId: UUID, conversationId: UUID): boolean {
    return this.entries.has(keyOf(userId, conversationId));
  }

  add(userId: UUID, conversationId: UUID): void {
    if (this.entries.size >= maxListed) this.entries = new Set();
    this.entries.add(keyOf(userId, conversationId));
  }
}
