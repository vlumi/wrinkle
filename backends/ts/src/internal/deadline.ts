// The deadline of the request being handled, carried through its async
// calls as Go carries a context, for waits that must not outlast it.
import { AsyncLocalStorage } from "node:async_hooks";

const current = new AsyncLocalStorage<AbortSignal>();

/** Runs fn with the signal as its request's deadline. */
export const withDeadline = <T>(signal: AbortSignal, fn: () => Promise<T>): Promise<T> => current.run(signal, fn);

/** The deadline of the request being handled, if any. */
export const deadline = (): AbortSignal | undefined => current.getStore();
