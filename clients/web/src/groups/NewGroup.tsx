import { useRef, useState } from "react";
import { errorText } from "../connection/connection";
import { newMessageId } from "../conversations/pending";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import { useSession } from "../session/session";
import { namesProblem, parseNames, topicProblem } from "./model";

/** Creates a group: a topic and, optionally, members by name. */
export function NewGroup({ onDone }: { onDone: () => void }) {
  const createGroup = useSession((s) => s.createGroup);
  const [topic, setTopic] = useState("");
  const [names, setNames] = useState("");
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
        const members = parseNames(names);
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
            setNames("");
            setError(null);
            onDone();
          })
          .catch((err: unknown) =>
            setError(
              errorText(err, {
                [ErrorCode.NOT_FOUND]: "Someone in the list does not exist.",
                [ErrorCode.FAILED_PRECONDITION]: "Too many members.",
              }),
            ),
          )
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
      <input
        data-testid="group-members"
        value={names}
        onChange={(e) => setNames(e.target.value)}
        placeholder="Members, separated by commas (optional)"
        aria-label="Members"
        autoCapitalize="none"
      />
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
