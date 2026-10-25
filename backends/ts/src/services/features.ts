// The features whose handlers make up the in-process services. Each feature
// module exports a function from the shared dependencies to its handlers.
import type { Deps } from "./deps.ts";
import type { Handlers, SignInHandlers } from "./backend.ts";
import { Local } from "./local.ts";

export interface Feature {
  handlers?: Handlers;
  signIn?: SignInHandlers;
  ack?: (userId: Buffer, deviceId: bigint, cursor: bigint) => Promise<void>;
  /** Background work of the services role (sweepers); returns a stop function. */
  start?: () => () => void | Promise<void>;
}

export type FeatureFactory = (deps: Deps) => Feature;

export function newLocal(deps: Deps, factories: FeatureFactory[]): { local: Local; features: Feature[] } {
  const features = factories.map((f) => f(deps));
  const handlers: Handlers = {};
  const signIn: SignInHandlers = {};
  let ack: Feature["ack"];
  for (const f of features) {
    Object.assign(handlers, f.handlers);
    Object.assign(signIn, f.signIn);
    ack ??= f.ack;
  }
  const local = new Local(handlers, signIn, ack ?? (() => Promise.resolve()));
  return { local, features };
}
