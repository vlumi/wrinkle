// The accounts feature's request handlers.
import { create } from "@bufbuild/protobuf";
import {
  ChangePasswordResponseSchema,
  DeleteAccountResponseSchema,
  GetDeviceKeysResponseSchema,
  ListDevicesResponseSchema,
  RegisterResponseSchema,
  RenameDeviceResponseSchema,
  SetDeviceKeyResponseSchema,
  SignInResponseSchema,
  SignOutDeviceResponseSchema,
  SignOutResponseSchema,
  UpdateProfileResponseSchema,
} from "../gen/wrinkle/v1/messaging_pb.js";
import type { UUID } from "../internal/ids.ts";
import type { Deps } from "../services/deps.ts";
import type { FeatureFactory } from "../services/features.ts";
import { deleteAccount } from "./delete.ts";
import { listDevices, renameDevice, signOutOther, signOutSelf } from "./devices.ts";
import { deviceKeys, setDeviceKey } from "./keys.ts";
import { updateProfile } from "./profile.ts";
import type { AccountsDeps } from "./service.ts";
import { changePassword, connect } from "./session.ts";
import { register, signIn } from "./signin.ts";

/** Takes a user being deleted out of every group they are in. */
export type LeaveGroups = (deps: Deps, userId: UUID) => Promise<void>;

export interface AccountsOptions {
  /** Run by DeleteAccount once the password is checked; by default nothing. */
  leaveGroups?: LeaveGroups;
}

export function accountsFeature(opts: AccountsOptions = {}): FeatureFactory {
  const leaveGroups = opts.leaveGroups ?? (() => Promise.resolve());
  return (deps) => {
    const d: AccountsDeps = { db: deps.users, blobs: deps.blobs, log: deps.log };
    return {
      signIn: {
        register: async (req) => ({
          case: "register",
          value: create(RegisterResponseSchema, { credentials: await register(d, req) }),
        }),
        signIn: async (req) => ({
          case: "signIn",
          value: create(SignInResponseSchema, { credentials: await signIn(d, req) }),
        }),
        connect: async (req) => ({ case: "connect", value: await connect(d, req) }),
      },
      handlers: {
        changePassword: async (c, req) => {
          await changePassword(d, c.userId, c.deviceId, req);
          return { case: "changePassword", value: create(ChangePasswordResponseSchema) };
        },
        updateProfile: async (c, req) => ({
          case: "updateProfile",
          value: create(UpdateProfileResponseSchema, { user: await updateProfile(d, c.userId, req) }),
        }),
        signOut: async (c) => {
          await signOutSelf(d, c.userId, c.deviceId);
          return { case: "signOut", value: create(SignOutResponseSchema) };
        },
        listDevices: async (c) => ({
          case: "listDevices",
          value: create(ListDevicesResponseSchema, { devices: await listDevices(d, c.userId) }),
        }),
        renameDevice: async (c, req) => ({
          case: "renameDevice",
          value: create(RenameDeviceResponseSchema, { device: await renameDevice(d, c.userId, req) }),
        }),
        signOutDevice: async (c, req) => {
          await signOutOther(d, c.userId, c.deviceId, req.deviceId);
          return { case: "signOutDevice", value: create(SignOutDeviceResponseSchema) };
        },
        deleteAccount: async (c, req) => {
          await deleteAccount(d, c.userId, req, () => leaveGroups(deps, c.userId));
          return { case: "deleteAccount", value: create(DeleteAccountResponseSchema) };
        },
        setDeviceKey: async (c, req) => {
          await setDeviceKey(d, c.userId, c.deviceId, req.publicKey);
          return { case: "setDeviceKey", value: create(SetDeviceKeyResponseSchema) };
        },
        getDeviceKeys: async (_c, req) => ({
          case: "getDeviceKeys",
          value: create(GetDeviceKeysResponseSchema, { users: await deviceKeys(d.db, req.userIds) }),
        }),
      },
    };
  };
}

export const accounts: FeatureFactory = accountsFeature();
