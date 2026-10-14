// Contacts as the device keeps them: each user the signed-in user has a
// state with, by hex id, listed on connect and kept current by events. Pure
// functions over plain data, so they are tested without a connection.

import { BackendError } from "../connection/connection";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import { ContactState } from "../gen/wrinkle/v1/model_pb";
import type { Contact, User } from "../gen/wrinkle/v1/model_pb";
import { hex } from "../ids";
import type { Reactions } from "../reactions/model";

/** By the other user's hex id; users without a state are left out. */
export type Contacts = Record<string, Contact>;

/** What a blocked user's message in a group shows until opened. */
export const BLOCKED_MESSAGE = "Message from a blocked user";

const idOf = (c: Contact): string => (c.user ? hex(c.user.userId) : "");

/** The contacts as ListContacts returned them. */
export function listed(list: Contact[]): Contacts {
  const out: Contacts = {};
  for (const c of list) if (c.user && c.state !== ContactState.UNSPECIFIED) out[idOf(c)] = c;
  return out;
}

/** The contacts after a change: an unspecified state removes the user. */
export function withContact(contacts: Contacts, c: Contact): Contacts {
  const id = idOf(c);
  if (!id) return contacts;
  if (c.state !== ContactState.UNSPECIFIED) return { ...contacts, [id]: c };
  if (!contacts[id]) return contacts;
  return Object.fromEntries(Object.entries(contacts).filter(([k]) => k !== id));
}

/**
 * A listing merged with what events changed while it was on the way: for
 * those users the events are newer than the listing.
 */
export function mergeListed(local: Contacts, list: Contact[], touched: ReadonlySet<string>): Contacts {
  const out = Object.entries(listed(list)).filter(([id]) => !touched.has(id));
  for (const id of touched) if (local[id]) out.push([id, local[id]]);
  return Object.fromEntries(out);
}

/** The contacts with a user's new profile, if they are among them. */
export function withProfile(contacts: Contacts, user: User): Contacts {
  const id = hex(user.userId);
  const c = contacts[id];
  return c ? { ...contacts, [id]: { ...c, user } } : contacts;
}

export function stateOf(contacts: Contacts | null, userId: Uint8Array | undefined): ContactState {
  if (!contacts || !userId) return ContactState.UNSPECIFIED;
  return contacts[hex(userId)]?.state ?? ContactState.UNSPECIFIED;
}

/** The hex ids of the users the signed-in user blocked. */
export function blockedIds(contacts: Contacts | null): ReadonlySet<string> {
  return new Set(
    Object.entries(contacts ?? {})
      .filter(([, c]) => c.state === ContactState.BLOCKED)
      .map(([id]) => id),
  );
}

export function incomingCount(contacts: Contacts | null): number {
  return Object.values(contacts ?? {}).filter((c) => c.state === ContactState.INCOMING).length;
}

const byName = (a: Contact, b: Contact): number => (a.user?.name ?? "").localeCompare(b.user?.name ?? "");

export interface Sections {
  incoming: Contact[];
  outgoing: Contact[];
  contacts: Contact[];
  blocked: Contact[];
}

/** The contacts screen's sections, each by name. */
export function sections(contacts: Contacts | null): Sections {
  const all = Object.values(contacts ?? {}).sort(byName);
  const of = (s: ContactState) => all.filter((c) => c.state === s);
  return {
    incoming: of(ContactState.INCOMING),
    outgoing: of(ContactState.OUTGOING),
    contacts: of(ContactState.CONTACT),
    blocked: of(ContactState.BLOCKED),
  };
}

/** Contacts to pick from, by name: those matching the filter, ignoring case, and not excluded. */
export function pickable(contacts: Contacts | null, filter: string, exclude: ReadonlySet<string>): User[] {
  const f = filter.trim().toLowerCase();
  return sections(contacts)
    .contacts.flatMap((c) => (c.user ? [c.user] : []))
    .filter((u) => !exclude.has(hex(u.userId)) && (f === "" || u.name.toLowerCase().includes(f)));
}

/** A message's reactions without those of blocked users. */
export function withoutBlocked(
  users: Reactions[string] | undefined,
  blocked: ReadonlySet<string>,
): Reactions[string] | undefined {
  if (!users || blocked.size === 0 || !Object.keys(users).some((u) => blocked.has(u))) return users;
  return Object.fromEntries(Object.entries(users).filter(([u]) => !blocked.has(u)));
}

const NOT_CONTACT = "not a contact: ";

/** The name in a group request refused for a user not in the contacts, or null for any other error. */
export function notContact(err: unknown): string | null {
  if (!(err instanceof BackendError) || err.code !== ErrorCode.FAILED_PRECONDITION) return null;
  return err.message.startsWith(NOT_CONTACT) ? err.message.slice(NOT_CONTACT.length) : null;
}
