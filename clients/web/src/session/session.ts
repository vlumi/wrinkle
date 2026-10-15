// The device's session: the store the screens read, composed from each
// feature's actions over the device's link (its connection, keys and
// cursor), which lives outside React state.

import { create } from "zustand";
import { accountActions } from "../account/actions";
import { attachmentActions } from "../attachments/send";
import type { ConnectionState } from "../connection/connection";
import { contactActions } from "../contacts/actions";
import type { Contacts } from "../contacts/model";
import { conversationActions, isVisible } from "../conversations/actions";
import { emptyData } from "../conversations/model";
import type { Data } from "../conversations/model";
import type { PendingByConversation } from "../conversations/pending";
import { sendActions } from "../conversations/send";
import { deviceActions } from "../devices/actions";
import { keyActions } from "../e2ee/actions";
import { opening } from "../e2ee/opening";
import { sharingActions } from "../e2ee/sharing";
import { deleteStore, storeName } from "../e2ee/store";
import type { Verified } from "../e2ee/verified";
import type { ContactState, Device, User } from "../gen/wrinkle/v1/model_pb";
import { groupActions } from "../groups/actions";
import { notesActions } from "../notes/actions";
import { reactionActions } from "../reactions/actions";
import { stickerActions } from "../stickers/actions";
import type { Api } from "./api";
import { connectActions } from "./connect";
import { Link } from "./link";
import { saved } from "./saved";
import type { Credentials } from "./saved";
import { sync } from "./sync";

export type AuthMode = "signIn" | "register";

/** Another device of the user that signed in and may get the message history. */
export interface NewDevice {
  deviceId: bigint;
  name: string;
  publicKey: Uint8Array;
}

export interface Session extends Data {
  credentials: Credentials | null;
  me: User | null;
  /** Connected means Connect completed, not only that the socket is open. */
  status: ConnectionState;
  /** The conversation on screen. */
  current: string | null;
  /** Conversations whose history is loaded back to its start, by id. */
  complete: Record<string, true>;
  /** Settings on screen, in place of the conversation. */
  settings: boolean;
  /** The contacts screen on screen, in place of the conversation. */
  contactsShown: boolean;
  /** Users the user has a state with, once listed. */
  contacts: Contacts | null;
  pending: PendingByConversation;
  /** The account's signed-in devices, once listed. */
  devices: Device[] | null;
  error: string | null;
  /** Why the device was signed out, for the sign-in screen. */
  notice: string | null;
  /** Users' fingerprints in hex, by hex id, for those looked up. */
  fingerprints: Record<string, string>;
  /** Contacts the user verified, with the fingerprint verified. */
  verified: Verified;
  /** A new device of the user waiting for the decision on sharing the history. */
  newDevice: NewDevice | null;
  /** What changed about the account's encryption, until dismissed. */
  securityNotice: string | null;
  /** The user's saved stickers, ids in hex in the user's order, once loaded. */
  palette: string[] | null;
  /** Reconnects with the saved credentials, if any. */
  resume: () => void;
  /** Registers or signs in, then connects; rejects with the backend's error. */
  authenticate: (mode: AuthMode, name: string, password: string, url: string) => Promise<void>;
  signOut: () => Promise<void>;
  updateProfile: (change: { name?: string; icon?: string; avatar?: Uint8Array }) => Promise<void>;
  /** Uploads a prepared picture and makes it the avatar. */
  setPicture: (picture: Blob) => Promise<void>;
  changePassword: (current: string, next: string) => Promise<void>;
  loadDevices: () => Promise<void>;
  renameDevice: (deviceId: bigint, name: string) => Promise<void>;
  signOutDevice: (deviceId: bigint) => Promise<void>;
  deleteAccount: (password: string) => Promise<void>;
  openConversation: (peerName: string) => Promise<void>;
  /** Shows the user's notes, made on the backend the first time. */
  openNotes: () => Promise<void>;
  /** Creates a group and shows it; the same clientGroupId on a retry returns the group created first. */
  createGroup: (topic: string, memberNames: string[], clientGroupId: string) => Promise<void>;
  /** Lists the current group's members again. */
  loadMembers: (conversationId: string) => Promise<void>;
  addMembers: (memberNames: string[]) => Promise<void>;
  removeMember: (userId: Uint8Array) => Promise<void>;
  setAdmin: (userId: Uint8Array, admin: boolean) => Promise<void>;
  setTopic: (topic: string) => Promise<void>;
  leaveGroup: () => Promise<void>;
  show: (conversationId: string | null) => Promise<void>;
  showSettings: (open: boolean) => void;
  showContacts: (open: boolean) => void;
  /** Asks a user to be a contact, or accepts their request; resolves with how they stand now. */
  addContact: (user: { name: string } | { userId: Uint8Array }) => Promise<ContactState>;
  /** Removes a contact, declines their request or withdraws one's own. */
  removeContact: (userId: Uint8Array) => Promise<void>;
  blockUser: (userId: Uint8Array) => Promise<void>;
  unblockUser: (userId: Uint8Array) => Promise<void>;
  loadEarlier: () => Promise<void>;
  send: (text: string) => Promise<void>;
  /** Sends files, up to 10, with an optional caption, to the current conversation. */
  sendAttachments: (files: File[], caption: string) => Promise<void>;
  /** Sends a message that failed again, as the same message. */
  retrySend: (clientMessageId: string) => Promise<void>;
  /** Sends a sticker, by its id in hex, to the current conversation. */
  sendSticker: (stickerId: string) => Promise<void>;
  /** Uploads a prepared picture as a new sticker, which the backend saves to the palette. */
  createSticker: (picture: Blob) => Promise<void>;
  saveSticker: (stickerId: string) => Promise<void>;
  removeSticker: (stickerId: string) => Promise<void>;
  /** Moves a saved sticker before another, or to the end when before is empty. */
  moveSticker: (stickerId: string, before: string) => Promise<void>;
  /** Deletes one of the user's own messages in the current conversation, for everyone. */
  deleteMessage: (seq: bigint) => Promise<void>;
  /** Adds the emoji to the user's reactions to a message in the current conversation, or removes it if there. */
  react: (seq: bigint, emoji: string) => Promise<void>;
  markReadIfVisible: () => void;
  clearError: () => void;
  /** Shares the message history with the new device, or not; either way it is no longer new. */
  decideSharing: (share: boolean) => Promise<void>;
  dismissSecurityNotice: () => void;
  /** Looks up the users' devices, for their fingerprints. */
  lookUpDevices: (userIds: Uint8Array[]) => Promise<void>;
  /** The safety number of the user and another, from their current devices. */
  safetyNumber: (userId: Uint8Array) => Promise<string>;
  /** Marks a contact verified with their current fingerprint. */
  markVerified: (userId: Uint8Array) => Promise<void>;
}

