// The conversation feature's request handlers, with the requests that start
// in the user keyspace: opening conversations, creating groups and adding
// members; and the mover of old history to the cold tier.
import { create } from "@bufbuild/protobuf";
import {
  AddMembersResponseSchema,
  CreateGroupResponseSchema,
  DeleteMessageResponseSchema,
  GetMembersResponseSchema,
  GetMessagesResponseSchema,
  LeaveGroupResponseSchema,
  MarkReadResponseSchema,
  OpenConversationResponseSchema,
  OpenNotesResponseSchema,
  PublishKeysResponseSchema,
  RemoveMemberResponseSchema,
  SendMessageResponseSchema,
  SetAdminResponseSchema,
  SetReactionResponseSchema,
  SetTopicResponseSchema,
} from "../gen/wrinkle/v1/messaging_pb.js";
import type { LeaveGroups } from "../accounts/feature.ts";
import { insertGroupEntry, openDirect, openNotes as openNotesEntry } from "../devicesync/conversations.ts";
import { checkContacts, leaveGroups, resolveMembers } from "../devicesync/groups.ts";
import { invalidArgument } from "../internal/apierror.ts";
import { parseUUID, type UUID } from "../internal/ids.ts";
import type { FeatureFactory } from "../services/features.ts";
import { deleteMessage } from "./delete.ts";
import {
  addMembers,
  checkClientGroupId,
  checkTopic,
  createGroup,
  groupExists,
  leave,
  members,
  removeMember,
  setAdmin,
  setTopic,
} from "./group.ts";
import { conversationKeys, publishKeys } from "./keys.ts";
import { startMover, type MoveOptions } from "./mover.ts";
import { setReaction } from "./reaction.ts";
import { markRead, messages, newConversations, open, openNotes, send } from "./service.ts";

function conversationIdOf(b: Uint8Array): UUID {
  const id = parseUUID(b);
  if (!id) throw invalidArgument("conversation_id must be 16 bytes");
  return id;
}

function userIdOf(b: Uint8Array): UUID {
  const id = parseUUID(b);
  if (!id) throw invalidArgument("user_id must be 16 bytes");
  return id;
}

/** DeleteAccount's leaving of every group, for the accounts feature. */
export const leaveAllGroups: LeaveGroups = (deps, userId) =>
  leaveGroups(deps.users, userId, (u, c) => leave(deps, u, c));

/** History moves to the cold tier only with the options. */
export const conversationFeature =
  (moving?: MoveOptions): FeatureFactory =>
  (deps) => ({
    ...handlersOf(deps),
    start:
      moving &&
      (() => startMover(deps.conversations, deps.cold, deps.leases, deps.log.with({ role: "mover" }), moving)),
  });

const handlersOf: FeatureFactory = (deps) => {
  const cs = newConversations(deps);
  const { users, conversations: db, cold } = deps;
  return {
    handlers: {
      openConversation: async (c, req) => ({
        case: "openConversation",
        value: create(OpenConversationResponseSchema, {
          conversation: await openDirect(users, c.userId, req.peerName, (a, b) => open(db, a, b)),
        }),
      }),
      openNotes: async (c) => ({
        case: "openNotes",
        value: create(OpenNotesResponseSchema, {
          conversation: await openNotesEntry(users, c.userId, () => openNotes(db, c.userId)),
        }),
      }),
      // The user's list has a new group at once; the members' lists get it from the fan-out.
      createGroup: async (c, req) => {
        checkTopic(req.topic);
        checkClientGroupId(req.clientGroupId);
        const invitees = await resolveMembers(users, c.userId, req.memberNames);
        // Only a new group is checked: a retry returns the group as created.
        if (!(await groupExists(db, c.userId, req.clientGroupId)))
          await checkContacts(users, c.userId, invitees.names)(invitees.users);
        const conv = await createGroup(deps, c.userId, req.topic, req.clientGroupId, invitees.users);
        await insertGroupEntry(users, c.userId, Buffer.from(conv.conversationId), conv.topic, conv.memberCount);
        return { case: "createGroup", value: create(CreateGroupResponseSchema, { conversation: conv }) };
      },
      addMembers: async (c, req) => {
        const conversationId = conversationIdOf(req.conversationId);
        const invitees = await resolveMembers(users, c.userId, req.memberNames);
        await addMembers(
          deps,
          c.userId,
          conversationId,
          invitees.users,
          checkContacts(users, c.userId, invitees.names),
        );
        return { case: "addMembers", value: create(AddMembersResponseSchema) };
      },
      removeMember: async (c, req) => {
        const conversationId = conversationIdOf(req.conversationId);
        await removeMember(deps, c.userId, conversationId, userIdOf(req.userId));
        return { case: "removeMember", value: create(RemoveMemberResponseSchema) };
      },
      leaveGroup: async (c, req) => {
        await leave(deps, c.userId, conversationIdOf(req.conversationId));
        return { case: "leaveGroup", value: create(LeaveGroupResponseSchema) };
      },
      setTopic: async (c, req) => {
        await setTopic(deps, c.userId, conversationIdOf(req.conversationId), req.topic);
        return { case: "setTopic", value: create(SetTopicResponseSchema) };
      },
      setAdmin: async (c, req) => {
        const conversationId = conversationIdOf(req.conversationId);
        await setAdmin(deps, c.userId, conversationId, userIdOf(req.userId), req.admin);
        return { case: "setAdmin", value: create(SetAdminResponseSchema) };
      },
      getMembers: async (c, req) => ({
        case: "getMembers",
        value: create(GetMembersResponseSchema, {
          members: await members(deps, c.userId, conversationIdOf(req.conversationId)),
        }),
      }),
      sendMessage: async (c, req) => {
        const conversationId = conversationIdOf(req.conversationId);
        const message = await send(cs, c.userId, conversationId, req.clientMessageId, req.sealed, req.blobIds);
        return { case: "sendMessage", value: create(SendMessageResponseSchema, { message }) };
      },
      deleteMessage: async (c, req) => {
        await deleteMessage(deps, c.userId, conversationIdOf(req.conversationId), req.seq);
        return { case: "deleteMessage", value: create(DeleteMessageResponseSchema) };
      },
      setReaction: async (c, req) => {
        await setReaction(cs, c.userId, conversationIdOf(req.conversationId), req.seq, req.sealed);
        return { case: "setReaction", value: create(SetReactionResponseSchema) };
      },
      markRead: async (c, req) => {
        await markRead(cs, c.userId, conversationIdOf(req.conversationId), req.seq);
        return { case: "markRead", value: create(MarkReadResponseSchema) };
      },
      getMessages: async (c, req) => ({
        case: "getMessages",
        value: create(
          GetMessagesResponseSchema,
          await messages(db, cold, c.userId, conversationIdOf(req.conversationId), req.beforeSeq, req.limit),
        ),
      }),
      publishKeys: async (c, req) => {
        const conversationId = conversationIdOf(req.conversationId);
        await publishKeys(cs, c.userId, c.deviceId, conversationId, req.epoch, req.keys);
        return { case: "publishKeys", value: create(PublishKeysResponseSchema) };
      },
      getConversationKeys: async (c, req) => ({
        case: "getConversationKeys",
        value: await conversationKeys(
          deps,
          c.userId,
          c.deviceId,
          conversationIdOf(req.conversationId),
          req.afterEpoch,
          req.limit,
        ),
      }),
    },
  };
};
