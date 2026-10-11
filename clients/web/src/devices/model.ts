// The account's signed-in devices as the settings show them, and how device
// events change the list.

import type { Device } from "../gen/wrinkle/v1/model_pb";

const compare = (a: bigint, b: bigint): number => (a < b ? -1 : a > b ? 1 : 0);
const created = (d: Device): bigint => d.createdAt?.seconds ?? 0n;

/** Oldest first, as ListDevices returns them. */
export function sortDevices(devices: Device[]): Device[] {
  return [...devices].sort((a, b) => compare(created(a), created(b)) || compare(a.deviceId, b.deviceId));
}

/** The list with a device added or replaced, or dropped when signed out. */
export function withDevice(devices: Device[], d: Device, signedOut: boolean): Device[] {
  const others = devices.filter((x) => x.deviceId !== d.deviceId);
  return signedOut ? others : sortDevices([...others, d]);
}

/** A device as named on screen. */
export function deviceLabel(d: Device): string {
  return d.name || `Device ${d.deviceId.toString()}`;
}

/** What is wrong with a device name, or null: UTF-8 of at most 64 bytes. */
export function deviceNameProblem(name: string): string | null {
  return new TextEncoder().encode(name).length > 64 ? "Device names are at most 64 bytes." : null;
}
