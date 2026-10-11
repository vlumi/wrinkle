// Gateways this browser has signed in through, most recent first, to pick
// from on the sign-in screen.

import { storage } from "./util";

const KEY = "gateways";
const MAX = 12;

/** The list with url moved to the front. */
export function withGateway(list: string[], url: string): string[] {
  return [url, ...list.filter((u) => u !== url)].slice(0, MAX);
}

/** A gateway URL as host and port, the scheme and path left out when they are the usual ones. */
export function gatewayLabel(url: string): string {
  const m = /^(wss?):\/\/([^/]+)(\/.*)?$/.exec(url);
  if (!m) return url;
  const [, scheme, host, path = ""] = m;
  return (scheme === "wss" ? "wss://" : "") + host + (path === "/v1/ws" ? "" : path);
}

export function loadGateways(): string[] {
  try {
    const list: unknown = JSON.parse(storage.get(KEY) ?? "[]");
    return Array.isArray(list) ? list.filter((u): u is string => typeof u === "string") : [];
  } catch {
    return [];
  }
}

export function rememberGateway(url: string): void {
  storage.set(KEY, JSON.stringify(withGateway(loadGateways(), url)));
}

export function forgetGateway(url: string): string[] {
  const list = loadGateways().filter((u) => u !== url);
  storage.set(KEY, JSON.stringify(list));
  return list;
}
