// Drives the web client in headless Chrome over the DevTools protocol:
// two users register in two pages, send a message each way, see a read
// receipt and a rename, reload, sign out and in again.
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
const PAGE = process.env.WEB_URL || "http://wrinkle.test:8081/";

// A hard deadline: a hang anywhere (a dialog, a paused page, the backend)
// fails the run instead of stalling the gate.
const DEADLINE_MS = 120_000;
setTimeout(() => {
  console.error(`browser test: no result within ${DEADLINE_MS / 1000} s`);
  process.exit(1);
}, DEADLINE_MS).unref();

const profile = mkdtempSync(join(tmpdir(), "wrinkle-web-test-"));
const chrome = spawn(
  CHROME,
  [
    "--headless=new",
    `--remote-debugging-port=${PORT}`,
    "--no-first-run",
    "--no-default-browser-check",
    `--user-data-dir=${profile}`,
    "--host-resolver-rules=MAP wrinkle.test 127.0.0.1",
    "about:blank",
  ],
  { stdio: "ignore" },
);
process.on("exit", () => rmSync(profile, { recursive: true, force: true }));
process.on("exit", () => chrome.kill());

async function waitForChrome() {
  for (let i = 0; i < 50; i++) {
    try {
      return await (await fetch(`http://localhost:${PORT}/json/version`)).json();
    } catch {
      await sleep(200);
    }
  }
  throw new Error("chrome did not start");
}

/**
 * A DevTools protocol session over the given socket. Returns send, which
 * resolves with the whole reply; events go to onEvent.
 */
async function devtools(url, onEvent = () => {}) {
  const ws = new WebSocket(url);
  await new Promise((r) => (ws.onopen = r));
  let id = 0;
  const pending = new Map();
  ws.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.id && pending.has(m.id)) {
      pending.get(m.id)(m);
      pending.delete(m.id);
    }
    if (m.method) onEvent(m);
  };
  return (method, params = {}) =>
    new Promise((resolve) => {
      const i = ++id;
      pending.set(i, resolve);
      ws.send(JSON.stringify({ id: i, method, params }));
    });
}

