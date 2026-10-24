// The TypeScript backend: any of the gateway, services and fan-out roles in
// one process. Settings come from flags or WRINKLE_* variables.
import { hostname } from "node:os";
import { parseArgs } from "node:util";
import type { Pool } from "mysql2/promise";
import { accounts } from "./accounts/feature.ts";
import { contacts } from "./contacts/feature.ts";
import { conversation } from "./conversation/feature.ts";
import { devicesync } from "./devicesync/feature.ts";
import { startFanout, type Fanout } from "./fanout/worker.ts";
import { Server } from "./gateway/server.ts";
import { Blobs } from "./internal/blobs.ts";
import { openKeyspace } from "./internal/db.ts";
import { serveHttp } from "./internal/http.ts";
import { log } from "./internal/log.ts";
import { Presence } from "./presence/presence.ts";
import type { Backend } from "./services/backend.ts";
import type { Deps } from "./services/deps.ts";
import { newLocal, type Feature } from "./services/features.ts";
import { Remote, serve } from "./services/rpc.ts";
import { stickers } from "./stickers/feature.ts";
import { uploads } from "./uploads/feature.ts";

const env = (key: string, fallback: string) => process.env[key] || fallback;

const { values: flags } = parseArgs({
  options: {
    roles: { type: "string", default: env("WRINKLE_ROLES", "gateway,fanout") },
    listen: { type: "string", default: env("WRINKLE_LISTEN", "127.0.0.1:8080") },
    mysql: { type: "string", default: env("WRINKLE_MYSQL", "127.0.0.1:15003") },
    kafka: { type: "string", default: env("WRINKLE_KAFKA", "localhost:19092") },
    valkey: { type: "string", default: env("WRINKLE_VALKEY", "localhost:6379") },
    services: { type: "string", default: env("WRINKLE_SERVICES", "") },
    "services-listen": { type: "string", default: env("WRINKLE_SERVICES_LISTEN", "127.0.0.1:9000") },
    node: { type: "string", default: env("WRINKLE_NODE", hostname()) },
    "db-conns": { type: "string", default: env("WRINKLE_DB_CONNS", "64") },
    "fanout-concurrency": { type: "string", default: env("WRINKLE_FANOUT_CONCURRENCY", "64") },
    "ack-interval-ms": { type: "string", default: env("WRINKLE_ACK_INTERVAL_MS", "60000") },
    "blob-url": { type: "string", default: env("WRINKLE_BLOB_URL", "http://127.0.0.1:9200") },
    "blob-key-id": { type: "string", default: env("WRINKLE_BLOB_KEY_ID", "GK7772696e6b6c6500000000") },
    "blob-secret": {
      type: "string",
      default: env("WRINKLE_BLOB_SECRET", "667c7e6aafc73808d30a89b14f18816b605d7d340823d9493b32b97aa7079fc4"),
    },
    "blob-region": { type: "string", default: env("WRINKLE_BLOB_REGION", "garage") },
  },
});

async function main(): Promise<void> {
  const roles = new Set(flags.roles.split(",").map((r) => r.trim()));
  for (const r of roles) if (!["gateway", "services", "fanout"].includes(r)) throw new Error(`unknown role ${r}`);
  const gateway = roles.has("gateway");
  const remote = gateway && flags.services !== "";
  const closers: (() => Promise<unknown>)[] = [];

  // The database is needed by the services (in-process or as a role) and by
  // the fan-out; a gateway forwarding to a services process does without.
  let deps: Deps | undefined;
  let features: Feature[] = [];
  let backend: Backend | undefined;
  if (roles.has("services") || roles.has("fanout") || (gateway && !remote)) {
    const conns = Number(flags["db-conns"]);
    const pools: Pool[] = await Promise.all([
      openKeyspace(flags.mysql, "user", conns),
      openKeyspace(flags.mysql, "conversation", conns),
    ]);
    closers.push(() => Promise.all(pools.map((p) => p.end())));
    deps = {
      users: pools[0]!,
      conversations: pools[1]!,
      blobs: new Blobs({
        url: flags["blob-url"],
        keyId: flags["blob-key-id"],
        secret: flags["blob-secret"],
        region: flags["blob-region"],
      }),
      log: log.with({ role: "services" }),
    };
    const built = newLocal(deps, [accounts, devicesync, conversation, contacts, uploads, stickers]);
    features = built.features;
    backend = built.local;
  }
  if (remote) {
    backend = new Remote(flags.services);
    log.info("gateway forwarding to services", { addr: flags.services });
  }

  let presence: Presence | undefined;
  if (gateway || roles.has("fanout")) {
    presence = await Presence.connect(flags.valkey, flags.node);
    closers.push(() => presence!.close());
  }

  if (roles.has("services")) {
    const rpc = await serve(backend!, flags["services-listen"]);
    log.info("services listening", { addr: flags["services-listen"] });
    closers.push(() => new Promise((r) => rpc.close(r)));
  }
  if (roles.has("services") || (gateway && !remote)) {
    for (const f of features) {
      const stop = f.start?.();
      if (stop)
        closers.push(() => {
          stop();
          return Promise.resolve();
        });
    }
  }

  let fanout: Fanout | undefined;
  if (roles.has("fanout")) {
    fanout = await startFanout(
      { ...deps!, presence: presence!, log: log.with({ role: "fanout" }) },
      Number(flags["fanout-concurrency"]),
    );
    log.info("fanout consuming");
  }

  let server: Server | undefined;
  if (gateway) {
    server = new Server(backend!, presence!, Number(flags["ack-interval-ms"]), log.with({ role: "gateway" }));
    await server.start();
  }
  const http = await serveHttp(flags.listen, {
    draining: () => server?.draining ?? false,
    upgrade: server ? (req, socket, head) => server.upgrade(req, socket, head) : undefined,
  });
  log.info("listening", { addr: flags.listen });

  let stopping = false;
  const shutdown = async () => {
    if (stopping) return;
    stopping = true;
    log.info("shutting down");
    await server?.drain();
    await fanout?.stop();
    http.close();
    for (const c of closers.reverse()) await c().catch(() => undefined);
    process.exit(0);
  };
  process.on("SIGTERM", () => void shutdown());
  process.on("SIGINT", () => void shutdown());
}

main().catch((err: unknown) => {
  log.error("startup failed", { err });
  process.exit(1);
});
