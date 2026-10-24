// The devicesync feature's request handlers: the event log, the
// conversation list, acks and push registration.
import { create } from "@bufbuild/protobuf";
import { ListConversationsResponseSchema, RegisterPushTokenResponseSchema } from "../gen/wrinkle/v1/messaging_pb.js";
import type { FeatureFactory } from "../services/features.ts";
import { listConversations } from "./conversations.ts";
import { ack, registerPushToken } from "./device.ts";
import { sync } from "./sync.ts";

export const devicesync: FeatureFactory = ({ users: db }) => ({
  ack: (userId, deviceId, cursor) => ack(db, userId, deviceId, cursor),
  handlers: {
    sync: async (c, req) => ({ case: "sync", value: await sync(db, c.userId, req.cursor, req.limit) }),
    listConversations: async (c) => ({
      case: "listConversations",
      value: create(ListConversationsResponseSchema, { conversations: await listConversations(db, c.userId) }),
    }),
    registerPushToken: async (c, req) => {
      await registerPushToken(db, c.userId, c.deviceId, req.service, req.token);
      return { case: "registerPushToken", value: create(RegisterPushTokenResponseSchema) };
    },
  },
});
