// The internal RPC (wrinkle.backend.v1.BackendService) over gRPC: a server
// that hands calls to the in-process services, and a client a gateway uses
// in their place.
import http2 from "node:http2";
import { create } from "@bufbuild/protobuf";
import { createClient, type Client } from "@connectrpc/connect";
import { connectNodeAdapter, createGrpcTransport } from "@connectrpc/connect-node";
import { AckResponseSchema, BackendService, CallResponseSchema } from "../gen/wrinkle/backend/v1/services_pb.js";
import type { Request, Response } from "../gen/wrinkle/v1/frame_pb.js";
import { isZero, parseUUID, type UUID } from "../internal/ids.ts";
import type { Backend } from "./backend.ts";

const callTimeout = 10_000;

/** Serves the backend's calls on the address until the server is closed. */
export async function serve(backend: Backend, addr: string): Promise<http2.Http2Server> {
  const handler = connectNodeAdapter({
    grpc: true,
    connect: false,
    grpcWeb: false,
    routes: (router) =>
      router.service(BackendService, {
        async call(req) {
          const userId = isZero(req.userId) ? undefined : parseUUID(req.userId);
          const response = await backend.call(userId, req.deviceId, req.request!);
          return create(CallResponseSchema, { response });
        },
        async ack(req) {
          const userId = parseUUID(req.userId);
          if (userId) await backend.ack(userId, req.deviceId, req.cursor);
          return create(AckResponseSchema);
        },
      }),
  });
  const server = http2.createServer({ settings: { maxConcurrentStreams: 1 << 20 } }, handler);
  const i = addr.lastIndexOf(":");
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(Number(addr.slice(i + 1)), addr.slice(0, i) || "0.0.0.0", resolve);
  });
  return server;
}

/** Forwards a gateway's calls to a services process. */
export class Remote implements Backend {
  private readonly client: Client<typeof BackendService>;

  constructor(addr: string) {
    this.client = createClient(BackendService, createGrpcTransport({ baseUrl: `http://${addr}` }));
  }

  async call(userId: UUID | undefined, deviceId: bigint, request: Request): Promise<Response> {
    const res = await this.client.call(
      { userId: userId ?? new Uint8Array(), deviceId, request },
      { timeoutMs: callTimeout },
    );
    return res.response!;
  }

  async ack(userId: UUID, deviceId: bigint, cursor: bigint): Promise<void> {
    await this.client.ack({ userId, deviceId, cursor }, { timeoutMs: callTimeout });
  }
}