class Page {
  constructor(name, width = 1100) {
    this.name = name;
    this.width = width;
    this.logs = [];
  }
  async open() {
    // Each user in a browser context of their own, so localStorage, where
    // the client remembers its session, is not shared.
    const context = await browser("Target.createBrowserContext", { disposeOnDetach: false });
    const { targetId } = await browser("Target.createTarget", {
      url: "about:blank",
      browserContextId: context.browserContextId,
    });
    const targets = await (await fetch(`http://localhost:${PORT}/json/list`)).json();
    const target = targets.find((t) => t.id === targetId);
    this.send = await devtools(target.webSocketDebuggerUrl, (m) => this.observe(m));
    await this.send("Runtime.enable");
    await this.send("Page.enable");
    await this.send("Emulation.setFocusEmulationEnabled", { enabled: true });
    await this.send("Emulation.setDeviceMetricsOverride", {
      width: this.width,
      height: 800,
      deviceScaleFactor: 2,
      mobile: this.width < 720,
    });
    await this.send("Page.navigate", { url: PAGE });
    await sleep(1500);
  }
  observe(m) {
    if (m.method === "Runtime.consoleAPICalled")
      this.logs.push(m.params.args.map((a) => a.value ?? a.description).join(" "));
    if (m.method === "Runtime.exceptionThrown")
      this.logs.push(
        "EXCEPTION " + m.params.exceptionDetails.text + " " + (m.params.exceptionDetails.exception?.description ?? ""),
      );
    // An alert() blocks all scripting until dismissed: record it as a
    // failure and dismiss it.
    if (m.method === "Page.javascriptDialogOpening") {
      this.logs.push("DIALOG " + m.params.message);
      this.send("Page.handleJavaScriptDialog", { accept: true });
    }
  }
  async eval(expression) {
    const r = await this.send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
    if (r.result.exceptionDetails)
      throw new Error(
        `${this.name}: ${r.result.exceptionDetails.exception?.description ?? r.result.exceptionDetails.text}`,
      );
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

function check(cond, msg) {
  if (!cond) throw new Error("FAIL: " + msg);
  console.log("ok  ", msg);
}

const version = await waitForChrome();
const browserSend = await devtools(version.webSocketDebuggerUrl);
const browser = async (method, params) => (await browserSend(method, params)).result;
const suffix = Date.now().toString(36);
// Alice on a phone-sized screen, Bob on a desktop one.
const alice = new Page("alice", 390),
  bob = new Page("bob", 1100);
await alice.open();
await bob.open();

const q = (testid) => `document.querySelector('[data-testid="${testid}"]')`;
const qa = (testid) => `[...document.querySelectorAll('[data-testid="${testid}"]')]`;
// React tracks input values; set them through the native setter so it sees the change.
const type = (testid, value) =>
  `(() => { const el = ${q(testid)}; Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(el, ${JSON.stringify(value)}); el.dispatchEvent(new Event("input", { bubbles: true })); return true; })()`;
// Submits once React has rendered the typed value: the form's handler reads
// component state, which a submit right after the input event may not have yet.
// A timeout rather than an animation frame: frames pause in a page the
// browser considers hidden, which can be either of the two.
const submit = (testid) => `new Promise((resolve) => setTimeout(() => {
  const el = ${q(testid)};
  const button = el.form.querySelector('button[type="submit"]');
  if (button && button.disabled) { resolve(false); return; }
  el.form.requestSubmit();
  resolve(true);
}, 50))`;
async function fill(p, testid, value) {
  await p.eval(type(testid, value));
  await p.waitFor(
    `${q(testid)}.value === ${JSON.stringify(value)} && !${q(testid)}.form.querySelector('button[type="submit"]')?.disabled`,
  );
  if (!(await p.eval(submit(testid)))) throw new Error(`${p.name}: ${testid} could not be submitted`);
}
const visible = (sel) =>
  `(() => { const el = document.querySelector(${JSON.stringify(sel)}); return !!el && el.getClientRects().length > 0; })()`;

const click = (sel) => `(document.querySelector(${JSON.stringify(sel)}).click(), true)`;
const text = (testid) => `${q(testid)}?.textContent`;

check((await alice.eval("isSecureContext")) === false, "the page runs as on a LAN, not as a secure context");
await alice.waitFor(`!!${q("url")}`);
check(
  (await alice.eval(`${q("url")}.value`)) === "ws://wrinkle.test:8080/v1/ws",
  "the gateway defaults to the page's host",
);
check((await alice.eval(`${qa("gateway")}.length`)) === 0, "no gateways are remembered before the first sign-in");

const GATEWAY = "ws://wrinkle.test:8080/v1/ws";
const PASSWORD = "correct horse";
const aliceName = "web-alice-" + suffix;
const bobName = "web-bob-" + suffix;
const robertName = "web-robert-" + suffix;

/** Fills the sign-in form in the given mode and submits it. */
async function authenticate(p, mode, name, password) {
  await p.waitFor(`!!${q("url")}`);
  await p.eval(click(`[data-testid="mode-${mode}"]`));
  await p.waitFor(`${q(`mode-${mode}`)}.getAttribute("aria-selected") === "true"`);
  const fields = [
    ["url", GATEWAY],
    ["name", name],
    ["password", password],
  ];
  if (mode === "register") fields.push(["confirm", password]);
  for (const [testid, value] of fields.slice(0, -1)) {
    await p.eval(type(testid, value));
    await p.waitFor(`${q(testid)}.value === ${JSON.stringify(value)}`);
  }
  const [testid, value] = fields[fields.length - 1];
  await fill(p, testid, value);
}

for (const [p, name] of [
  [alice, aliceName],
  [bob, bobName],
]) {
  await authenticate(p, "register", name, PASSWORD);
  await p.waitFor(`${text("status")} === "online" && ${text("me")} === ${JSON.stringify(name)}`);
  check(true, `${p.name} registered as ${name}`);
}

// Alice opens a conversation with Bob, his name in another case, and sends.
await fill(alice, "peer", bobName.toUpperCase());
await alice.waitFor(`${text("peer-name")} === ${JSON.stringify(bobName)}`);
check(await alice.eval(`!${visible(".list")}`), "on a phone, the conversation replaces the list");
check((await alice.eval(`document.querySelector(".conversation .avatar").textContent`)) === "W", "no icon: an initial");
await fill(alice, "text", "hello bob");
await alice.waitFor(`${qa("message")}.some(li => li.textContent.startsWith("hello bob"))`);
check(true, "alice sees her sent message confirmed");

// Bob sees it in the list with an unread badge, opens it, which marks it read.
await bob.waitFor(
  `[...document.querySelectorAll(".row")].some(r => r.textContent.includes("hello bob") && r.querySelector('[data-testid="unread"]')?.textContent === "1")`,
);
check(true, "bob's list shows the conversation with 1 unread");
await bob.eval(click(".row"));
await bob.waitFor(`${qa("message")}.some(li => li.textContent.startsWith("hello bob"))`);
check(await bob.eval(visible(".list")), "on a desktop, the list stays beside the conversation");
await bob.waitFor(`!${q("unread")}`);
check(true, "bob's unread badge cleared");
await alice.waitFor(
  `${qa("message")}.some(li => li.classList.contains("mine") && li.querySelector(".meta").textContent.endsWith("read"))`,
);
check(true, "alice sees the read receipt");

// Bob replies; Alice gets it pushed and marks it read while viewing.
await fill(bob, "text", "hi alice");
await alice.waitFor(
  `${qa("message")}.some(li => !li.classList.contains("mine") && li.textContent.startsWith("hi alice"))`,
);
check(true, "alice received bob's reply");
await bob.waitFor(
  `${qa("message")}.some(li => li.classList.contains("mine") && li.textContent.startsWith("hi alice") && li.querySelector(".meta").textContent.endsWith("read"))`,
);
check(true, "alice marked the reply read while viewing");

// Back to the list on the phone.
await alice.eval(click(".back"));
await alice.waitFor(visible(".list"));
check(await alice.eval(`!${q("peer-name")}`), "the back button returns to the list");

// Bob renames himself and picks an icon; Alice sees both.
await bob.eval(click('[data-testid="open-settings"]'));
await bob.waitFor(`${text("profile-name")} === ${JSON.stringify(bobName)}`);
await fill(bob, "new-name", robertName);
await bob.waitFor(`${text("me")} === ${JSON.stringify(robertName)} && ${text("settings-done")} === "Name changed."`);
check(true, "bob renamed himself");
await bob.eval(click('[data-testid="icon-fox"]'));
await bob.waitFor(`${q("icon-fox")}.getAttribute("aria-checked") === "true" && ${text("avatar")} === "🦊"`);
check(true, "bob picked an icon");
await alice.waitFor(
  `[...document.querySelectorAll(".row")].some(r => r.querySelector(".peer .name").textContent === ${JSON.stringify(robertName)} && r.querySelector(".avatar").textContent === "🦊")`,
);
check(true, "alice's list shows bob's new name and icon");
await alice.eval(click(".row"));
await alice.waitFor(`${text("peer-name")} === ${JSON.stringify(robertName)}`);
check(true, "the conversation header shows the new name");

// Bob changes his password; his other devices, none here, are signed out.
await bob.eval(type("current-password", PASSWORD));
await bob.eval(type("new-password", PASSWORD + "!"));
await bob.waitFor(`${q("new-password")}.value === ${JSON.stringify(PASSWORD + "!")}`);
await fill(bob, "confirm-password", PASSWORD + "!");
await bob.waitFor(`${qa("settings-done")}.some(p => p.textContent.includes("other devices were signed out"))`);
check(true, "bob changed his password and is told his other devices were signed out");

// Reload Alice: the session resumes with the device token and history comes back.
await alice.send("Page.reload");
await sleep(1500);
await alice.waitFor(`${text("status")} === "online"`);
await alice.eval(click(".row"));
await alice.waitFor(`${qa("message")}.length === 2`);
check((await alice.eval(text("me"))) === aliceName, "alice reloaded as herself and sees both messages from history");
const revoked = await alice.eval(`localStorage.getItem("wrinkle.credentials")`);

// Signed out, Alice can pick the gateway she used with one click.
await alice.eval(click('[data-testid="open-settings"]'));
await alice.waitFor(`!!${q("signout")}`);
await alice.eval(click('[data-testid="signout"]'));
await alice.waitFor(`${qa("gateway")}.length === 1`);
check((await alice.eval(text("gateway"))) === "wrinkle.test:8080", "the gateway she signed in through is remembered");
await alice.eval(type("url", "ws://elsewhere.test:1/v1/ws"));
await alice.waitFor(`${q("url")}.value === "ws://elsewhere.test:1/v1/ws"`);
await alice.eval(`(${q("gateway")}.click(), true)`);
await alice.waitFor(`${q("url")}.value === ${JSON.stringify(GATEWAY)}`);
check(true, "one click on a remembered gateway fills the field");

// Taken names and wrong passwords are told apart from other errors.
await authenticate(alice, "register", robertName.toUpperCase(), PASSWORD);
await alice.waitFor(`${text("signin-error")} === "That name is taken."`);
check(true, "registering a taken name, in another case, says it is taken");
await authenticate(alice, "register", " " + aliceName, PASSWORD);
await alice.waitFor(`${text("signin-error")}?.includes("start or end with a space")`);
check(true, "an invalid name is explained");
await authenticate(alice, "signIn", aliceName, PASSWORD + "?");
await alice.waitFor(`${text("signin-error")} === "Wrong name or password."`);
check(true, "a wrong password says so");

await alice.eval(`(${q("forget-gateway")}.click(), true)`);
await alice.waitFor(`${qa("gateway")}.length === 0`);
await alice.send("Page.reload");
await alice.waitFor(`!!${q("url")}`);
check((await alice.eval(`${qa("gateway")}.length`)) === 0, "a forgotten gateway stays forgotten");

// Signing in again is a new device, with the conversation there.
await authenticate(alice, "signIn", aliceName.toUpperCase(), PASSWORD);
await alice.waitFor(`${text("status")} === "online" && ${text("me")} === ${JSON.stringify(aliceName)}`);
await alice.waitFor(
  `[...document.querySelectorAll(".row")].some(r => r.textContent.includes(${JSON.stringify(robertName)}))`,
);
check(true, "alice signed in again, her name in another case, and sees the conversation");

// A signed-out device's token is refused: back to sign-in, with a note.
await alice.eval(`(localStorage.setItem("wrinkle.credentials", ${JSON.stringify(revoked)}), true)`);
await alice.send("Page.reload");
await alice.waitFor(`!!${q("url")} && document.querySelector(".notice")?.textContent.includes("signed out")`);
check(true, "a revoked token returns to sign-in with a note");

if (process.env.SCREENSHOT) {
  const { writeFileSync } = await import("node:fs");
  for (const p of [alice, bob]) {
    const shot = await p.send("Page.captureScreenshot", { format: "png" });
    writeFileSync(process.env.SCREENSHOT.replace(/\.png$/, `-${p.name}.png`), Buffer.from(shot.result.data, "base64"));
  }
}

for (const p of [alice, bob]) {
  const errors = p.logs.filter((l) => l.startsWith("EXCEPTION") || l.startsWith("DIALOG"));
  check(
    errors.length === 0,
    `${p.name} had no exceptions or error dialogs${errors.length ? ": " + errors.join("; ") : ""}`,
  );
}
console.log("web client test passed");
process.exit(0);
