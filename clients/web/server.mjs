// Serves the built web client (dist/) to the network, with no dependencies.
// Usage: node server.mjs [port]; HOST and PORT override the defaults
// (0.0.0.0:8081). Over HTTPS with the certificate from make certs, and the
// page then connects to the TLS edge on its own host, port 8443. Plain HTTP
// (no certificate, or WEB_TLS=0) only on loopback, so nothing on the network
// signs in without encryption.
// /monitor, for requests from this host only, links to the monitoring pages.
import { createReadStream, existsSync, readFileSync } from "node:fs";
import { stat } from "node:fs/promises";
import { createServer as createHTTPServer } from "node:http";
import { createServer as createHTTPSServer } from "node:https";
import { extname, join, normalize, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { monitor } from "./monitor.mjs";

const here = fileURLToPath(new URL(".", import.meta.url));
const root = resolve(here, "dist");
const certs = resolve(here, "../../infra/certs");
const tls =
  process.env.WEB_TLS !== "0" && existsSync(join(certs, "cert.pem"))
    ? { cert: readFileSync(join(certs, "cert.pem")), key: readFileSync(join(certs, "key.pem")) }
    : null;
const host = process.env.HOST ?? "0.0.0.0";
const port = Number(process.argv[2] ?? process.env.PORT ?? 8081);
if (!tls && !["127.0.0.1", "::1", "localhost"].includes(host)) {
  console.error("no certificate in infra/certs: run make tls-up, or serve on loopback (HOST=127.0.0.1)");
  process.exit(1);
}

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

const assets = join(root, "assets");

async function existingFile(path) {
  try {
    const s = await stat(path);
    return s.isFile() ? path : null;
  } catch {
    return null;
  }
}

const handler = async (req, res) => {
  const url = new URL(req.url ?? "/", "http://localhost");
  if (url.pathname === "/healthz") {
    res.end("ok\n");
    return;
  }
  if (await monitor(req, res, url.pathname)) return;
  const rel = normalize(decodeURIComponent(url.pathname)).replace(/^(\.\.[/\\])+/, "");
  // One page: anything that is not a file gets index.html; a missing file does not.
  const file = await existingFile(join(root, rel));
  if (!file && extname(rel)) {
    res.writeHead(404).end();
    return;
  }
  const path = file ?? join(root, "index.html");
  if (!path.startsWith(root)) {
    res.writeHead(403).end();
    return;
  }
  res.writeHead(200, {
    "content-type": types[extname(path)] ?? "application/octet-stream",
    // Hashed asset names never change; the page itself always revalidates.
    "cache-control": path.startsWith(assets) ? "public, max-age=31536000, immutable" : "no-cache",
  });
  createReadStream(path).pipe(res);
};
const server = tls ? createHTTPSServer(tls, handler) : createHTTPServer(handler);

server.listen(port, host, () => {
  const scheme = tls ? "https" : "http";
  console.log(`web client on ${scheme}://${host === "0.0.0.0" ? "<this host>" : host}:${port}/`);
  console.log(`monitoring on ${scheme}://localhost:${port}/monitor (this host only)`);
});
