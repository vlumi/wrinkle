/** Persistent settings under wrinkle.*; localStorage may be unavailable, as in private mode, and then nothing is remembered. */
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
      // Not remembered.
    }
  },
  remove(key: string): void {
    try {
      localStorage.removeItem(`wrinkle.${key}`);
    } catch {
      // Nothing was remembered.
    }
  },
  /** Removes every setting but those kept. */
  clear(keep: string[]): void {
    try {
      const keys = Array.from({ length: localStorage.length }, (_, i) => localStorage.key(i) ?? "");
      for (const k of keys) {
        if (k.startsWith("wrinkle.") && !keep.includes(k.slice("wrinkle.".length))) localStorage.removeItem(k);
      }
    } catch {
      // Nothing was remembered.
    }
  },
};
