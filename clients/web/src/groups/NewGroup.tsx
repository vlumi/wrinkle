import { useRef, useState } from "react";
import { errorText } from "../connection/connection";
import { ContactPicker } from "../contacts/ContactPicker";
import { notContact } from "../contacts/model";
import { newMessageId } from "../conversations/pending";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import type { User } from "../gen/wrinkle/v1/model_pb";
import { useSession } from "../session/session";
import { namesProblem, topicProblem } from "./model";

/** Creates a group: a topic and, optionally, members picked from the contacts. */
export function NewGroup({ onDone }: { onDone: () => void }) {
  const createGroup = useSession((s) => s.createGroup);
  const [topic, setTopic] = useState("");
  const [picked, setPicked] = useState<User[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // One id per group being created, kept across retries so a lost response does not create it twice.
  const attempt = useRef<string | null>(null);

  return (
    <form
      className="new-group"
      onSubmit={(e) => {
        e.preventDefault();
        const t = topic.trim();
        const members = picked.map((u) => u.name);
        const problem = topicProblem(t) ?? namesProblem(members);
        if (problem) {
          setError(problem);
          return;
        }
        attempt.current ??= newMessageId();
        setBusy(true);
        createGroup(t, members, attempt.current)
          .then(() => {
            attempt.current = null;
            setTopic("");
            setPicked([]);
            setError(null);
            onDone();
          })
          .catch((err: unknown) => setError(groupError(err)))
          .finally(() => setBusy(false));
      }}
    >
      <input
        data-testid="group-topic"
        value={topic}
        onChange={(e) => setTopic(e.target.value)}
        placeholder="Topic"
        aria-label="Topic"
        maxLength={200}
      />
      <ContactPicker legend="Members (optional)" selected={picked} onChange={setPicked} />
      <div className="new-group-actions">
        <button type="submit" disabled={!topic.trim() || busy} data-testid="create-group">
          Create group
        </button>
        <button type="button" className="secondary" onClick={onDone}>
          Cancel
        </button>
      </div>
      {error && (
        <p className="error" role="alert" data-testid="group-error">
          {error}
        </p>
      )}
    </form>
  );
}

/** A refused group change as a sentence. */
function groupError(err: unknown): string {
  const name = notContact(err);
  if (name) return `${name} isn't in your contacts.`;
  return errorText(err, {
    [ErrorCode.NOT_FOUND]: "Someone in the list does not exist.",
    [ErrorCode.FAILED_PRECONDITION]: "Too many members.",
  });
}
