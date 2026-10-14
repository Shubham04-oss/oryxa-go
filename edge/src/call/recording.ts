// Recording (B3): off by default, turned on per call by a member who is in
// it (the owner's decision of 2026-10-14). While it is on, the call's view
// carries who turned it on and since when, which the consent banner shows to
// everyone, and the room log records each switch. The file RealtimeKit makes
// is copied into R2 under the call when its webhook says it is uploaded.
//
// The agent is not a meeting participant, so its voice is not in the
// recording; what it said is in the transcript.
import { CALL_EVENTS, type RecordingState } from "./contracts";
import { type CallRoom, callPrefix, meetingTitle, type RoomIdentity } from "./index";
import { callProvider } from "./providers";
import { copyToR2 } from "./r2copy";
import type { CallRow, CallStore } from "./store";
import { type RealtimeKitEvent, recordingPrefix } from "./webhook";

export async function switchRecording(
  ctx: CallRoom,
  store: CallStore,
  identity: RoomIdentity,
  callId: string,
  member: string,
  on: boolean,
): Promise<RecordingState> {
  const call = store.byId(callId);
  if (!call || call.ended_at !== null) return { on: false, by: null, since: null };
  const current = store.recording(call);
  if (current.on === on) return current;
  const provider = callProvider(ctx.env);
  const name = store.participant(callId, member)?.name ?? member;
  if (on) {
    const recording = await provider.startRecording(call.meeting_id, recordingPrefix(meetingTitle(identity, callId)));
    const since = new Date().toISOString();
    store.setRecording(callId, recording.id, member, since);
    ctx.room.append({
      actor: { type: "member", id: member },
      run: null,
      type: CALL_EVENTS.recordingStarted,
      summary: `${name} started recording the call`,
      data: { callId, recordingId: recording.id },
    });
  } else {
    await provider.stopRecording(call.recording_id!);
    store.setRecording(callId, null, null, null);
    ctx.room.append({
      actor: { type: "member", id: member },
      run: null,
      type: CALL_EVENTS.recordingStopped,
      summary: `${name} stopped recording the call`,
      data: { callId, recordingId: call.recording_id },
    });
  }
  return store.recording(store.byId(callId)!);
}

// saveRecording copies a finished recording into R2.
export async function saveRecording(ctx: CallRoom, _store: CallStore, identity: RoomIdentity, call: CallRow, event: RealtimeKitEvent): Promise<void> {
  const recording = event.recording;
  if (!recording || recording.status !== "UPLOADED" || !recording.downloadUrl) return;
  const id = String(recording.id ?? recording.recordingId ?? "recording").replace(/[^A-Za-z0-9_-]/g, "");
  const key = `${callPrefix(identity, call.id)}recording/${id}.mp4`;
  const size = await copyToR2(ctx.env, recording.downloadUrl, key, identity.org, "video/mp4");
  ctx.room.append({
    actor: { type: "system", id: "realtimekit" },
    run: null,
    type: CALL_EVENTS.recordingSaved,
    summary: "The call's recording was saved",
    data: { callId: call.id, key, size, durationSeconds: recording.recordingDuration ?? null },
  });
}
