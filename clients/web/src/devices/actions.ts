// The account's devices in the session: listed in the settings, renamed and
// signed out from there.

import type { Api } from "../session/api";
import type { Session } from "../session/session";
import { sortDevices, withDevice } from "./model";

export function deviceActions({ set, get, link }: Api) {
  const actions = {
    async loadDevices() {
      if (!link.conn) return;
      const resp = await link.conn.call("listDevices", { case: "listDevices", value: {} });
      set({ devices: sortDevices(resp.devices) });
    },

    async renameDevice(deviceId, name) {
      const resp = await link.call("renameDevice", { case: "renameDevice", value: { deviceId, name } });
      const devices = get().devices;
      if (resp.device && devices) set({ devices: withDevice(devices, resp.device, false) });
    },

    async signOutDevice(deviceId) {
      await link.call("signOutDevice", { case: "signOutDevice", value: { deviceId } });
      const devices = get().devices;
      if (devices) set({ devices: devices.filter((d) => d.deviceId !== deviceId) });
    },
  } satisfies Partial<Session>;

  return { actions };
}
