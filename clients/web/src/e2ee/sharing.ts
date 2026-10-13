// Sharing the message history with a new device of the user: offered when
// one signs in with keys, one at a time, and decided on the banner.

import { deviceLabel } from "../devices/model";
import type { Device } from "../gen/wrinkle/v1/model_pb";
import type { Api } from "../session/api";
import type { NewDevice, Session } from "../session/session";

export function sharingActions({ set, get, link, fail, onSignOut }: Api) {
  /** New devices of the user after the one being asked about. */
  let queue: NewDevice[] = [];
  onSignOut(() => {
    queue = [];
  });

  function offerSharing(device: Device) {
    const d = { deviceId: device.deviceId, name: deviceLabel(device), publicKey: device.publicKey };
    if (get().newDevice?.deviceId === d.deviceId || queue.some((x) => x.deviceId === d.deviceId)) return;
    if (get().newDevice) queue.push(d);
    else set({ newDevice: d });
  }

  /** A device event: offers sharing when it is a new device of the user with keys. */
  function deviceChanged(owner: Uint8Array, device: Device, signedOut: boolean) {
    link.keysReady
      .then((ok) => {
        if (ok && link.keys?.deviceChanged(owner, device, signedOut)) offerSharing(device);
      })
      .catch(fail);
  }

  const actions = {
    async decideSharing(share) {
      const d = get().newDevice;
      const k = link.keys;
      if (!d || !k) return;
      let notice;
      if (share) {
        await k.share(d);
        notice = `Your message history was shared with ${d.name}.`;
      } else {
        notice = `${d.name} signed in. Your message history was not shared with it.`;
      }
      await k.markKnown(d.deviceId);
      set({ newDevice: queue.shift() ?? null, securityNotice: notice });
    },

    dismissSecurityNotice() {
      set({ securityNotice: null });
    },
  } satisfies Partial<Session>;

  return { actions, deviceChanged };
}
