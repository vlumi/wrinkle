// What the services and the fan-out share: the keyspaces, Valkey and the blob
// storage settings.
import type { Pool } from "mysql2/promise";
import type { Logger } from "../internal/log.ts";
import type { Presence } from "../presence/presence.ts";

export interface BlobConfig {
  url: string;
  keyId: string;
  secret: string;
  region: string;
}

export interface Deps {
  /** The user keyspace. */
  users: Pool;
  /** The conversation keyspace. */
  conversations: Pool;
  blobs: BlobConfig;
  log: Logger;
}

/** What the fan-out needs beyond the services' dependencies. */
export interface FanoutDeps extends Deps {
  presence: Presence;
}
