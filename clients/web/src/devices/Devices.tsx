import { useEffect, useState } from "react";
import { errorText } from "../connection/connection";
import type { Device } from "../gen/wrinkle/v1/model_pb";
import { useSession } from "../session/session";
import { deviceLabel, deviceNameProblem } from "./model";

/** The account's signed-in devices: rename any, sign out the others. */
export function Devices() {
  const devices = useSession((s) => s.devices);
  const status = useSession((s) => s.status);
  const thisDevice = useSession((s) => s.credentials?.deviceId);
  const loadDevices = useSession((s) => s.loadDevices);
  const [error, setError] = useState<string | null>(null);

  // Listed again on every connect; device events keep the list current in between.
  useEffect(() => {
    if (status === "connected") loadDevices().catch((err: unknown) => setError(errorText(err)));
  }, [status, loadDevices]);

  return (
    <div className="field">
      <span className="label">Devices</span>
      {devices && (
        <ul className="devices" data-testid="devices">
          {devices.map((d) => (
            <DeviceItem
              key={d.deviceId.toString()}
              device={d}
              current={d.deviceId.toString() === thisDevice}
              onError={setError}
            />
          ))}
        </ul>
      )}
      {error && <p className="error">{error}</p>}
    </div>
  );
}

function DeviceItem({
  device: d,
  current,
  onError,
}: {
  device: Device;
  current: boolean;
  onError: (e: string) => void;
}) {
  const renameDevice = useSession((s) => s.renameDevice);
  const signOutDevice = useSession((s) => s.signOutDevice);
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState("");

  return (
    <li data-testid="device">
      <span className="device-info">
        <span className="device-name" data-testid="device-name" title={d.name}>
          {deviceLabel(d)}
        </span>
        <span className="device-seen">
          {current ? (
            <span className="tag" data-testid="this-device">
              This device
            </span>
          ) : (
            seen(d)
          )}
        </span>
      </span>
      {!editing && (
        <span className="device-actions">
          <button
            type="button"
            className="secondary"
            data-testid="rename-device"
            onClick={() => {
              setName(d.name);
              setEditing(true);
            }}
          >
            Rename
          </button>
          {!current && (
            <button
              type="button"
              className="signout"
              data-testid="signout-device"
              onClick={() => signOutDevice(d.deviceId).catch((err: unknown) => onError(errorText(err)))}
            >
              Sign out
            </button>
          )}
        </span>
      )}
      {editing && (
        <form
          className="row-input"
          onSubmit={(e) => {
            e.preventDefault();
            const problem = deviceNameProblem(name.trim());
            if (problem) {
              onError(problem);
              return;
            }
            renameDevice(d.deviceId, name.trim())
              .then(() => setEditing(false))
              .catch((err: unknown) => onError(errorText(err)));
          }}
        >
          <input
            data-testid="device-new-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            aria-label="Device name"
            maxLength={64}
          />
          <button type="submit">Save</button>
          <button type="button" className="secondary" onClick={() => setEditing(false)}>
            Cancel
          </button>
        </form>
      )}
    </li>
  );
}

function seen(d: Device): string {
  const s = (d.lastSeenAt ?? d.createdAt)?.seconds;
  if (s === undefined) return "";
  return "Last seen " + new Date(Number(s) * 1000).toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}
