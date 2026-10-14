import { useState } from "react";
import { displayName } from "../account/icons";
import { errorText } from "../connection/connection";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import { ContactState } from "../gen/wrinkle/v1/model_pb";
import type { User } from "../gen/wrinkle/v1/model_pb";
import { useSession } from "../session/session";
import { stateOf } from "./model";

/** A contact request's error as a sentence; by name, the name asked for. */
export function contactError(err: unknown, name?: string): string {
  return errorText(err, {
    [ErrorCode.NOT_FOUND]: name ? `No one is called ${name}.` : "No such user.",
    [ErrorCode.INVALID_ARGUMENT]: "That's you.",
    [ErrorCode.FAILED_PRECONDITION]: "Unblock this user first.",
    [ErrorCode.PERMISSION_DENIED]: "Can't add this user.",
  });
}

/** A button whose action waits for a confirmation in the page. */
export function ConfirmButton({
  label,
  question,
  testid,
  onConfirm,
}: {
  label: string;
  question: string;
  testid: string;
  onConfirm: () => Promise<unknown>;
}) {
  const [confirming, setConfirming] = useState(false);
  if (!confirming) {
    return (
      <button type="button" className="secondary" data-testid={testid} onClick={() => setConfirming(true)}>
        {label}
      </button>
    );
  }
  return (
    <span className="inline-confirm" role="alertdialog" aria-label={question}>
      <span>{question}</span>
      <button
        type="button"
        className="danger"
        data-testid={`confirm-${testid}`}
        onClick={() => void onConfirm().finally(() => setConfirming(false))}
      >
        {label}
      </button>
      <button type="button" className="secondary" onClick={() => setConfirming(false)}>
        Cancel
      </button>
    </span>
  );
}

/** What the user can do about another user, by how they stand. */
export function ContactActions({ user, onError }: { user: User; onError: (err: unknown) => void }) {
  const loaded = useSession((s) => s.contacts !== null);
  const state = useSession((s) => stateOf(s.contacts, user.userId));
  const addContact = useSession((s) => s.addContact);
  const removeContact = useSession((s) => s.removeContact);
  const blockUser = useSession((s) => s.blockUser);
  const unblockUser = useSession((s) => s.unblockUser);
  if (!loaded || user.deleted) return null;
  const name = displayName(user);
  const run = (work: Promise<unknown>) => work.catch(onError);
  const add = (label: string) => (
    <button
      type="button"
      className="secondary"
      data-testid="add-contact"
      onClick={() => void run(addContact({ userId: user.userId }))}
    >
      {label}
    </button>
  );
  const remove = (label: string, testid: string) => (
    <button
      type="button"
      className="secondary"
      data-testid={testid}
      onClick={() => void run(removeContact(user.userId))}
    >
      {label}
    </button>
  );
  const block = (
    <ConfirmButton
      label="Block"
      question={`Block ${name}?`}
      testid="block-user"
      onConfirm={() => run(blockUser(user.userId))}
    />
  );

  switch (state) {
    case ContactState.BLOCKED:
      return (
        <button
          type="button"
          className="secondary"
          data-testid="unblock-user"
          onClick={() => void run(unblockUser(user.userId))}
        >
          Unblock
        </button>
      );
    case ContactState.CONTACT:
      return (
        <>
          <ConfirmButton
            label="Remove contact"
            question={`Remove ${name} from your contacts?`}
            testid="remove-contact"
            onConfirm={() => run(removeContact(user.userId))}
          />
          {block}
        </>
      );
    case ContactState.INCOMING:
      return (
        <>
          {add("Accept")}
          {remove("Decline", "decline-contact")}
          {block}
        </>
      );
    case ContactState.OUTGOING:
      return (
        <>
          {remove("Withdraw request", "withdraw-contact")}
          {block}
        </>
      );
    default:
      return (
        <>
          {add("Add contact")}
          {block}
        </>
      );
  }
}
