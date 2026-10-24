// The fan-out: committed outbox rows from Kafka into the logs of everyone
// concerned, then pushed to their gateways.
import type { FanoutDeps } from "../services/deps.ts";

export interface Fanout {
  stop(): Promise<void>;
}

export function startFanout(_deps: FanoutDeps, _concurrency: number): Promise<Fanout> {
  return Promise.resolve({ stop: () => Promise.resolve() });
}
