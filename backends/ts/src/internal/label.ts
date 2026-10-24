// Short texts people name things with: user names and group topics.

const control = /\p{Cc}/u;
const edgeSpace = /^\p{White_Space}|\p{White_Space}$/u;

/** Whether s is 1 to maxBytes bytes of UTF-8, without control characters or white space at either end. */
export function validLabel(s: string, maxBytes: number): boolean {
  if (s === "" || Buffer.byteLength(s, "utf8") > maxBytes || !s.isWellFormed()) return false;
  return !control.test(s) && !edgeSpace.test(s);
}
