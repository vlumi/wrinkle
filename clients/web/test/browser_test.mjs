// Drives the web client in headless Chrome over the DevTools protocol:
// two users in two pages, a message each way, a read receipt, a reload.
// Needs the page served at WEB_URL and a backend behind it; run.sh
// arranges both. No dependencies beyond Node and Chrome.
import { spawn } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";

const CHROME = process.env.CHROME || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const PORT = 9333;
// A name Chrome maps to loopback: unlike localhost, it is not a secure
// context, which is how the page is served on a LAN, so APIs that need one
// fail here too.
const PAGE = process.env.WEB_URL || "http://wrinkle.test:8081/clients/web/";

const profile = mkdtempSync(join(tmpdir(), "wrinkle-web-test-"));
const chrome = spawn(CHROME, [
  "--headless=new", `--remote-debugging-port=${PORT}`, "--no-first-run", "--no-default-browser-check",
  `--user-data-dir=${profile}`, "--host-resolver-rules=MAP wrinkle.test 127.0.0.1", "about:blank",
], { stdio: "ignore" });
process.on("exit", () => rmSync(profile, { recursive: true, force: true }));
process.on("exit", () => chrome.kill());

async function waitForChrome() {
  for (let i = 0; i < 50; i++) {
    try { return await (await fetch(`http://localhost:${PORT}/json/version`)).json(); } catch { await sleep(200); }
  }
  throw new Error("chrome did not start");
}

class Page {
  constructor(name) { this.name = name; this.id = 0; this.pending = new Map(); this.logs = []; }
  async open() {
    const target = await (await fetch(`http://localhost:${PORT}/json/new?about:blank`, { method: "PUT" })).json();
    this.ws = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((r) => (this.ws.onopen = r));
    this.ws.onmessage = (e) => {
      const m = JSON.parse(e.data);
      if (m.id && this.pending.has(m.id)) { this.pending.get(m.id)(m); this.pending.delete(m.id); }
      if (m.method === "Runtime.consoleAPICalled") this.logs.push(m.params.args.map((a) => a.value ?? a.description).join(" "));
      if (m.method === "Runtime.exceptionThrown") this.logs.push("EXCEPTION " + m.params.exceptionDetails.text + " " + (m.params.exceptionDetails.exception?.description ?? ""));
      // The page reports errors with alert(), which blocks all scripting
      // until dismissed: record it as a failure and dismiss it.
      if (m.method === "Page.javascriptDialogOpening") {
        this.logs.push("DIALOG " + m.params.message);
        this.ws.send(JSON.stringify({ id: ++this.id, method: "Page.handleJavaScriptDialog", params: { accept: true } }));
      }
    };
    await this.send("Runtime.enable");
    await this.send("Page.enable");
    await this.send("Emulation.setFocusEmulationEnabled", { enabled: true });
    await this.send("Page.navigate", { url: PAGE });
    await sleep(1500);
  }
  send(method, params = {}) {
    const id = ++this.id;
    return new Promise((resolve) => { this.pending.set(id, resolve); this.ws.send(JSON.stringify({ id, method, params })); });
  }
  async eval(expression) {
    const r = await this.send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
    if (r.result.exceptionDetails) throw new Error(`${this.name}: ${r.result.exceptionDetails.exception?.description ?? r.result.exceptionDetails.text}`);
    return r.result.result.value;
  }
  async waitFor(expression, timeoutMs = 15000) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      if (await this.eval(expression)) return;
      await sleep(200);
    }
    throw new Error(`${this.name}: timed out waiting for ${expression}\nlogs:\n${this.logs.join("\n")}`);
  }
}

function check(cond, msg) { if (!cond) throw new Error("FAIL: " + msg); console.log("ok  ", msg); }

await waitForChrome();
const suffix = Date.now().toString(36);
const alice = new Page("alice"), bob = new Page("bob");
await alice.open(); await bob.open();

