// Drives the web client in headless Chrome over the DevTools protocol:
// two users register in two pages, send a message each way, see a read
// receipt, a deleted message, a rename and a picture, sign a second device in and out from the
// first, delete an account, reload, sign out and in again; then three
// users in a group: members added, messages, a topic change, read counts
// and a removal.
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
const DEADLINE_MS = 300_000;
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

// Alice deletes a message of her own: an in-page confirmation, then both
// sides show it removed, in the conversation and in the list.
const ownWith = (body) =>
  `${qa("message")}.find(li => li.classList.contains("mine") && li.querySelector(".text").textContent === ${JSON.stringify(body)})`;
await fill(alice, "text", "oops wrong chat");
await bob.waitFor(`${qa("message")}.some(li => li.querySelector(".text").textContent === "oops wrong chat")`);
await alice.waitFor(`!!${ownWith("oops wrong chat")}`);
check(
  await alice.eval(
    `${qa("message")}.filter(li => !li.classList.contains("mine")).every(li => !li.querySelector('[data-testid="delete-message"]'))`,
  ),
  "others' messages have no delete action",
);
await alice.eval(`(${ownWith("oops wrong chat")}.click(), true)`);
await alice.waitFor(
  `(() => { const b = ${ownWith("oops wrong chat")}?.querySelector('[data-testid="delete-message"]'); return !!b && b.getClientRects().length > 0 && getComputedStyle(b).visibility === "visible"; })()`,
);
await alice.eval(`(${ownWith("oops wrong chat")}.querySelector('[data-testid="delete-message"]').click(), true)`);
await alice.waitFor(`!!${q("confirm-delete")}`);
await alice.eval(click('[data-testid="cancel-delete"]'));
await alice.waitFor(`!${q("confirm-delete")} && !!${ownWith("oops wrong chat")}`);
check(true, "a tap shows the delete action on her own message; cancelling keeps it");
await alice.eval(`(${ownWith("oops wrong chat")}.querySelector('[data-testid="delete-message"]').click(), true)`);
await alice.waitFor(`!!${q("confirm-delete")}`);
await alice.eval(click('[data-testid="confirm-delete"]'));
const removedShown = `${qa("message")}.some(li => li.classList.contains("removed") && li.querySelector(".text").textContent === "Message removed") && !${qa("message")}.some(li => li.textContent.includes("oops wrong chat"))`;
await alice.waitFor(removedShown);
check(
  await alice.eval(`!document.querySelector('.message.removed [data-testid="delete-message"]')`),
  "alice deleted her message after confirming in the page; it shows as removed, without a delete action",
);
await bob.waitFor(removedShown);
await bob.waitFor(
  `[...document.querySelectorAll(".row")].some(r => r.querySelector(".last").textContent === "Message removed")`,
);
check(true, "bob sees the message removed, in the conversation and the list's preview");

// Back to the list on the phone.
await alice.eval(click(".back"));
await alice.waitFor(visible(".list"));
check(await alice.eval(`!${q("peer-name")}`), "the back button returns to the list");
check(
  await alice.eval(`document.querySelector(".row .last").textContent === "Message removed"`),
  "alice's list previews the removed message",
);

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

// Bob uploads an 800×600 picture: it becomes a 512 square, and Alice sees it.
await bob.eval(`new Promise((resolve) => {
  const canvas = document.createElement("canvas");
  canvas.width = 800;
  canvas.height = 600;
  const ctx = canvas.getContext("2d");
  ctx.fillStyle = "#c33";
  ctx.fillRect(0, 0, 800, 600);
  ctx.fillStyle = "#36c";
  ctx.fillRect(100, 0, 600, 600);
  canvas.toBlob((blob) => {
    const input = ${q("picture-input")};
    const files = new DataTransfer();
    files.items.add(new File([blob], "picture.png", { type: "image/png" }));
    input.files = files.files;
    input.dispatchEvent(new Event("change", { bubbles: true }));
    resolve(true);
  }, "image/png");
})`);
const picture = (sel) =>
  `(() => { const img = document.querySelector(${JSON.stringify(sel + " img")}); return !!img && img.complete && img.naturalWidth === 512 && img.naturalHeight === 512 && img.src.startsWith("http://wrinkle.test:9200/blobs/"); })()`;