/** The state with no user's data in it. */
const blank = {
  ...emptyData(),
  me: null,
  current: null,
  complete: {},
  settings: false,
  contactsShown: false,
  contacts: null,
  pending: {},
  devices: null,
  fingerprints: {},
  verified: {},
  newDevice: null,
  securityNotice: null,
  palette: null,
} satisfies Partial<Session>;

export const useSession = create<Session>((set, get) => {
  const link = new Link();
  const resets: (() => void)[] = [];
  const api: Api = {
    set,
    get,
    link,
    fail: (err) => set({ error: err instanceof Error ? err.message : String(err) }),
    signedOut(notice) {
      const credentials = get().credentials;
      link.reset();
      // A signed-out device's keys are of no further use.
      if (credentials) deleteStore(storeName(credentials.userId, credentials.deviceId));
      for (const reset of resets) reset();
      saved.forget();
      set({ ...blank, credentials: null, status: "offline", error: null, notice });
    },
    onSignOut: (fn) => void resets.push(fn),
    current() {
      const id = get().current;
      const c = id ? get().conversations[id] : undefined;
      if (!c || !link.conn) throw new Error("not connected");
      return c;
    },
  };

  const groups = groupActions(api);
  const contacts = contactActions(api);
  const keys = keyActions(api, groups.membersOf);
  const open = opening(api);
  const sharing = sharingActions(api);
  const conversations = conversationActions(api, groups, open);
  const send = sendActions(api);
  const stickers = stickerActions(api, send);
  const attachments = attachmentActions(api, send);
  const events = sync(api, conversations, groups, open, sharing, contacts);
  const connect = connectActions(api, keys, conversations, groups, contacts, stickers, events);

  // Back on screen: reconnect now instead of waiting out the backoff.
  if (typeof document !== "undefined") {
    document.addEventListener("visibilitychange", () => {
      if (isVisible()) {
        link.conn?.wake();
        get().markReadIfVisible();
      }
    });
  }

  return {
    ...blank,
    credentials: saved.credentials(),
    status: "offline",
    error: null,
    notice: null,
    ...connect.actions,
    ...accountActions(api).actions,
    ...deviceActions(api).actions,
    ...conversations.actions,
    ...notesActions(api, open).actions,
    ...groups.actions,
    ...contacts.actions,
    ...send.actions,
    ...attachments.actions,
    ...stickers.actions,
    ...reactionActions(api).actions,
    ...keys.actions,
    ...sharing.actions,
  };
});
