// Where the call gets RealtimeKit from. Production builds it from the
// Worker's settings; tests set a fake for the whole isolate, the way the
// sandbox and browser tests fake their container and session.
import type { Env } from "../env";
import { EdgeError } from "../http";
import { type CallProvider, RealtimeKit } from "./realtimekit";

let override: ((env: Env) => CallProvider | undefined) | undefined;

// setCallProvider replaces RealtimeKit for tests; undefined restores it.
export function setCallProvider(factory: ((env: Env) => CallProvider | undefined) | undefined): void {
  override = factory;
}

// callProvider returns RealtimeKit, or refuses when calls aren't set up here.
export function callProvider(env: Env): CallProvider {
  const provider = override ? override(env) : RealtimeKit.fromEnv(env);
  if (!provider) throw new EdgeError("unavailable", "calls are not set up here");
  return provider;
}
