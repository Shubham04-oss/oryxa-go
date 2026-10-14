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

  // ---- calls and the agent's voice (edge/src/call, edge/src/voice) ----
  // Calls answer 503 until these are set (B7).
  // AI is Workers AI: the agent's text to speech.
  AI?: Ai;
  // REALTIMEKIT_ACCOUNT_ID and REALTIMEKIT_APP_ID name the RealtimeKit app;
  // REALTIMEKIT_API_TOKEN (a secret) is a Cloudflare API token with the
  // Realtime permission.
  REALTIMEKIT_ACCOUNT_ID?: string;
  REALTIMEKIT_APP_ID?: string;
  REALTIMEKIT_API_TOKEN?: string;
  // REALTIMEKIT_WEBHOOK_PUBLIC_KEY is RealtimeKit's webhook signing key
  // (PEM), from its .well-known/webhooks.json. Public, so a var.
  REALTIMEKIT_WEBHOOK_PUBLIC_KEY?: string;
  // GEMINI_API_KEY (a secret) and VOICE_MODEL: the fast voice model.
  GEMINI_API_KEY?: string;
  VOICE_MODEL?: string;
  VOICE_TTS_MODEL?: string;
  VOICE_TTS_SPEAKER?: string;
  // VOICE_AGENT_NAME is what the agent is called in calls ("Krude").
  VOICE_AGENT_NAME?: string;
  // AGENTS_URL is where dispatched work goes (https://api.oryxa.in), signed
  // with EDGE_SIGNING_KEY (a secret: the edge's P-256 private JWK, whose
  // public half agents holds in AGENTS_PRIMARY_EDGE_TOKEN_PUBLIC_KEYS).
  AGENTS_URL?: string;
  EDGE_SIGNING_KEY?: string;
}
