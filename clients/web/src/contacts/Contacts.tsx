import { useState } from "react";
import type { ReactNode } from "react";
import { Avatar } from "../account/Avatar";
import { displayName } from "../account/icons";
import { nameProblem } from "../account/rules";
import { ContactState } from "../gen/wrinkle/v1/model_pb";
import type { Contact, User } from "../gen/wrinkle/v1/model_pb";
import { hex } from "../ids";
import { useSession } from "../session/session";
import { ConfirmButton, contactError } from "./ContactActions";
import { sections } from "./model";

/** The user's contacts, requests both ways and blocked users, and adding a contact by name. */
export function Contacts({ onClose }: { onClose: () => void }) {
  const contacts = useSession((s) => s.contacts);
  const [error, setError] = useState<string | null>(null);
  const report = (err: unknown) => setError(contactError(err));
  const { incoming, outgoing, contacts: mutual, blocked } = sections(contacts);

  return (
    <section className="settings contacts" aria-label="Contacts">
      <header>
        <button className="back" onClick={onClose} aria-label="Back to conversations" data-testid="close-contacts">
          ‹
        </button>
        <h2>Contacts</h2>
      </header>
      <div className="settings-body">
        <AddContact />
        {error && (
          <p className="error" role="alert" data-testid="contacts-error">
            {error}
          </p>
        )}
        {contacts === null ? (
          <p className="hint">Loading contacts…</p>
        ) : (
          <>
            {incoming.length + outgoing.length > 0 && (
              <div className="field">
                <span className="label">Requests</span>
                <ul className="devices" data-testid="requests">
                  {incoming.map((c) => (
                    <Row key={key(c)} contact={c} note="Wants to add you">
                      <IncomingActions user={c.user} onError={report} />
                    </Row>
                  ))}
                  {outgoing.map((c) => (
                    <Row key={key(c)} contact={c} note="Requested">
                      <OutgoingActions user={c.user} onError={report} />
                    </Row>
                  ))}
                </ul>
              </div>
            )}
            <div className="field">
              <span className="label">{mutual.length === 1 ? "1 contact" : `${mutual.length} contacts`}</span>
              {mutual.length === 0 ? (
                <p className="hint">
                  No contacts yet. Add someone by name above; they become a contact when they accept.
                </p>
              ) : (
                <ul className="devices" data-testid="contact-list">
                  {mutual.map((c) => (
                    <Row key={key(c)} contact={c} open onError={report}>
                      <ContactRowActions user={c.user} onError={report} />
                    </Row>
                  ))}
                </ul>
              )}
            </div>
            {blocked.length > 0 && (
              <div className="field">
                <span className="label">Blocked</span>
                <ul className="devices" data-testid="blocked-list">
                  {blocked.map((c) => (
                    <Row key={key(c)} contact={c}>
                      <UnblockButton user={c.user} onError={report} />
                    </Row>
                  ))}
                </ul>
              </div>
            )}
          </>
        )}
      </div>
    </section>
  );
}

const key = (c: Contact) => hex(c.user?.userId ?? new Uint8Array());

/** One user in a section; a contact's name opens the conversation with them. */
function Row({
  contact: c,
  note,
  open,
  onError,
  children,
}: {
  contact: Contact;
  note?: string;
  open?: boolean;
  onError?: (err: unknown) => void;
  children: ReactNode;
}) {
  const openConversation = useSession((s) => s.openConversation);
  const name = displayName(c.user);
  const info = (
    <>
      <Avatar user={c.user} />
      <span className="device-info">
        <span className="device-name" data-testid="contact-name">
          {name}
        </span>
        {note && <span className="device-seen">{note}</span>}
      </span>
    </>
  );
  return (
    <li data-testid="contact" data-state={ContactState[c.state]} data-name={c.user?.name}>
      {open && c.user ? (
        <button
          type="button"
          className="contact-open"
          data-testid="contact-open"
          aria-label={`Message ${name}`}
          onClick={() => void openConversation(c.user?.name ?? "").catch(onError)}
        >
          {info}
        </button>
      ) : (
        info
      )}
      <span className="device-actions">{children}</span>
    </li>
  );
}

type RowActions = { user: User | undefined; onError: (err: unknown) => void };

function IncomingActions({ user, onError }: RowActions) {
  const addContact = useSession((s) => s.addContact);
  const removeContact = useSession((s) => s.removeContact);
  if (!user) return null;
  return (
    <>
      <button data-testid="accept-contact" onClick={() => void addContact({ userId: user.userId }).catch(onError)}>
        Accept
      </button>
      <button
        className="secondary"
        data-testid="decline-contact"
        onClick={() => void removeContact(user.userId).catch(onError)}
      >
        Decline
      </button>
    </>
  );
}

function OutgoingActions({ user, onError }: RowActions) {
  const removeContact = useSession((s) => s.removeContact);
  if (!user) return null;
  return (
    <button
      className="secondary"
      data-testid="withdraw-contact"
      onClick={() => void removeContact(user.userId).catch(onError)}
    >
      Withdraw
    </button>
  );
}

function ContactRowActions({ user, onError }: RowActions) {
  const removeContact = useSession((s) => s.removeContact);
  const blockUser = useSession((s) => s.blockUser);
  if (!user) return null;
  const name = displayName(user);
  return (
    <>
      <ConfirmButton
        label="Remove"
        question={`Remove ${name}?`}
        testid="remove-contact"
        onConfirm={() => removeContact(user.userId).catch(onError)}
      />
      <ConfirmButton
        label="Block"
        question={`Block ${name}?`}
        testid="block-user"
        onConfirm={() => blockUser(user.userId).catch(onError)}
      />
    </>
  );
}

function UnblockButton({ user, onError }: RowActions) {
  const unblockUser = useSession((s) => s.unblockUser);
  if (!user) return null;
  return (
    <button
      className="secondary"
      data-testid="unblock-user"
      onClick={() => void unblockUser(user.userId).catch(onError)}
    >
      Unblock
    </button>
  );
}

/** A form's outcome: an error, or what was done. */
type Outcome = { error: string } | { done: string } | null;

function AddContact() {
  const addContact = useSession((s) => s.addContact);
  const [name, setName] = useState("");
  const [outcome, setOutcome] = useState<Outcome>(null);

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const n = name.trim();
        const problem = nameProblem(n);
        if (problem) {
          setOutcome({ error: problem });
          return;
        }
        addContact({ name: n })
          .then((state) => {
            setName("");
            setOutcome({
              done: state === ContactState.CONTACT ? `${n} is now a contact.` : `Asked ${n} to be a contact.`,
            });
          })
          .catch((err: unknown) =>
            setOutcome({
              error: contactError(err, n),
            }),
          );
      }}
    >
      <label>
        Add contact
        <span className="row-input">
          <input
            data-testid="add-contact-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Name"
            maxLength={64}
            autoCapitalize="none"
            autoComplete="off"
          />
          <button type="submit" disabled={!name.trim()}>
            Add
          </button>
        </span>
      </label>
      {outcome &&
        ("error" in outcome ? (
          <p className="error" role="alert" data-testid="add-contact-error">
            {outcome.error}
          </p>
        ) : (
          <p className="hint" data-testid="add-contact-done">
            {outcome.done}
          </p>
        ))}
    </form>
  );
}
