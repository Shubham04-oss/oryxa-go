import type { BrowserDO } from "./browser/browser";
import type { RoomDO } from "./room/room";
import type { SandboxDO } from "./sandbox/sandbox";
import type { VoiceDO } from "./voice/voice";

export interface Env {
  ROOM: DurableObjectNamespace<RoomDO>;
  SANDBOX: DurableObjectNamespace<SandboxDO>;
  BROWSER_SESSION: DurableObjectNamespace<BrowserDO>;
  VOICE: DurableObjectNamespace<VoiceDO>;
  // RUNS is the oryxa-runs bucket (oryxa-runs-staging on staging).
  RUNS: R2Bucket;
  // AGENTS_TOKEN_PUBLIC_KEYS is a JSON array of base64 DER SPKI keys that
  // may sign edge tokens: agents' serviceauth KMS key, and its successor
  // during a rotation.
  AGENTS_TOKEN_PUBLIC_KEYS: string;
  ENVIRONMENT: string;
  // BROWSER is the Browser Run binding.
  BROWSER: Fetcher;
  // EDGE_URL_SIGNING_KEY signs preview and terminal URLs (a secret, 32+
  // characters); EDGE_PUBLIC_URL is where those URLs point.
  EDGE_URL_SIGNING_KEY: string;
  EDGE_PUBLIC_URL: string;
  // SANDBOX_INSTANCE is the sandbox containers' instance type.
  SANDBOX_INSTANCE?: string;
}
