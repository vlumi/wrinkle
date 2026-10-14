// Contacts in the session: listed after every connect, kept current by
// contact events, and changed from the contacts screen, a direct
// conversation or a group's members.

import { create } from "@bufbuild/protobuf";
import { ContactSchema, ContactState } from "../gen/wrinkle/v1/model_pb";
import type { Contact, User } from "../gen/wrinkle/v1/model_pb";
import { withUsers } from "../conversations/model";
import { hex, sameId } from "../ids";
import type { Api } from "../session/api";
import type { Session } from "../session/session";
import { mergeListed, withContact, withProfile } from "./model";

export function contactActions({ set, get, link }: Api) {
  /** Users changed while a listing is on the way, by hex id; null when none is. */
  let touched: Set<string> | null = null;

  async function refreshContacts() {
    if (!link.conn) return;
    const changed = new Set<string>();
    touched = changed;
    try {
      const resp = await link.conn.call("listContacts", { case: "listContacts", value: {} });
      const contacts = mergeListed(get().contacts ?? {}, resp.contacts, changed);
      set({
        contacts,
        users: withUsers(
          get(),
          resp.contacts.map((c) => c.user),
        ).users,
      });
    } finally {
      if (touched === changed) touched = null;
    }
  }

  /** Records a contact's new state, from an event or a response. */
  function apply(c: Contact) {
    if (!c.user) return;
    touched?.add(hex(c.user.userId));
    set({
      contacts: withContact(get().contacts ?? {}, c),
      users: c.state === ContactState.UNSPECIFIED ? get().users : withUsers(get(), [c.user]).users,
    });
  }

  /** Records the state a change is known to lead to before its event arrives. */
  function anticipate(userId: Uint8Array, state: ContactState) {
    const user = knownUser(userId);
    if (user) apply(create(ContactSchema, { user, state }));
  }

  function knownUser(userId: Uint8Array): User | undefined {
    const id = hex(userId);
    const { contacts, users, conversations } = get();
    return (
      contacts?.[id]?.user ??
      users[id] ??
      Object.values(conversations).find((c) => sameId(c.peer?.userId, userId))?.peer
    );
  }

  function profileChanged(user: User) {
    const contacts = get().contacts;
    if (contacts) set({ contacts: withProfile(contacts, user) });
  }

  const actions = {
    showContacts(open) {
      set({ contactsShown: open, ...(open ? { settings: false } : {}) });
    },

    async addContact(user) {
      const resp = await link.call("addContact", {
        case: "addContact",
        value: { user: "name" in user ? { case: "name", value: user.name } : { case: "userId", value: user.userId } },
      });
      if (resp.contact) apply(resp.contact);
      return resp.contact?.state ?? ContactState.UNSPECIFIED;
    },

    async removeContact(userId) {
      await link.call("removeContact", { case: "removeContact", value: { userId } });
      // A block stays; anything else ends for both.
      if (get().contacts?.[hex(userId)]?.state !== ContactState.BLOCKED) anticipate(userId, ContactState.UNSPECIFIED);
    },

    async blockUser(userId) {
      await link.call("blockUser", { case: "blockUser", value: { userId } });
      anticipate(userId, ContactState.BLOCKED);
    },

    async unblockUser(userId) {
      await link.call("unblockUser", { case: "unblockUser", value: { userId } });
      if (get().contacts?.[hex(userId)]?.state === ContactState.BLOCKED) anticipate(userId, ContactState.UNSPECIFIED);
    },
  } satisfies Partial<Session>;

  return { actions, refreshContacts, apply, profileChanged };
}
