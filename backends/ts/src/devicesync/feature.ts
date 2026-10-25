// The devicesync feature's request handlers: the event log, the
// conversation list, acks and push registration; and the trimmer of the logs.
import { create } from "@bufbuild/protobuf";
import { ListConversationsResponseSchema, RegisterPushTokenResponseSchema } from "../gen/wrinkle/v1/messaging_pb.js";
import type { FeatureFactory } from "../services/features.ts";
import { listConversations } from "./conversations.ts";
import { ack, registerPushToken } from "./device.ts";
import { sync } from "./sync.ts";
import { startTrimmer, type TrimOptions } from "./trim.ts";

/** The logs are trimmed only with the options. */
export function devicesyncFeature(trimming?: TrimOptions): FeatureFactory {
  return ({ users: db, log }) => ({
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
    start: trimming && (() => startTrimmer(db, log.with({ role: "trimmer" }), trimming)),
  });
}