await bob.waitFor(
  `${qa("settings-done")}.some(p => p.textContent === "Picture changed.") && ${picture(".profile .avatar")}`,
);
check(true, "bob uploaded a picture, cropped and scaled to 512 × 512, read from the blob address");
await alice.waitFor(picture(".conversation header .avatar"));
check(true, "alice sees bob's picture");
await bob.eval(click('[data-testid="remove-picture"]'));
await bob.waitFor(`!document.querySelector(".profile .avatar img") && ${text("avatar")} === "🦊"`);
await alice.waitFor(`document.querySelector(".conversation header .avatar").textContent === "🦊"`);
check(true, "bob removed the picture; alice sees his icon again");

// Bob changes his password; his other devices, none here, are signed out.
await bob.eval(type("current-password", PASSWORD));
await bob.eval(type("new-password", PASSWORD + "!"));
await bob.waitFor(`${q("new-password")}.value === ${JSON.stringify(PASSWORD + "!")}`);
await fill(bob, "confirm-password", PASSWORD + "!");
await bob.waitFor(`${qa("settings-done")}.some(p => p.textContent.includes("other devices were signed out"))`);
check(true, "bob changed his password and is told his other devices were signed out");

// Bob signs in on a phone too; his first device lists it live, renames it
// and signs it out, which closes its connection for good.
const nthDevice = (i, testid) => `${qa("device")}[${i}]?.querySelector('[data-testid="${testid}"]')`;
await bob.waitFor(`${qa("device")}.length === 1 && !!${nthDevice(0, "this-device")}`);
const phone = new Page("bob-phone", 390);
await phone.open();
await authenticate(phone, "signIn", robertName, PASSWORD + "!");
await phone.waitFor(`${text("status")} === "online"`);
await bob.waitFor(
  `${qa("device")}.length === 2 && !!${nthDevice(0, "this-device")} && !${nthDevice(1, "this-device")}`,
);
check(true, "bob's devices list the new one as it signs in, this one marked");
await bob.eval(`(${nthDevice(1, "rename-device")}.click(), true)`);
await bob.waitFor(`!!${q("device-new-name")}`);
await fill(bob, "device-new-name", "Bob's phone");
await bob.waitFor(`${nthDevice(1, "device-name")}?.textContent === "Bob's phone" && !${q("device-new-name")}`);
check(true, "bob renamed his other device");
await bob.eval(`(${nthDevice(1, "signout-device")}.click(), true)`);
await phone.waitFor(`!!${q("url")} && document.querySelector(".notice")?.textContent === "Signed out."`);
check(
  (await phone.eval(`localStorage.getItem("wrinkle.credentials")`)) === null,
  "the signed-out phone is back at sign-in, told so, its credentials forgotten",
);
await bob.waitFor(`${qa("device")}.length === 1`);
check(true, "bob's devices no longer list the phone");
await sleep(1500);
check(await phone.eval(`!!${q("url")} && !${q("status")}`), "the phone does not reconnect");

// Dave writes to Bob, then deletes his account: Bob sees a deleted account,
// and the name is free again.
const daveName = "web-dave-" + suffix;
await authenticate(phone, "register", daveName, PASSWORD);
await phone.waitFor(`${text("status")} === "online"`);
await fill(phone, "peer", robertName);
await phone.waitFor(`${text("peer-name")} === ${JSON.stringify(robertName)}`);
await fill(phone, "text", "bye bob");
await bob.waitFor(
  `[...document.querySelectorAll(".row")].some(r => r.querySelector(".peer .name").textContent === ${JSON.stringify(daveName)})`,
);
await phone.eval(click('[data-testid="open-settings"]'));
await phone.eval(click('[data-testid="delete-account"]'));
await phone.waitFor(`!!${q("delete-password")}`);
await fill(phone, "delete-password", PASSWORD + "?");
await phone.waitFor(`${text("settings-error")} === "The password is wrong."`);
check(true, "deleting an account with a wrong password says so");
await fill(phone, "delete-password", PASSWORD);
await phone.waitFor(`!!${q("url")} && document.querySelector(".notice")?.textContent === "Your account was deleted."`);
check(true, "dave deleted his account and is back at sign-in");
await bob.waitFor(
  `[...document.querySelectorAll(".row")].some(r => r.querySelector(".peer .name").textContent === "Deleted account" && r.querySelector(".avatar.deleted") && r.textContent.includes("bye bob"))`,
);
check(true, "bob's list shows a deleted account, the message kept");
await authenticate(phone, "register", daveName, PASSWORD);
await phone.waitFor(`${text("status")} === "online" && ${text("me")} === ${JSON.stringify(daveName)}`);
check(true, "the deleted account's name can be registered again");

