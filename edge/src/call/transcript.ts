// The shared transcript (B2). RealtimeKit transcribes each speaker live but
// delivers the text only to the call's clients, so each member's client
// forwards its own speaker's segments here. The room takes a segment only
// from the member who said it: nobody can put words in someone else's mouth.
//
// Final segments become transcript.segment events in the room log, and the
// call's transcript is written to R2 when it ends. RealtimeKit's own
// post-call transcript (Whisper) follows by webhook and is kept beside it.
import { EdgeError } from "../http";
import { CALL_EVENTS, type TranscriptRequest } from "./contracts";
import { type CallRoom, callPrefix, type RoomIdentity } from "./index";
import { copyToR2 } from "./r2copy";
import type { CallRow, CallStore } from "./store";
import type { RealtimeKitEvent } from "./webhook";

// AGENT_SPEAKER is the agent's speaker label in the transcript.
export const AGENT_SPEAKER = "agent";

// Heard is what VoiceDO is told of a segment: final ones to follow the
// conversation, partial ones to stop talking when someone talks over it.
export interface Heard {
  id: string;
  speaker: string;
  name: string;
  text: string;
  partial: boolean;
  at: number;
}

// acceptTranscript checks and keeps a member's segments, and returns how
// many final ones were new.
export function acceptTranscript(
  ctx: CallRoom,
  store: CallStore,
  _identity: RoomIdentity,
  member: string,
  body: TranscriptRequest,
  hear: (segments: Heard[]) => void,
): number {
  const call = store.active();
  if (!call || call.id !== body.callId) throw new EdgeError("conflict", "that call is not going on in this room");
  const participant = store.participant(call.id, member);
  if (!participant || participant.left_at !== null) throw new EdgeError("forbidden", "join the call first");
  if (body.segments.some((segment) => segment.speaker !== member)) {
    throw new EdgeError("forbidden", "a client forwards only its own speaker's transcript");
  }

  let accepted = 0;
  const heard: Heard[] = [];
  for (const segment of body.segments) {
    const text = segment.text.trim();
    if (text === "") continue;
    heard.push({ id: segment.id, speaker: member, name: participant.name, text, partial: segment.partial, at: segment.at });
    if (segment.partial) continue;
    if (!store.addSegment({ call_id: call.id, segment_id: segment.id, speaker: member, text, at: segment.at })) continue;
    accepted++;
    ctx.room.append({
      actor: { type: "member", id: member },
      run: null,
      type: CALL_EVENTS.segment,
      summary: `${participant.name}: ${text}`,
      data: { callId: call.id, segmentId: segment.id, speaker: member, name: participant.name, text, at: segment.at },
    });
  }
  if (heard.length > 0) hear(heard);
  return accepted;
}

// recordAgentSegment keeps what the agent said in the same transcript.
export function recordAgentSegment(ctx: CallRoom, store: CallStore, callId: string, segmentId: string, text: string, at: number, name: string): void {
  const call = store.byId(callId);
  if (!call || call.ended_at !== null) return;
  if (!store.addSegment({ call_id: callId, segment_id: segmentId, speaker: AGENT_SPEAKER, text, at })) return;
  ctx.room.append({
    actor: { type: "agent", id: "voice" },
    run: null,
    type: CALL_EVENTS.segment,
    summary: `${name}: ${text}`,
    data: { callId, segmentId, speaker: AGENT_SPEAKER, name, text, at },
  });
}

// saveLiveTranscript writes the call's transcript to R2 as one speaker-labelled
// line per segment, then lets the room forget it.
export async function saveLiveTranscript(ctx: CallRoom, store: CallStore, identity: RoomIdentity, callId: string): Promise<void> {
  const segments = store.segments(callId);
  const key = `${callPrefix(identity, callId)}transcript.ndjson`;
  try {
    const body = segments.map((segment) => JSON.stringify({ id: segment.segment_id, speaker: segment.speaker, text: segment.text, at: segment.at })).join("\n");
    await ctx.env.RUNS.put(key, body === "" ? "" : `${body}\n`, {
      httpMetadata: { contentType: "application/x-ndjson" },
      customMetadata: { org: identity.org },
    });
  } catch (error) {
    // Kept in the room until a later save works: the transcript is not lost.
    console.error(JSON.stringify({ msg: "save a call transcript to R2", callId, error: String(error) }));
    return;
  }
  store.dropSegments(callId);
  ctx.room.append({
    actor: { type: "system", id: "edge" },
    run: null,
    type: CALL_EVENTS.transcriptSaved,
    summary: `The call's transcript was saved (${segments.length} segments)`,
    data: { callId, key, segments: segments.length, source: "live" },
  });
}

// saveFinalTranscript keeps RealtimeKit's post-call transcript beside the
// live one.
export async function saveFinalTranscript(ctx: CallRoom, identity: RoomIdentity, call: CallRow, event: RealtimeKitEvent): Promise<void> {
  if (!event.transcriptDownloadUrl) return;
  const key = `${callPrefix(identity, call.id)}transcript.realtimekit`;
  try {
    const size = await copyToR2(ctx.env, event.transcriptDownloadUrl, key, identity.org, "text/csv");
    ctx.room.append({
      actor: { type: "system", id: "realtimekit" },
      run: null,
      type: CALL_EVENTS.transcriptSaved,
      summary: "RealtimeKit's transcript of the call was saved",
      data: { callId: call.id, key, size, source: "realtimekit" },
    });
  } catch (error) {
    console.error(JSON.stringify({ msg: "save RealtimeKit's transcript", callId: call.id, error: String(error) }));
    throw error;
  }
}
