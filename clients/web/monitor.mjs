// The monitoring page: links to every dashboard and endpoint of this
// instance, with whether each answers. The infrastructure listens on
// loopback only, so the page is served only to requests from this host.

const services = [
  {
    group: "Infrastructure",
    items: [
      {
        name: "Grafana",
        probe: "http://127.0.0.1:3000/api/health",
        links: [
          ["dashboard", "http://localhost:3000/d/wrinkle"],
          ["home", "http://localhost:3000/"],
        ],
      },
      {
        name: "Prometheus",
        probe: "http://127.0.0.1:9090/-/healthy",
        links: [
          ["targets", "http://localhost:9090/targets"],
          ["query", "http://localhost:9090/query"],
        ],
      },
      {
        name: "Balancers (HAProxy)",
        probe: "http://127.0.0.1:8404/",
        links: [["statistics", "http://localhost:8404/"]],
        note: "make lb-up",
      },
      {
        name: "Kafka Connect (Debezium)",
        probe: "http://127.0.0.1:8083/connectors",
        links: [
          ["outbox connector", "http://localhost:8083/connectors/wrinkle-outbox/status"],
          ["connectors", "http://localhost:8083/connectors"],
        ],
      },
      {
        name: "Vitess (vtcombo)",
        probe: "http://127.0.0.1:15000/debug/status",
        links: [
          ["status", "http://localhost:15000/debug/status"],
          ["variables", "http://localhost:15000/debug/vars"],
        ],
      },
    ],
  },
  {
    group: "Backends",
    items: [
      backend("Backend or load balancer :8080", 8080, "make run-go-mono, run-java-mono or lb-up"),
      ...[1, 2, 3, 4].map((n) =>
        backend(
          `Services instance ${n} :${9100 + n}`,
          8190 + n,
          n === 1 ? "make run-go-services or run-java-services" : `INSTANCE=${n}`,
        ),
      ),
      backend("Go gateway :8180", 8180, "make run-go-gw"),
      backend("Elixir gateway :8181", 8181, "make run-elixir-gw", { pprof: false }),
      backend("Rust gateway :8182", 8182, "make run-rust-gw", { pprof: false }),
      backend("Java gateway :8183", 8183, "make run-java-gw", { pprof: false }),
    ],
  },
];

/** A backend process, probed and linked through its HTTP port. */
function backend(name, port, note, { pprof = true } = {}) {
  const links = [["metrics", `http://localhost:${port}/metrics`]];
  if (pprof) links.push(["pprof (if Go)", `http://localhost:${port}/debug/pprof/`]);
  return { name, probe: `http://127.0.0.1:${port}/healthz`, links, note };
}

const all = services.flatMap((g) => g.items);

function isLocal(req) {
  const a = req.socket.remoteAddress ?? "";
  return a === "127.0.0.1" || a === "::1" || a === "::ffff:127.0.0.1";
}

async function up(url) {
  try {
    const r = await fetch(url, { signal: AbortSignal.timeout(800), redirect: "manual" });
    return r.status < 500;
  } catch {
    return false;
  }
}

/** Handles /monitor and /monitor/status; returns false for other paths. */
export async function monitor(req, res, pathname) {
  if (pathname !== "/monitor" && pathname !== "/monitor/status") return false;
  if (!isLocal(req)) {
    res.writeHead(404).end();
    return true;
  }
  if (pathname === "/monitor/status") {
    const states = await Promise.all(all.map((s) => up(s.probe)));
    res.writeHead(200, { "content-type": "application/json", "cache-control": "no-store" });
    res.end(JSON.stringify(states));
    return true;
  }
  res.writeHead(200, { "content-type": "text/html; charset=utf-8", "cache-control": "no-store" });
  res.end(page());
  return true;
}

const esc = (s) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);

/** One service's row; the dot's index matches its place in /monitor/status. */
function row(s) {
  const note = s.note ? ` <small>${esc(s.note)}</small>` : "";
  const links = s.links
    .map(([label, href]) => `<a href="${esc(href)}" target="_blank" rel="noopener">${esc(label)}</a>`)
    .join(" · ");
  return `<li><span class="dot" data-i="${all.indexOf(s)}"></span><div><strong>${esc(s.name)}</strong>${note}<br>${links}</div></li>`;
}

function page() {
  const groups = services.map((g) => `<h2>${esc(g.group)}</h2><ul>${g.items.map(row).join("")}</ul>`).join("");
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Wrinkle monitoring</title>
<style>
  :root { color-scheme: light dark; --bg: #f4f4f2; --panel: #fff; --line: #e2e2dd; --ink: #1d1d1b; --muted: #6b6b66; --up: #2f7d4a; --down: #b3261e; }
  @media (prefers-color-scheme: dark) { :root { --bg: #121412; --panel: #1b1e1b; --line: #2c302c; --ink: #e8eae6; --muted: #9aa097; --up: #5cbf7c; --down: #f2b8b5; } }
  body { margin: 0; padding: 24px 16px; font: 16px/1.5 system-ui, sans-serif; color: var(--ink); background: var(--bg); }
  main { max-width: 640px; margin: 0 auto; }
  h1 { margin: 0 0 4px; }
  h2 { margin: 24px 0 8px; font-size: 14px; text-transform: uppercase; letter-spacing: .05em; color: var(--muted); }
  p { margin: 0; color: var(--muted); }
  ul { margin: 0; padding: 0; list-style: none; background: var(--panel); border: 1px solid var(--line); border-radius: 12px; }
  li { display: flex; gap: 12px; align-items: baseline; padding: 10px 14px; }
  li + li { border-top: 1px solid var(--line); }
  small { color: var(--muted); }
  a { color: inherit; }
  .dot { flex: none; width: 10px; height: 10px; border-radius: 50%; background: var(--line); }
  .dot.up { background: var(--up); }
  .dot.down { background: var(--down); }
</style>
</head>
<body>
<main>
<h1>Wrinkle monitoring</h1>
<p>Served only on this host, like the pages it links to. Green answers, red does not.</p>
${groups}
</main>
<script>
  async function refresh() {
    try {
      const states = await (await fetch("status", { cache: "no-store" })).json();
      document.querySelectorAll(".dot").forEach((d) => d.className = "dot " + (states[d.dataset.i] ? "up" : "down"));
    } catch {}
  }
  refresh();
  setInterval(refresh, 3000);
</script>
</body>
</html>`;
}
