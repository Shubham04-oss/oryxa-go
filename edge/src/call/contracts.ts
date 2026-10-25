// The call API (docs/design/live-layer/03, B1–B5) and the voice frames the
// call client plays. Members reach it through WebEdge at
// /v1/rooms/{kind}/{id}/call…; Go reaches /voice/result with an edge token.
//
// The agent is a virtual participant (the [decision] on discussion #217):
// it never joins the RealtimeKit meeting. It hears through the transcript
// segments each member's client forwards for its own speaker, and speaks
// through voice.* frames on /voice/live, which every client plays.
import { z } from "zod";
import { Id, Room } from "../contracts";

// ---- presets ----

// The RealtimeKit presets B7 sets up. Guests are out of this release; the
// name is reserved so a token is never issued for it by accident.
export const PRESET_MEMBER = "oryxa-member";
export const PRESET_GUEST = "oryxa-guest";

// ---- requests from members ----

export const MAX_DISPLAY_NAME = 80;

export const MAX_AGENT_NAME = 60;

export const JoinRequest = z.strictObject({
  // name is what the call shows for the member; web-next knows it.
  name: z.string().trim().min(1).max(MAX_DISPLAY_NAME).optional(),
  // agents are the names the room shows for its agents, which the agent in
  // the call answers to as well as its own (T3). They only decide when it
  // listens, so a member's word for them is enough.
  agents: z.array(z.string().trim().min(1).max(MAX_AGENT_NAME)).max(20).optional(),
});
export type JoinRequest = z.infer<typeof JoinRequest>;

export const JoinResponse = z.strictObject({
  callId: Id,
  meetingId: z.string(),
  participantId: z.string(),
  // token is the RealtimeKit participant token the client SDK joins with. It
  // is revoked when the member leaves and when the call ends.
  token: z.string(),
  preset: z.string(),
});
export type JoinResponse = z.infer<typeof JoinResponse>;

export const MAX_SEGMENT_CHARS = 2_000;
export const MAX_SEGMENTS = 50;

// TranscriptSegment is one piece of RealtimeKit's live transcript, as the
// client SDK delivers it (meeting.ai.on("transcript")). A client forwards
// only its own speaker's: speaker must be the member sending it.
export const TranscriptSegment = z.strictObject({
  id: z.string().min(1).max(128),
  speaker: z.string().min(1).max(128),
  text: z.string().max(MAX_SEGMENT_CHARS),
  partial: z.boolean(),
  // at is the segment's own time, in Unix milliseconds.
  at: z.number().int().nonnegative(),
});
export type TranscriptSegment = z.infer<typeof TranscriptSegment>;

export const TranscriptRequest = z.strictObject({
  callId: Id,
  segments: z.array(TranscriptSegment).min(1).max(MAX_SEGMENTS),
});
export type TranscriptRequest = z.infer<typeof TranscriptRequest>;

export const RecordingRequest = z.strictObject({ on: z.boolean() });
export type RecordingRequest = z.infer<typeof RecordingRequest>;

export const AgentRequest = z.strictObject({ present: z.boolean() });
export type AgentRequest = z.infer<typeof AgentRequest>;

// ---- what a member reads ----

export const CallParticipant = z.strictObject({
  member: z.string(),
  name: z.string(),
  joinedAt: z.string(),
});

export const RecordingState = z.strictObject({
  on: z.boolean(),
  // by and since say who turned it on and when: the consent banner shows them.
  by: z.string().nullable(),
  since: z.string().nullable(),
});
export type RecordingState = z.infer<typeof RecordingState>;

export const CallView = z.strictObject({
  callId: Id,
  startedAt: z.string(),
  startedBy: z.string(),
  participants: z.array(CallParticipant),
  recording: RecordingState,
  agent: z.strictObject({
    present: z.boolean(),
    name: z.string(),
    // names is every name it answers to, its own first.
    names: z.array(z.string()),
    // canAnswer is false while the voice model or speech isn't set up here.
    canAnswer: z.boolean(),
    // notes is whether the call leaves notes in the room when it ends.
    notes: z.boolean(),
  }),
});
export type CallView = z.infer<typeof CallView>;

