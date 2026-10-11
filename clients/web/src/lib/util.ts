/** The gateway, or the load balancer in front of several, on the host that served this page. */
export function defaultGatewayURL(loc: Pick<Location, "protocol" | "hostname"> = location): string {
  const scheme = loc.protocol === "https:" ? "wss" : "ws";
  return `${scheme}://${loc.hostname || "localhost"}:8080/v1/ws`;
}

/**
 * 128 random bits in hex, for client message ids. crypto.randomUUID exists
 * only on secure pages (HTTPS or localhost), and on a LAN the page is plain HTTP.
 */
export function newMessageId(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

/** Per-user persistent settings; localStorage may be unavailable. */
export const storage = {
  get(key: string): string | null {
    try {
      return localStorage.getItem(`wrinkle.${key}`);
    } catch {
      return null;
    }
  },
  set(key: string, value: string): void {
    try {
      localStorage.setItem(`wrinkle.${key}`, value);
    } catch {
      // Private mode: the session is not remembered.
    }
  },
  remove(key: string): void {
    try {
      localStorage.removeItem(`wrinkle.${key}`);
    } catch {
      // As above.
    }
  },
};