// Reload Alice: the session resumes with the device token and history comes back.
await alice.send("Page.reload");
await sleep(1500);
await alice.waitFor(`${text("status")} === "online"`);
await alice.eval(click(".row"));
await alice.waitFor(`${qa("message")}.length === 3`);
check((await alice.eval(text("me"))) === aliceName, "alice reloaded as herself and sees the messages from history");
check(await alice.eval(removedShown), "the removed message stays removed in history");
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

// A group of three: Gina on a phone creates it with Hank, Hank adds Ivy.
const gina = new Page("gina", 390),
  hank = new Page("hank", 1100),
  ivy = new Page("ivy", 1100);
const ginaName = "web-gina-" + suffix;
const hankName = "web-hank-" + suffix;
const ivyName = "web-ivy-" + suffix;
for (const [p, name] of [
  [gina, ginaName],
  [hank, hankName],
  [ivy, ivyName],
]) {
  await p.open();
  await authenticate(p, "register", name, PASSWORD);
  await p.waitFor(`${text("status")} === "online" && ${text("me")} === ${JSON.stringify(name)}`);
}
check(true, "gina, hank and ivy registered");

const topic = "Trip " + suffix;
const topic2 = "Trip plans " + suffix;
const has = (testid, s) => `${qa(testid)}.some(el => el.textContent === ${JSON.stringify(s)})`;
const noOverflow = `document.documentElement.scrollWidth <= window.innerWidth`;
/** The message from the given sender with the given text, shown with the sender's name. */
const fromSender = (name, body) =>
  `${qa("message")}.some(li => li.querySelector('[data-testid="sender"] .sender-name')?.textContent === ${JSON.stringify(name)} && li.querySelector(".text").textContent === ${JSON.stringify(body)})`;

await gina.eval(click('[data-testid="new-group"]'));
await gina.waitFor(`!!${q("group-topic")}`);
await gina.eval(type("group-topic", topic));
await gina.waitFor(`${q("group-topic")}.value === ${JSON.stringify(topic)}`);
await fill(gina, "group-members", hankName.toUpperCase());
await gina.waitFor(`${text("topic")} === ${JSON.stringify(topic)} && ${text("member-count")} === "2 members"`);
check(await gina.eval(`!${visible(".list")}`), "gina created the group; on a phone it replaces the list");
await gina.waitFor(has("notice", `You created the group “${topic}” with ${hankName}`));
check(true, "the group starts with a notice of its creation");
check(await gina.eval(noOverflow), "the group fits the phone's width");

await hank.waitFor(
  `[...document.querySelectorAll(".row")].some(r => r.querySelector(".peer .name").textContent === ${JSON.stringify(topic)} && r.querySelector('[data-testid="group-avatar"]') && r.querySelector(".last").textContent === ${JSON.stringify(`${ginaName} created the group “${topic}” with you`)})`,
);
check(true, "hank's list shows the group by its topic, a group avatar and the notice");
await hank.eval(click(".row"));
await hank.waitFor(`${text("topic")} === ${JSON.stringify(topic)}`);

// Hank, not an admin, adds Ivy from the group's info.
await hank.eval(click('[data-testid="group-info"]'));
await hank.waitFor(`${qa("member")}.length === 2`);
const memberRows = `${qa("member")}.map(li => li.querySelector('[data-testid="member-name"]').textContent + (li.querySelector('[data-testid="admin-tag"]') ? "*" : "")).sort().join(",")`;
await hank
  .waitFor(`${memberRows} === ${JSON.stringify([ginaName + "*", hankName].sort().join(","))}`)
  .catch(async (err) => {
    console.error("members:", await hank.eval(memberRows));
    throw err;
  });
check(
  await hank.eval(`!${q("remove-member")} && !${q("toggle-admin")}`),
  "the info lists the members, the creator as admin; a non-admin cannot remove anyone",
);
await fill(hank, "add-members", ivyName);
await hank.waitFor(`${qa("member")}.length === 3`);
await hank.eval(click('[data-testid="close-info"]'));
await hank.waitFor(has("notice", `You added ${ivyName}`));
await gina.waitFor(has("notice", `${hankName} added ${ivyName}`) + ` && ${text("member-count")} === "3 members"`);
check(true, "hank added ivy; gina sees the notice and three members");

await ivy.waitFor(
  `[...document.querySelectorAll(".row")].some(r => r.querySelector(".peer .name").textContent === ${JSON.stringify(topic)})`,
);
await ivy.eval(click(".row"));
await ivy.waitFor(has("notice", `${hankName} added you`));
check(
  await ivy.eval(`${qa("notice")}.length === 1 && !document.querySelector(".earlier")`),
  "ivy sees the group from the notice that added her on",
);

