import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Avatar } from "../account/Avatar";
import { displayName } from "../account/icons";
import type { Message } from "../gen/wrinkle/v1/model_pb";
import { GroupAvatar } from "../groups/GroupAvatar";
import { GroupInfo } from "../groups/GroupInfo";
import { isFormer, isGroup, noticeOf, noticeText, readBy } from "../groups/model";
import { hex, sameId } from "../ids";
import { useSession } from "../session/session";
import { FingerprintWarning } from "../e2ee/FingerprintWarning";
import { Verify } from "../e2ee/Verify";
import { Composer } from "./Composer";
import { MessageItem } from "./MessageItem";
import { inOrder, isDeleted } from "./model";
import { PendingItem } from "./PendingItem";

/** How close to the end still counts as scrolled to the bottom, in pixels. */
const BOTTOM_SLACK = 24;

export function ConversationView({ id }: { id: string }) {
  const conversation = useSession((s) => s.conversations[id]);
  const messageMap = useSession((s) => s.messages[id]);
  const pending = useSession((s) => s.pending[id]);
  const members = useSession((s) => s.members[id]);
  const users = useSession((s) => s.users);
  const complete = useSession((s) => s.complete[id]);
  const me = useSession((s) => s.me);
  const show = useSession((s) => s.show);
  const send = useSession((s) => s.send);
  const sendSticker = useSession((s) => s.sendSticker);
  const sendAttachments = useSession((s) => s.sendAttachments);
  const retrySend = useSession((s) => s.retrySend);
  const loadEarlier = useSession((s) => s.loadEarlier);
  const scroller = useRef<HTMLOListElement>(null);
  // Whether to follow new messages: not while the user reads further up.
  const atBottom = useRef(true);
  const [info, setInfo] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const messages = inOrder(messageMap);
  const count = messages.length + (pending?.length ?? 0);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && atBottom.current) el.scrollTop = el.scrollHeight;
  }, [count, info]);

  useEffect(() => {
    atBottom.current = true;
  }, [id]);

  if (!conversation) return null;
  const group = isGroup(conversation);
  if (group && info) return <GroupInfo id={id} onClose={() => setInfo(false)} />;
  if (!group && verifying && conversation.peer) {
    return <Verify user={conversation.peer} onClose={() => setVerifying(false)} />;
  }
  const oldest = messages[0];
  const peerName = displayName(conversation.peer);
  const lastMine = [...messages]
    .reverse()
    .find((m) => !noticeOf(m) && !isDeleted(m) && sameId(m.senderUserId, me?.userId));

  const former = isFormer(conversation);

  /** What the user's own message shows about who read it. */
  function receipt(m: Message): string {
    if (isDeleted(m)) return "";
    if (!group) return conversation.peerReadSeq >= m.seq ? "read" : "";
    if (m !== lastMine) return "";
    const n = readBy(members, me?.userId, m.seq);
    return n > 0 ? `Read by ${n}` : "";
  }

  return (
    <section
      className="conversation"
      aria-label={group ? `Group ${conversation.topic}` : `Conversation with ${peerName}`}
    >
      <header>
        <button className="back" onClick={() => show(null)} aria-label="Back to conversations">
          ‹
        </button>
        {group ? (
          <>
            <GroupAvatar />
            <span className="titles">
              <h2 data-testid="topic">{conversation.topic}</h2>
              <span className="subtitle" data-testid="member-count">
                {conversation.memberCount === 1 ? "1 member" : `${conversation.memberCount} members`}
              </span>
            </span>
            <button className="info secondary" data-testid="group-info" onClick={() => setInfo(true)}>
              Info
            </button>
          </>
        ) : (
          <>
            <Avatar user={conversation.peer} />
            <h2 data-testid="peer-name">{peerName}</h2>
            {!conversation.peer?.deleted && (
              <button className="info secondary" data-testid="verify" onClick={() => setVerifying(true)}>
                Verify
              </button>
            )}
          </>
        )}
      </header>
      <FingerprintWarning
        users={
          group
            ? (members ?? []).flatMap((m) => (m.user ? [m.user] : []))
            : conversation.peer
              ? [conversation.peer]
              : []
        }
        me={me?.userId}
        onVerify={group ? () => setInfo(true) : () => setVerifying(true)}
      />
      <ol
        ref={scroller}
        className="messages"
        data-testid="messages"
        onScroll={(e) => {
          const el = e.currentTarget;
          atBottom.current = el.scrollTop + el.clientHeight >= el.scrollHeight - BOTTOM_SLACK;
        }}
      >
        {oldest && oldest.seq > 1n && !complete && (
          <li className="earlier">
            <button
              onClick={() => {
                atBottom.current = false;
                void loadEarlier();
              }}
            >
              Load earlier
            </button>
          </li>
        )}
        {messages.map((m) => {
          const notice = noticeOf(m);
          if (notice) {
            return (
              <li key={m.seq.toString()} className="system" data-testid="notice">
                {noticeText(notice, m.senderUserId, me?.userId, users)}
              </li>
            );
          }
          const mine = sameId(m.senderUserId, me?.userId);
          return (
            <MessageItem
              key={m.seq.toString()}
              message={m}
              mine={mine}
              sender={group && !mine ? (users[hex(m.senderUserId)] ?? null) : undefined}
              receipt={mine ? receipt(m) : ""}
              canReact={!former}
            />
          );
        })}
        {pending?.map((p) => (
          <PendingItem
            key={p.clientMessageId}
            pending={p}
            onRetry={() => retrySend(p.clientMessageId).catch(() => {})}
          />
        ))}
      </ol>
      {former ? (
        <p className="hint closed" data-testid="former-member">
          You are no longer in this group
        </p>
      ) : !group && conversation.peer?.deleted ? (
        <p className="hint closed">This account was deleted.</p>
      ) : (
        <Composer
          onSend={(text) => {
            atBottom.current = true;
            send(text).catch(() => {});
          }}
          onSticker={(sticker) => {
            atBottom.current = true;
            sendSticker(sticker).catch(() => {});
          }}
          onAttach={(files, caption) => {
            atBottom.current = true;
            sendAttachments(files, caption).catch(() => {});
          }}
        />
      )}
    </section>
  );
}