check((await alice.eval("isSecureContext")) === false, "the page runs as on a LAN, not as a secure context");
check(
  (await alice.eval(`document.getElementById("url").value`)) === "ws://wrinkle.test:8080/v1/ws",
  "the gateway defaults to the page's host",
);

for (const [p, name] of [[alice, "web-alice-" + suffix], [bob, "web-bob-" + suffix]]) {
  await p.waitFor(`typeof T !== "undefined" && T.Frame !== undefined`);
  await p.eval(`document.getElementById("name").value = ${JSON.stringify(name)}; document.getElementById("signin-form").requestSubmit(); true`);
  await p.waitFor(`document.getElementById("status").textContent === "connected" && state.me !== null`);
  check((await p.eval(`state.me.name`)) === name, `${p.name} signed in as ${name}`);
}

// Alice opens a conversation with Bob and sends.
await alice.eval(`document.getElementById("peer").value = ${JSON.stringify("web-bob-" + suffix)}; document.getElementById("open-form").requestSubmit(); true`);
await alice.waitFor(`document.getElementById("peer-name").textContent === ${JSON.stringify("web-bob-" + suffix)}`);
check(true, "alice opened the conversation");
await alice.eval(`document.getElementById("text").value = "hello bob"; document.getElementById("send-form").requestSubmit(); true`);
await alice.waitFor(`[...document.querySelectorAll("#messages li:not(.pending)")].some(li => li.textContent.startsWith("hello bob"))`);
check(true, "alice sees her sent message confirmed");

// Bob sees it in the list with an unread badge, opens it, which marks it read.
await bob.waitFor(`[...document.querySelectorAll("#conversations li")].some(li => li.textContent.includes("hello bob") && li.querySelector(".unread")?.textContent === "1")`);
check(true, "bob's list shows the conversation with 1 unread");
await bob.eval(`document.querySelector("#conversations li").click(); true`);
await bob.waitFor(`[...document.querySelectorAll("#messages li")].some(li => li.textContent.startsWith("hello bob"))`);
check(true, "bob sees the message in the conversation");
await bob.waitFor(`!document.querySelector("#conversations .unread")`);
check(true, "bob's unread badge cleared");
await alice.waitFor(`[...document.querySelectorAll("#messages li.mine .meta")].some(m => m.textContent.endsWith("read"))`);
check(true, "alice sees the read receipt");

// Bob replies; Alice gets it pushed.
await bob.eval(`document.getElementById("text").value = "hi alice"; document.getElementById("send-form").requestSubmit(); true`);
await alice.waitFor(`[...document.querySelectorAll("#messages li:not(.mine)")].some(li => li.textContent.startsWith("hi alice"))`);
check(true, "alice received bob's reply");
await alice.waitFor(`state.conversations.values().next().value.read_seq === "2"`);
check(true, "alice marked the reply read while viewing");

// Reload Alice: the device id and cursor persist, history comes back.
await alice.send("Page.reload");
await sleep(1500);
await alice.waitFor(`typeof state !== "undefined" && state.me !== null && document.getElementById("status").textContent === "connected"`);
await alice.eval(`document.querySelector("#conversations li").click(); true`);
await alice.waitFor(`document.querySelectorAll("#messages li").length === 2`);
check(true, "alice reloaded and sees both messages from history");

if (process.env.SCREENSHOT) {
  const { writeFileSync } = await import("node:fs");
  await alice.send("Emulation.setDeviceMetricsOverride", { width: 1100, height: 700, deviceScaleFactor: 1, mobile: false });
  const shot = await alice.send("Page.captureScreenshot", { format: "png" });
  writeFileSync(process.env.SCREENSHOT, Buffer.from(shot.result.data, "base64"));
}

for (const p of [alice, bob]) {
  const errors = p.logs.filter((l) => l.startsWith("EXCEPTION") || l.startsWith("DIALOG"));
  check(errors.length === 0, `${p.name} had no exceptions or error dialogs${errors.length ? ": " + errors.join("; ") : ""}`);
}
console.log("web client test passed");
process.exit(0);
