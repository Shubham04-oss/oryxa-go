// Where VoiceDO gets its model, its voice and its way to Go, and where a
// call's notes get theirs. Production
// builds them from the Worker's settings; tests set fakes for the isolate.
import type { Env } from "../env";
import { type NotesWriter, type VoiceBrain, Gemini } from "./brain";
import { type AgentsClient, Agents, type NotesClient } from "./dispatch";
import { type Speech, WorkersAISpeech } from "./speech";

export interface VoiceProviders {
  brain?: VoiceBrain;
  speech?: Speech;
  agents?: AgentsClient;
  // notesWriter summarizes a finished call; notes posts the summary to Go.
  notesWriter?: NotesWriter;
  notes?: NotesClient;
}

let override: ((env: Env) => VoiceProviders) | undefined;

// setVoiceProviders replaces the real ones for tests; undefined restores them.
export function setVoiceProviders(factory: ((env: Env) => VoiceProviders) | undefined): void {
  override = factory;
}

export function voiceProviders(env: Env): VoiceProviders {
  if (override) return override(env);
  const gemini = Gemini.fromEnv(env);
  const agents = Agents.fromEnv(env);
  return { brain: gemini, speech: WorkersAISpeech.fromEnv(env), agents, notesWriter: gemini, notes: agents };
}
