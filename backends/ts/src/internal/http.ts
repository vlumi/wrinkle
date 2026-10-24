// The HTTP server every role serves: metrics, health, the debug endpoints on
// loopback, and the gateway's WebSocket upgrade.
import http, { type IncomingMessage, type ServerResponse } from "node:http";
import type { Duplex } from "node:stream";
import { registry } from "./metrics.ts";

export interface HttpOptions {
  draining: () => boolean;
  upgrade?: (req: IncomingMessage, socket: Duplex, head: Buffer) => void;
}

const isLoopback = (addr: string | undefined) => addr === "127.0.0.1" || addr === "::1" || addr === "::ffff:127.0.0.1";

export async function serveHttp(addr: string, opts: HttpOptions): Promise<http.Server> {
  const server = http.createServer((req, res) => void handle(req, res, opts));
  server.on("upgrade", (req: IncomingMessage, socket: Duplex, head: Buffer) => {
    if (opts.upgrade && req.url?.split("?")[0] === "/v1/ws") opts.upgrade(req, socket, head);
    else socket.end("HTTP/1.1 404 Not Found\r\nConnection: close\r\n\r\n");
  });
  const i = addr.lastIndexOf(":");
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(Number(addr.slice(i + 1)), addr.slice(0, i) || "0.0.0.0", resolve);
  });
  return server;
}

async function handle(req: IncomingMessage, res: ServerResponse, opts: HttpOptions): Promise<void> {
  const path = req.url?.split("?")[0];
  if (req.method === "GET" && path === "/metrics") {
    res.setHeader("Content-Type", registry.contentType);
    res.end(await registry.metrics());
  } else if (req.method === "GET" && path === "/healthz") {
    if (opts.draining()) res.writeHead(503).end("shutting down\n");
    else res.end("ok\n");
  } else if (req.method === "POST" && path === "/debug/gc") {
    if (!isLoopback(req.socket.remoteAddress)) {
      res.writeHead(403).end("forbidden\n");
      return;
    }
    // node runs with --expose-gc (see the launcher); without it there is no gc.
    (globalThis as { gc?: () => void }).gc?.();
    res.end("ok\n");
  } else if (path === "/v1/ws") {
    res.writeHead(426).end("upgrade required\n");
  } else {
    res.writeHead(404).end("not found\n");
  }
}
