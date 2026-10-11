import { useEffect, useState } from "react";
import { Avatar } from "../account/Avatar";
import { displayName } from "../account/icons";
import { errorText } from "../connection/connection";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import { Verify } from "../e2ee/Verify";
import type { Member, User } from "../gen/wrinkle/v1/model_pb";
import { hex, sameId } from "../ids";
import { useSession } from "../session/session";
import { GroupAvatar } from "./GroupAvatar";
import { namesProblem, parseNames, topicProblem } from "./model";

/** A group's topic and members, and the changes the user may make to them. */
export function GroupInfo({ id, onClose }: { id: string; onClose(): void }) {
  const conversation = useSession((s) => s.conversations[id]);
  const members = useSession((s) => s.members[id]);
  const me = useSession((s) => s.me);
  const loadMembers = useSession((s) => s.loadMembers);
  const [error, setError] = useState<string | null>(null);
  const [verifying, setVerifying] = useState<User | null>(null);
  const report = (err: unknown) =>
    setError(
      errorText(err, {
        [ErrorCode.PERMISSION_DENIED]: "Only admins can do that.",
        [ErrorCode.NOT_FOUND]: "No one by that name is in the group or exists.",
        [ErrorCode.FAILED_PRECONDITION]: "That would leave the group without an admin, or with too many members.",
      }),
    );

  useEffect(() => {
    loadMembers(id).catch(report);
    // Listed again on opening; notices keep it current from there.
  }, [id, loadMembers]);

  if (!conversation) return null;
  if (verifying) return <Verify user={verifying} onClose={() => setVerifying(null)} />;
  const mine = members?.find((m) => sameId(m.user?.userId, me?.userId));
  const admin = mine ? mine.admin : conversation.admin;

  return (
    <section className="settings group-info" aria-label="Group info">
      <header>
        <GroupAvatar />
        <h2 data-testid="info-topic">{conversation.topic}</h2>
        <button className="close-info secondary" data-testid="close-info" onClick={onClose}>
          Done
        </button>
      </header>
      <div className="settings-body">
        {error && (
          <p className="error" role="alert" data-testid="group-info-error">
            {error}
          </p>
        )}
        {!conversation.member ? (
          <p className="hint" data-testid="info-former">
            You are no longer in this group
          </p>
        ) : (
          <>
            <TopicForm onError={report} onDone={() => setError(null)} />
            <div className="field">
              <span className="label">
                {conversation.memberCount === 1 ? "1 member" : `${conversation.memberCount} members`}
              </span>
              <ul className="devices members" data-testid="members">
                {(members ?? []).map((m) => (
                  <MemberItem
                    key={hex(m.user?.userId ?? new Uint8Array())}
                    member={m}
                    self={sameId(m.user?.userId, me?.userId)}
                    canManage={admin}
                    onError={report}
                    onVerify={() => m.user && setVerifying(m.user)}
                  />
                ))}
              </ul>
            </div>
            <AddMembers onError={report} onDone={() => setError(null)} />
            <Leave onError={report} onLeft={onClose} />
          </>
        )}
      </div>
    </section>
  );
}

function TopicForm({ onError, onDone }: { onError(err: unknown): void; onDone(): void }) {
  const setTopic = useSession((s) => s.setTopic);
  const [topic, setText] = useState("");

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const t = topic.trim();
        const problem = topicProblem(t);
        if (problem) return onError(new Error(problem));
        setTopic(t)
          .then(() => {
            setText("");
            onDone();
          })
          .catch(onError);
      }}
    >
      <label>
        Topic
        <span className="row-input">
          <input
            data-testid="new-topic"
            value={topic}
            onChange={(e) => setText(e.target.value)}
            placeholder="New topic"
            maxLength={200}
          />
          <button type="submit" disabled={!topic.trim()}>
            Change
          </button>
        </span>
      </label>
    </form>
  );
}

function MemberItem({
  member: m,
  self,
  canManage,
  onError,
  onVerify,
}: {
  member: Member;
  self: boolean;
  canManage: boolean;
  onError(err: unknown): void;
  onVerify(): void;
}) {
  const setAdmin = useSession((s) => s.setAdmin);
  const removeMember = useSession((s) => s.removeMember);
  const userId = m.user?.userId ?? new Uint8Array();

  return (
    <li data-testid="member">
      <Avatar user={m.user} />
      <span className="device-info">
        <span className="device-name" data-testid="member-name">
          {displayName(m.user)}
        </span>
        <span className="device-seen">
          {m.admin && (
            <span className="tag" data-testid="admin-tag">
              Admin
            </span>
          )}
          {self && <span>{m.admin ? " · you" : "You"}</span>}
        </span>
      </span>
      {!self && (
        <span className="device-actions">
          <button type="button" className="secondary" data-testid="verify-member" onClick={onVerify}>
            Verify
          </button>
          {canManage && (
            <>
              <button
                type="button"
                className="secondary"
                data-testid="toggle-admin"
                onClick={() => setAdmin(userId, !m.admin).catch(onError)}
              >
                {m.admin ? "Remove admin" : "Make admin"}
              </button>
              <button
                type="button"
                className="signout"
                data-testid="remove-member"
                onClick={() => removeMember(userId).catch(onError)}
              >
                Remove
              </button>
            </>
          )}
        </span>
      )}
    </li>
  );
}

function AddMembers({ onError, onDone }: { onError(err: unknown): void; onDone(): void }) {
  const addMembers = useSession((s) => s.addMembers);
  const [names, setNames] = useState("");

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const list = parseNames(names);
        const problem = namesProblem(list);
        if (problem) return onError(new Error(problem));
        addMembers(list)
          .then(() => {
            setNames("");
            onDone();
          })
          .catch(onError);
      }}
    >
      <label>
        Add members
        <span className="row-input">
          <input
            data-testid="add-members"
            value={names}
            onChange={(e) => setNames(e.target.value)}
            placeholder="Names, separated by commas"
            autoCapitalize="none"
          />
          <button type="submit" disabled={parseNames(names).length === 0}>
            Add
          </button>
        </span>
      </label>
    </form>
  );
}

/** Leaving, behind an in-page confirmation. */
function Leave({ onError, onLeft }: { onError(err: unknown): void; onLeft(): void }) {
  const leaveGroup = useSession((s) => s.leaveGroup);
  const [confirming, setConfirming] = useState(false);

  if (!confirming) {
    return (
      <button className="delete-account" data-testid="leave-group" onClick={() => setConfirming(true)}>
        Leave group
      </button>
    );
  }
  return (
    <div className="field confirm" role="alertdialog" aria-label="Leave the group?">
      <span className="label">Leave the group? You keep its history but get no new messages.</span>
      <span className="picture-actions">
        <button
          className="danger"
          data-testid="confirm-leave"
          onClick={() =>
            leaveGroup()
              .then(onLeft)
              .catch((err) => {
                setConfirming(false);
                onError(err);
              })
          }
        >
          Leave
        </button>
        <button className="secondary" data-testid="cancel-leave" onClick={() => setConfirming(false)}>
          Cancel
        </button>
      </span>
    </div>
  );
}
