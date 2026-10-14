// Where VoiceDO gets its model, its voice and its way to Go. Production
// builds them from the Worker's settings; tests set fakes for the isolate.
import type { Env } from "../env";
import { type VoiceBrain, Gemini } from "./brain";
import { type AgentsClient, Agents } from "./dispatch";
import { type Speech, WorkersAISpeech } from "./speech";

export interface VoiceProviders {
  brain?: VoiceBrain;
  speech?: Speech;
  agents?: AgentsClient;
}

let override: ((env: Env) => VoiceProviders) | undefined;

// setVoiceProviders replaces the real ones for tests; undefined restores them.
export function setVoiceProviders(factory: ((env: Env) => VoiceProviders) | undefined): void {
  override = factory;
}

export function voiceProviders(env: Env): VoiceProviders {
  if (override) return override(env);
  return { brain: Gemini.fromEnv(env), speech: WorkersAISpeech.fromEnv(env), agents: Agents.fromEnv(env) };
}
