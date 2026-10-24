// The contacts feature's request handlers.
import { create } from "@bufbuild/protobuf";
import {
  AddContactResponseSchema,
  BlockUserResponseSchema,
  ListContactsResponseSchema,
  RemoveContactResponseSchema,
  UnblockUserResponseSchema,
} from "../gen/wrinkle/v1/messaging_pb.js";
import type { FeatureFactory } from "../services/features.ts";
import { addContact, blockUser, listContacts, removeContact, unblockUser } from "./contacts.ts";

export const contacts: FeatureFactory = ({ users: db }) => ({
  handlers: {
    listContacts: async (c) => ({
      case: "listContacts",
      value: create(ListContactsResponseSchema, { contacts: await listContacts(db, c.userId) }),
    }),
    addContact: async (c, req) => ({
      case: "addContact",
      value: create(AddContactResponseSchema, { contact: await addContact(db, c.userId, req) }),
    }),
    removeContact: async (c, req) => {
      await removeContact(db, c.userId, req.userId);
      return { case: "removeContact", value: create(RemoveContactResponseSchema) };
    },
    blockUser: async (c, req) => {
      await blockUser(db, c.userId, req.userId);
      return { case: "blockUser", value: create(BlockUserResponseSchema) };
    },
    unblockUser: async (c, req) => {
      await unblockUser(db, c.userId, req.userId);
      return { case: "unblockUser", value: create(UnblockUserResponseSchema) };
    },
  },
});
