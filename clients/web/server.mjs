// Serves the built web client (dist/) to the network, with no dependencies.
// Usage: node server.mjs [port]; HOST and PORT override the defaults
// (0.0.0.0:8081). The page connects to the gateway on its own host, port 8080.
// /monitor, for requests from this host only, links to the monitoring pages.
import { createReadStream } from "node:fs";
import { stat } from "node:fs/promises";
import { createServer } from "node:http";
import { extname, join, normalize, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { monitor } from "./monitor.mjs";

const root = resolve(fileURLToPath(new URL(".", import.meta.url)), "dist");
const host = process.env.HOST ?? "0.0.0.0";
const port = Number(process.argv[2] ?? process.env.PORT ?? 8081);

const types = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".json": "application/json",
  ".webmanifest": "application/manifest+json",
};

async function file(path) {
  try {
    const s = await stat(path);
    return s.isFile() ? path : null;
  } catch {
    return null;
  }
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url ?? "/", "http://localhost");
  if (url.pathname === "/healthz") {
    res.end("ok\n");
    return;
  }
  if (await monitor(req, res, url.pathname)) return;
  const rel = normalize(decodeURIComponent(url.pathname)).replace(/^(\.\.[/\\])+/, "");
  // One page: anything that is not a file gets index.html.
  const path = (await file(join(root, rel))) ?? join(root, "index.html");
  if (!path.startsWith(root)) {
    res.writeHead(403).end();
    return;
  }
  const isAsset = path.includes(`${join(root, "assets")}`);
  res.writeHead(200, {
    "content-type": types[extname(path)] ?? "application/octet-stream",
    // Hashed asset names never change; the page itself always revalidates.
    "cache-control": isAsset ? "public, max-age=31536000, immutable" : "no-cache",
  });
  createReadStream(path).pipe(res);
});

server.listen(port, host, () => {
  console.log(`web client on http://${host === "0.0.0.0" ? "<this host>" : host}:${port}/`);
  console.log(`monitoring on http://localhost:${port}/monitor (this host only)`);
});
