// The account in the session: the profile, the password, and deleting it.

import { upload } from "../blobs/upload";
import { BlobPurpose } from "../gen/wrinkle/v1/messaging_pb";
import type { Api } from "../session/api";
import type { Session } from "../session/session";

export function accountActions({ set, get, link, signedOut }: Api) {
  const actions = {
    async updateProfile(change) {
      const resp = await link.call("updateProfile", { case: "updateProfile", value: change });
      if (resp.user) set({ me: resp.user });
    },

    async setPicture(picture) {
      const avatar = await upload(link.conn, BlobPurpose.AVATAR, picture);
      await get().updateProfile({ avatar });
    },

    async changePassword(current, next) {
      await link.call("changePassword", {
        case: "changePassword",
        value: { currentPassword: current, newPassword: next },
      });
    },

    async deleteAccount(password) {
      if (!link.conn) throw new Error("not connected");
      const notice = "Your account was deleted.";
      link.leaving = { notice };
      try {
        await link.conn.call("deleteAccount", { case: "deleteAccount", value: { password } });
      } catch (err) {
        // The server may close the connection before the response arrives.
        if (!get().credentials) return;
        link.leaving = null;
        throw err;
      }
      signedOut(notice);
    },
  } satisfies Partial<Session>;

  return { actions };
}
