// RealtimeKit's REST API (developers.cloudflare.com/api/resources/realtime_kit),
// the part calls use: a meeting per call, a participant per member who joins
// (its token is how their client joins), ending the session, and recording.
//
// A participant token lives 100 days and can't be shortened, so a member's
// participant is deleted when they leave and when the call ends: deleting it
// revokes every token it had.
import type { Env } from "../env";
import { EdgeError } from "../http";

export interface Meeting {
  id: string;
}

export interface Participant {
  id: string;
  token: string;
}

export interface Recording {
  id: string;
}

// CallProvider is what the call needs from RealtimeKit. Tests replace it
// (providers.ts).
export interface CallProvider {
  createMeeting(title: string): Promise<Meeting>;
  addParticipant(meetingId: string, participant: { member: string; name: string; preset: string }): Promise<Participant>;
  deleteParticipant(meetingId: string, participantId: string): Promise<void>;
  // endSession kicks everyone from the meeting's live session, which ends it
  // and stops any recording.
  endSession(meetingId: string): Promise<void>;
  startRecording(meetingId: string, prefix: string): Promise<Recording>;
  stopRecording(recordingId: string): Promise<void>;
}

const API = "https://api.cloudflare.com/client/v4/accounts";
const TIMEOUT_MS = 10_000;

// apiURL takes REALTIMEKIT_API_URL (a stand-in for RealtimeKit when testing
// locally, test/live/call/fakertk) only over https or to this machine: the
// API token goes with every request. Anything else is ignored.
export function apiURL(configured: string | undefined): string {
  if (!configured) return API;
  try {
    const url = new URL(configured);
    if (url.protocol === "https:") return configured;
    if (url.protocol === "http:" && ["127.0.0.1", "localhost", "[::1]"].includes(url.hostname)) return configured;
  } catch {
    // not a URL
  }
  return API;
}

interface Envelope<T> {
  success?: boolean;
  data?: T;
  errors?: { message?: string }[];
  // RealtimeKit's own API answers errors as { error: { message } } or
  // { error: "…" } rather than Cloudflare's errors[].
  error?: { message?: string } | string;
}

export class RealtimeKit implements CallProvider {
  private readonly base: string;

  constructor(
    accountId: string,
    appId: string,
    private readonly apiToken: string,
    // Workers refuse the global fetch called as another object's method, so
    // it is wrapped rather than stored as is.
    private readonly fetcher: typeof fetch = (input, init) => fetch(input, init),
    api: string = API,
  ) {
    this.base = `${api.replace(/\/+$/, "")}/${encodeURIComponent(accountId)}/realtime/kit/${encodeURIComponent(appId)}`;
  }

  // fromEnv returns the client, or undefined when calls aren't set up here.
  static fromEnv(env: Env): RealtimeKit | undefined {
    if (!env.REALTIMEKIT_ACCOUNT_ID || !env.REALTIMEKIT_APP_ID || !env.REALTIMEKIT_API_TOKEN) return undefined;
    return new RealtimeKit(env.REALTIMEKIT_ACCOUNT_ID, env.REALTIMEKIT_APP_ID, env.REALTIMEKIT_API_TOKEN, undefined, apiURL(env.REALTIMEKIT_API_URL));
  }

  async createMeeting(title: string): Promise<Meeting> {
    const data = await this.request<{ id: string }>("POST", "/meetings", {
      title,
      record_on_start: false,
      persist_chat: false,
      // Live per-speaker transcription, which the clients forward to the
      // room, and a Whisper transcript once the call ends. No language:
      // RealtimeKit refuses en-US for the Whisper transcript ("Invalid
      // language 'en-US' for post-meeting transcription") and detects it.
      ai_config: { transcription: { profanity_filter: false } },
      transcribe_on_end: true,
    });
    return { id: requireString(data.id, "meeting id") };
  }

  async addParticipant(meetingId: string, participant: { member: string; name: string; preset: string }): Promise<Participant> {
    const data = await this.request<{ id: string; token: string }>("POST", `/meetings/${encodeURIComponent(meetingId)}/participants`, {
      custom_participant_id: participant.member,
      preset_name: participant.preset,
      name: participant.name,
    });
    return { id: requireString(data.id, "participant id"), token: requireString(data.token, "participant token") };
  }

  async deleteParticipant(meetingId: string, participantId: string): Promise<void> {
    await this.request("DELETE", `/meetings/${encodeURIComponent(meetingId)}/participants/${encodeURIComponent(participantId)}`, undefined, [404]);
  }

  async endSession(meetingId: string): Promise<void> {
    // A meeting nobody is in has no active session: nothing to end.
    await this.request("POST", `/meetings/${encodeURIComponent(meetingId)}/active-session/kick-all`, undefined, [404]);
  }

  async startRecording(meetingId: string, prefix: string): Promise<Recording> {
    const data = await this.request<{ id: string }>("POST", "/recordings", {
      meeting_id: meetingId,
      file_name_prefix: prefix,
    });
    return { id: requireString(data.id, "recording id") };
  }

  async stopRecording(recordingId: string): Promise<void> {
    await this.request("PUT", `/recordings/${encodeURIComponent(recordingId)}`, { action: "stop" }, [404]);
  }

  // request sends one call and returns its data. A status in tolerated
  // counts as done with no data; RealtimeKit's own failure becomes an
  // upstream error, which is retryable when RealtimeKit said so with a 429
  // or a 5xx.
  private async request<T>(method: string, path: string, body?: unknown, tolerated: number[] = []): Promise<T> {
    let response: Response;
    try {
      response = await this.fetcher(this.base + path, {
        method,
        headers: { authorization: `Bearer ${this.apiToken}`, ...(body ? { "content-type": "application/json" } : {}) },
        body: body ? JSON.stringify(body) : undefined,
        signal: AbortSignal.timeout(TIMEOUT_MS),
      });
    } catch (error) {
      throw new EdgeError("unavailable", `RealtimeKit did not answer: ${String(error)}`, true);
    }
    if (tolerated.includes(response.status)) return {} as T;
    let envelope: Envelope<T> = {};
    const text = await response.text().catch(() => "");
    try {
      envelope = JSON.parse(text) as Envelope<T>;
    } catch {
      // An answer that isn't JSON is judged by its status, and its text.
    }
    if (!response.ok || envelope.success === false) {
      const message = refusal(envelope, text, response.status);
      const retryable = response.status === 429 || response.status >= 500;
      // The body says which field failed validation; it holds no credentials.
      console.warn(JSON.stringify({ msg: "realtimekit refused", method, route: route(path), status: response.status, message, body: text.slice(0, 1000) }));
      throw new EdgeError("unavailable", `RealtimeKit refused ${method}: ${message}`, retryable);
    }
    return (envelope.data ?? {}) as T;
  }
}

// refusal is RealtimeKit's reason for refusing, in whichever shape it came.
function refusal(envelope: Envelope<unknown>, text: string, status: number): string {
  const error = envelope.error;
  const reason = envelope.errors?.[0]?.message ?? (typeof error === "string" ? error : error?.message) ?? text.trim().slice(0, 300);
  return reason || `status ${status}`;
}

// route is a request's path with its ids left out, for the log.
function route(path: string): string {
  return path.replace(/\/meetings\/[^/]+/, "/meetings/{id}").replace(/\/(participants|recordings)\/[^/]+/, "/$1/{id}");
}

function requireString(value: unknown, what: string): string {
  if (typeof value !== "string" || value === "") throw new EdgeError("unavailable", `RealtimeKit returned no ${what}`);
  return value;
}