export const CallResponse = z.strictObject({ call: CallView.nullable() });
export type CallResponse = z.infer<typeof CallResponse>;

// ---- edge ↔ agents (contract §4) ----

export const MAX_REQUEST_CHARS = 4_000;
export const MAX_CONTEXT_LINES = 40;
export const MAX_CONTEXT_LINE_CHARS = 1_000;

// DispatchRequest is what VoiceDO sends POST /v1/edge/dispatch; Go's
// edgedispatch.Request is its twin.
export const DispatchRequest = z.strictObject({
  dispatchId: Id,
  org: Id,
  team: z.string().optional(),
  room: Room,
  call: Id,
  member: Id,
  // said is the member's own words; request is the voice model's
  // paraphrase, which may draw on what others said.
  said: z.string().min(1).max(MAX_REQUEST_CHARS),
  request: z.string().min(1).max(MAX_REQUEST_CHARS),
  transcriptContext: z.array(z.string().max(MAX_CONTEXT_LINE_CHARS)).max(MAX_CONTEXT_LINES),
});
export type DispatchRequest = z.infer<typeof DispatchRequest>;

export const DispatchAccepted = z.object({ dispatchId: Id, threadId: z.string(), runId: z.string() });
export type DispatchAccepted = z.infer<typeof DispatchAccepted>;

export const DISPATCH_STATUSES = ["completed", "failed", "interrupted"] as const;

// DispatchResult is what Go posts to /voice/result when the run ends.
export const DispatchResult = z.strictObject({
  dispatchId: Id,
  org: Id,
  team: z.string().optional(),
  room: Room,
  call: Id,
  member: Id,
  threadId: z.string(),
  runId: z.string(),
  status: z.enum(DISPATCH_STATUSES),
  text: z.string().max(8_000).optional(),
  error: z.string().max(1_000).optional(),
});
export type DispatchResult = z.infer<typeof DispatchResult>;

// ---- voice frames on /voice/live ----

// A spoken answer is a run of voice.audio frames for one utterance, in seq
// order, each one sentence of MP3. voice.stop tells every client to stop
// playing an utterance now (a person spoke over the agent).
export type VoiceFrame =
  | { type: "voice.speaking"; utteranceId: string; text: string }
  | { type: "voice.audio"; utteranceId: string; seq: number; format: "mp3"; data: string; last: boolean }
  | { type: "voice.stop"; utteranceId: string; reason: "barge_in" | "dismissed" | "call_ended" }
  | { type: "voice.state"; present: boolean; name: string }
  // voice.heard tells the member who addressed the agent what it heard, so
  // a miss is visible (T3).
  | { type: "voice.heard"; text: string }
  // voice.trouble says the agent heard and could not answer: no model, or
  // the model failed (T2). The member sees why it stayed quiet.
  | { type: "voice.trouble"; reason: "no_model" | "model_failed" | "no_speech" }
  // voice.private is an answer for one member only, sent to their own
  // sockets: the work agent used their access to find it.
  | { type: "voice.private"; dispatchId: string; text: string };

// ---- room log event types ----

export const CALL_EVENTS = {
  started: "call.started",
  joined: "call.joined",
  left: "call.left",
  ended: "call.ended",
  recordingStarted: "call.recording.started",
  recordingStopped: "call.recording.stopped",
  recordingSaved: "call.recording.saved",
  transcriptSaved: "call.transcript.saved",
  segment: "transcript.segment",
  agentJoined: "call.agent.joined",
  agentLeft: "call.agent.left",
  dispatched: "voice.dispatch",
  result: "voice.result",
  // A teamspace call's notes: posted in the room by Go, given up on, or not
  // made because nothing said in the call reached the room.
  notesPosted: "call.notes.posted",
  notesFailed: "call.notes.failed",
  notesSkipped: "call.notes.skipped",
} as const;
