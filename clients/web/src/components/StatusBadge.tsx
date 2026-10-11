import type { ConnectionState } from "../lib/connection";

const labels: Record<ConnectionState, string> = { connected: "online", connecting: "connecting", offline: "offline" };

export function StatusBadge({ status }: { status: ConnectionState }) {
  return (
    <span className="status" data-state={status} data-testid="status">
      <span className="dot" aria-hidden="true" />
      {labels[status]}
    </span>
  );
}
