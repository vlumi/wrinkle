// Connecting: registering or signing in on a fresh connection, then Connect
// with the device's token on every (re)connection, after which the keys
// start and the model fills in; and signing out.

import { BackendError, Connection } from "../connection/connection";
import type { conversationActions } from "../conversations/actions";
import type { keyActions } from "../e2ee/actions";
import { ErrorCode } from "../gen/wrinkle/v1/frame_pb";
import type { DeviceCredentials } from "../gen/wrinkle/v1/messaging_pb";
import type { groupActions } from "../groups/actions";
import { hex } from "../ids";
import { rememberGateway } from "../signin/gateways";
import type { stickerActions } from "../stickers/actions";
import type { Api } from "./api";
import type { PendingAuth } from "./link";
import { saved } from "./saved";
import type { Session } from "./session";
import type { sync } from "./sync";

const deviceName = () => (typeof navigator === "undefined" ? "" : navigator.userAgent.slice(0, 64));

export function connectActions(
  { set, get, link, fail, signedOut }: Api,
  keys: ReturnType<typeof keyActions>,
  conversations: ReturnType<typeof conversationActions>,
  groups: ReturnType<typeof groupActions>,
  stickers: ReturnType<typeof stickerActions>,
  events: ReturnType<typeof sync>,
) {
  function connectTo(url: string) {
    link.conn?.close();
    link.conn = new Connection(url, {
      // Online once Connect completes.
      state: (status) => {
        if (status === "offline" && link.pendingAuth) {
          failAuth(link.pendingAuth, new Error("Could not reach the gateway."));
          return;
        }
        set({ status: status === "connected" ? "connecting" : status });
      },
      open: () => {
        onOpen().catch(fail);
      },
      event: events.onEvent,
      signedOut: () => signedOut(link.leaving ? link.leaving.notice : "Signed out."),
    });
    link.conn.open();
  }

  function failAuth(a: PendingAuth, err: Error) {
    link.pendingAuth = null;
    link.conn?.close();
    link.conn = null;
    set({ status: "offline" });
    a.reject(err);
  }

  async function authenticateOnOpen(c: Connection, a: PendingAuth) {
    // A connection lost from here on fails the call instead.
    link.pendingAuth = null;
    const value = { name: a.name, password: a.password, deviceName: deviceName() };
    let creds: DeviceCredentials | undefined;
    try {
      creds =
        a.mode === "register"
          ? (await c.call("register", { case: "register", value })).credentials
          : (await c.call("signIn", { case: "signIn", value })).credentials;
      if (!creds) throw new Error("no credentials in the response");
    } catch (err) {
      failAuth(a, err as Error);
      return false;
    }
    const credentials = {
      userId: hex(creds.userId),
      deviceId: creds.deviceId.toString(),
      deviceToken: creds.deviceToken,
      url: c.url,
    };
    saved.setCredentials(credentials);
    link.cursor = 0n;
    set({ credentials, notice: null });
    a.resolve();
    return true;
  }

  async function onOpen() {
    const c = link.conn;
    if (!c) return;
    // Connect must follow Register or SignIn within the connect deadline.
    if (link.pendingAuth && !(await authenticateOnOpen(c, link.pendingAuth))) return;
    const credentials = get().credentials;
    if (!credentials || link.conn !== c) return;
    let resp;
    try {
      resp = await c.call("connect", { case: "connect", value: { deviceToken: credentials.deviceToken } });
    } catch (err) {
      if (err instanceof BackendError && err.code === ErrorCode.UNAUTHENTICATED) {
        signedOut("This device was signed out. Sign in again.");
        return;
      }
      // Not connected, such as when pushes could not be set up: try again.
      c.restart();
      throw err;
    }
    rememberGateway(c.url);
    if (link.cursor === 0n) {
      // A device without history starts at the head; the past comes from the
      // conversation list and history requests.
      link.saveCursor(resp.logHead);
    }
    set({ me: resp.user ?? null, status: "connected", error: null });
    if (!(await keys.startKeys(credentials))) return;
    stickers.refreshPalette().catch(fail);
    await conversations.refreshConversations();
    await events.syncFromCursor();
    const current = get().current;
    if (current) {
      await conversations.loadHistory(current, 0n);
      await groups.refreshMembers(current);
    }
  }

  const actions = {
    resume() {
      const credentials = get().credentials;
      if (!credentials || link.conn) return;
      link.cursor = saved.cursor();
      connectTo(credentials.url);
    },

    authenticate(mode, name, password, url) {
      signedOut(null);
      return new Promise<void>((resolve, reject) => {
        link.pendingAuth = { mode, name, password, resolve, reject };
        connectTo(url);
      });
    },

    async signOut() {
      const c = link.conn;
      link.leaving = { notice: null };
      if (c && get().status === "connected") {
        await c.call("signOut", { case: "signOut", value: {} }).catch(() => {});
      }
      signedOut(null);
    },

    clearError() {
      set({ error: null });
    },
  } satisfies Partial<Session>;

  return { actions };
}
