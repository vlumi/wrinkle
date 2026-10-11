import { create } from "@bufbuild/protobuf";
import { DeviceSchema } from "../gen/wrinkle/v1/model_pb";
import { deviceLabel, deviceNameProblem, withDevice } from "./model";

const device = (id: bigint, created: bigint, name = "") =>
  create(DeviceSchema, { deviceId: id, name, createdAt: { seconds: created } });
const ids = (list: { deviceId: bigint }[]) => list.map((d) => d.deviceId);

test("a device signing in joins the list in order of signing in", () => {
  const list = [device(1n, 100n), device(3n, 300n)];
  const next = withDevice(list, device(2n, 200n), false);
  expect(ids(next)).toEqual([1n, 2n, 3n]);
});

test("a renamed device is replaced in place", () => {
  const list = [device(1n, 100n, "old"), device(2n, 200n)];
  const next = withDevice(list, device(1n, 100n, "new"), false);
  expect(ids(next)).toEqual([1n, 2n]);
  expect(next[0].name).toBe("new");
});

test("a signed-out device leaves the list", () => {
  const list = [device(1n, 100n), device(2n, 200n)];
  const next = withDevice(list, device(2n, 200n), true);
  expect(ids(next)).toEqual([1n]);
});

test("an unnamed device is shown by its id", () => {
  expect(deviceLabel(device(7n, 0n))).toBe("Device 7");
  expect(deviceLabel(device(7n, 0n, "Phone"))).toBe("Phone");
});

test("device names are at most 64 bytes, empty allowed", () => {
  expect(deviceNameProblem("")).toBeNull();
  expect(deviceNameProblem("a".repeat(64))).toBeNull();
  expect(deviceNameProblem("ä".repeat(33))).not.toBeNull();
});