// Messages from all three, each shown with the sender's name to the others.
await fill(gina, "text", "hello all");
await hank.waitFor(fromSender(ginaName, "hello all"));
await fill(hank, "text", "hi from hank");
await ivy.waitFor(fromSender(hankName, "hi from hank") + " && " + fromSender(ginaName, "hello all"));
await fill(ivy, "text", "ivy here");
await gina.waitFor(fromSender(hankName, "hi from hank") + " && " + fromSender(ivyName, "ivy here"));
await hank.waitFor(fromSender(ivyName, "ivy here"));
check(true, "messages from several members arrive with their senders' names");
check(
  await gina.eval(
    `${qa("notice")}.every(li => !li.querySelector('[data-testid="delete-message"]')) && ${qa("message")}.every(li => !!li.querySelector('[data-testid="delete-message"]') === li.classList.contains("mine"))`,
  ),
  "in the group, only gina's own messages have a delete action; notices and others' messages none",
);
await hank.waitFor(
  `[...document.querySelectorAll(".row")].some(r => r.querySelector(".last").textContent === ${JSON.stringify(`${ivyName}: ivy here`)})`,
);
check(true, "hank's list previews the latest message with its sender's name");
await gina.waitFor(
  `${qa("message")}.some(li => li.classList.contains("mine") && li.querySelector(".text").textContent === "hello all" && li.querySelector(".meta").textContent.endsWith("Read by 2"))`,
);
check(true, "gina's message shows it was read by both others");
check(await gina.eval(noOverflow), "the conversation fits the phone's width");

// Ivy changes the topic; everyone sees it.
await ivy.eval(click('[data-testid="group-info"]'));
await ivy.waitFor(`${qa("member")}.length === 3`);
await fill(ivy, "new-topic", topic2);
await ivy.waitFor(`${text("info-topic")} === ${JSON.stringify(topic2)}`);
await ivy.eval(click('[data-testid="close-info"]'));
for (const p of [gina, hank, ivy]) {
  await p.waitFor(`${text("topic")} === ${JSON.stringify(topic2)}`);
}
await gina.waitFor(has("notice", `${ivyName} changed the topic to “${topic2}”`));
await hank.waitFor(
  `[...document.querySelectorAll(".row")].some(r => r.querySelector(".peer .name").textContent === ${JSON.stringify(topic2)})`,
);
check(true, "ivy changed the topic; everyone sees the new one");

// Gina, the admin, removes Ivy on her phone.
await gina.eval(click('[data-testid="group-info"]'));
await gina.waitFor(`${qa("member")}.length === 3 && !!${q("remove-member")}`);
check(await gina.eval(noOverflow), "the group info fits the phone's width");
await gina.eval(
  `(${qa("member")}.find(li => li.querySelector('[data-testid="member-name"]').textContent === ${JSON.stringify(ivyName)}).querySelector('[data-testid="remove-member"]').click(), true)`,
);
await gina.waitFor(`${qa("member")}.length === 2`);
await gina.eval(click('[data-testid="close-info"]'));
await gina.waitFor(has("notice", `You removed ${ivyName}`) + ` && ${text("member-count")} === "2 members"`);
await hank.waitFor(has("notice", `${ginaName} removed ${ivyName}`));
await ivy.waitFor(
  `${text("former-member")} === "You are no longer in this group" && !${q("text")} && ${has("notice", `${ginaName} removed you`)}`,
);
check(
  await ivy.eval(`${qa("message")}.length === 3`),
  "ivy sees she is no longer in the group, without a composer, the history kept",
);
for (const p of [gina, hank, ivy]) {
  check(await p.eval(`!${q("error")}`), `${p.name} saw no error banner`);
}

if (process.env.SCREENSHOT) {
  const { writeFileSync } = await import("node:fs");
  for (const p of [alice, bob, phone, gina, hank, ivy]) {
    const shot = await p.send("Page.captureScreenshot", { format: "png" });
    writeFileSync(process.env.SCREENSHOT.replace(/\.png$/, `-${p.name}.png`), Buffer.from(shot.result.data, "base64"));
  }
}

for (const p of [alice, bob, phone, gina, hank, ivy]) {
  const errors = p.logs.filter((l) => l.startsWith("EXCEPTION") || l.startsWith("DIALOG"));
  check(
    errors.length === 0,
    `${p.name} had no exceptions or error dialogs${errors.length ? ": " + errors.join("; ") : ""}`,
  );
}
console.log("web client test passed");
process.exit(0);
