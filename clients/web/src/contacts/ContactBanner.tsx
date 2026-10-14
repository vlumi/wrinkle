import { useState } from "react";
import type { ReactNode } from "react";
import { displayName } from "../account/icons";
import { ContactState } from "../gen/wrinkle/v1/model_pb";
import type { User } from "../gen/wrinkle/v1/model_pb";
import { useSession } from "../session/session";
import { ConfirmButton, contactError } from "./ContactActions";
import { stateOf } from "./model";

/** Above a direct conversation with someone not a contact: how they stand, and what to do about it. */
export function ContactBanner({ peer }: { peer: User }) {
  const loaded = useSession((s) => s.contacts !== null);
  const state = useSession((s) => stateOf(s.contacts, peer.userId));
  const addContact = useSession((s) => s.addContact);
  const removeContact = useSession((s) => s.removeContact);
  const blockUser = useSession((s) => s.blockUser);
  const [error, setError] = useState<string | null>(null);
  if (!loaded || peer.deleted || state === ContactState.CONTACT || state === ContactState.BLOCKED) return null;
  const name = displayName(peer);
  const run = (work: Promise<unknown>) =>
    work.then(
      () => setError(null),
      (err: unknown) => setError(contactError(err)),
    );
  const block = (
    <ConfirmButton
      label="Block"
      question={`Block ${name}?`}
      testid="banner-block"
      onConfirm={() => run(blockUser(peer.userId))}
    />
  );

  let text: string;
  let actions: ReactNode;
  if (state === ContactState.OUTGOING) {
    text = "Contact request sent";
    actions = (
      <button className="secondary" data-testid="banner-withdraw" onClick={() => void run(removeContact(peer.userId))}>
        Withdraw
      </button>
    );
  } else if (state === ContactState.INCOMING) {
    text = `${name} wants to add you as a contact`;
    actions = (
      <>
        <button data-testid="banner-accept" onClick={() => void run(addContact({ userId: peer.userId }))}>
          Accept
        </button>
        <button className="secondary" data-testid="banner-decline" onClick={() => void run(removeContact(peer.userId))}>
          Decline
        </button>
        {block}
      </>
    );
  } else {
    text = `${name} isn't in your contacts`;
    actions = (
      <>
        <button data-testid="banner-add" onClick={() => void run(addContact({ userId: peer.userId }))}>
          Add contact
        </button>
        {block}
      </>
    );
  }
  return (
    <div className="banner contact" role="status" data-testid="contact-banner" data-state={ContactState[state]}>
      <span data-testid="contact-banner-text">{text}</span>
      <span className="banner-actions">{actions}</span>
      {error && (
        <span className="contact-error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}

/** In place of the composer in a direct conversation with a user the user blocked. */
export function BlockedNotice({ peer }: { peer: User }) {
  const unblockUser = useSession((s) => s.unblockUser);
  const [error, setError] = useState<string | null>(null);
  return (
    <div className="hint closed blocked" data-testid="blocked">
      <span data-testid="blocked-text">You blocked {displayName(peer)}</span>
      <button
        className="secondary"
        data-testid="unblock"
        onClick={() => void unblockUser(peer.userId).catch((err: unknown) => setError(contactError(err)))}
      >
        Unblock
      </button>
      {error && (
        <span className="contact-error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}
