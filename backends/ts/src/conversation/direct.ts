// The members of direct conversations and notes, which never change, so a
// send's block check needs no read of the row.
import { hex, type UUID } from "../internal/ids.ts";
import { kindDirect } from "./state.ts";

// Bounds the cache; it starts over when full.
const maxDirect = 1 << 20;

interface Entry {
  members: UUID[];
  /** A direct conversation rather than notes. */
  direct: boolean;
}

export class DirectMembers {
  private entries = new Map<string, Entry>();

  /** The conversation's members and whether it is direct, or undefined when not remembered. */
  get(conversationId: UUID): Entry | undefined {
    return this.entries.get(hex(conversationId));
  }

  add(conversationId: UUID, kind: string, members: UUID[]): void {
    if (this.entries.size >= maxDirect) this.entries = new Map();
    this.entries.set(hex(conversationId), { members, direct: kind === kindDirect });
  }
}
