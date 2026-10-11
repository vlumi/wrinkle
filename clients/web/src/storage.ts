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
};
