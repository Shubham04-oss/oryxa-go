// A room's call (B1–B5, edge/src/call): one RealtimeKit meeting per room.
// A page reaches it through the Worker (lib/live), which names the member
// to the edge. The shapes are edge/src/call/contracts.ts's.
import { apiRequest } from "@/lib/api/client"
import { livePath } from "@/lib/live/client"
import type { LiveRoom } from "@/lib/live/client"

export type CallParticipant = { member: string; name: string; joinedAt: string }

export type RecordingState = {
  on: boolean
  /** Who turned it on and when: the consent banner says so. */
  by: string | null
  since: string | null
}

export type CallView = {
  callId: string
  startedAt: string
  startedBy: string
  participants: CallParticipant[]
  recording: RecordingState
  agent: { present: boolean; name: string }
}

export type JoinAnswer = {
  callId: string
  meetingId: string
  participantId: string
  /** The RealtimeKit participant token, revoked when the member leaves. */
  token: string
  preset: string
}

/** One piece of the member's own live transcript, as RealtimeKit heard it. */
export type TranscriptSegment = {
  id: string
  speaker: string
  text: string
  partial: boolean
  at: number
}

/** The edge takes at most this many segments in one request. */
export const MAX_SEGMENTS = 50

export function callKey(organizationId: string, room: LiveRoom) {
  return ["call", organizationId, room.kind, room.id] as const
}

export async function readCall(
  organizationId: string,
  room: LiveRoom,
  signal?: AbortSignal
): Promise<CallView | null> {
  const answer = await apiRequest<{ call: CallView | null }>(
    livePath(organizationId, room, "/call"),
    { signal }
  )
  return answer.call
}

/** Joins the room's call, starting it when there is none. */
export function joinCall(
  organizationId: string,
  room: LiveRoom,
  name?: string
): Promise<JoinAnswer> {
  return apiRequest<JoinAnswer>(livePath(organizationId, room, "/call/join"), {
    method: "POST",
    body: name ? { name } : {},
  })
}

/** Leaves; the last to leave ends the call. */
export function leaveCall(organizationId: string, room: LiveRoom) {
  return apiRequest<{ left: boolean }>(
    livePath(organizationId, room, "/call/leave"),
    { method: "POST", body: {} }
  )
}

/** Ends the call for everyone. */
export function endCall(organizationId: string, room: LiveRoom) {
  return apiRequest<{ ended: boolean }>(
    livePath(organizationId, room, "/call/end"),
    { method: "POST", body: {} }
  )
}

export function sendTranscript(
  organizationId: string,
  room: LiveRoom,
  callId: string,
  segments: TranscriptSegment[]
) {
  return apiRequest<{ accepted: number }>(
    livePath(organizationId, room, "/call/transcript"),
    { method: "POST", body: { callId, segments } }
  )
}

export function setRecording(
  organizationId: string,
  room: LiveRoom,
  on: boolean
) {
  return apiRequest<{ recording: RecordingState }>(
    livePath(organizationId, room, "/call/recording"),
    { method: "POST", body: { on } }
  )
}

/** Asks the agent into the call, or dismisses it. */
export function setAgentPresent(
  organizationId: string,
  room: LiveRoom,
  present: boolean
) {
  return apiRequest<{ call: CallView }>(
    livePath(organizationId, room, "/call/agent"),
    { method: "POST", body: { present } }
  )
}

/** The voice frames every client in the call plays (`/voice/live`). */
export type VoiceFrame =
  | { type: "voice.speaking"; utteranceId: string; text: string }
  | {
      type: "voice.audio"
      utteranceId: string
      seq: number
      format: "mp3"
      data: string
      last: boolean
    }
  | {
      type: "voice.stop"
      utteranceId: string
      reason: "barge_in" | "dismissed" | "call_ended"
    }
  | { type: "voice.state"; present: boolean; name: string }
  | { type: "voice.private"; dispatchId: string; text: string }

export function parseVoiceFrame(data: unknown): VoiceFrame | undefined {
  if (typeof data !== "string") return undefined
  try {
    const frame = JSON.parse(data) as { type?: unknown }
    return typeof frame.type === "string" && frame.type.startsWith("voice.")
      ? (frame as VoiceFrame)
      : undefined
  } catch {
    return undefined
  }
}

/** "18:42" or "1:02:05": how long the call has run. */
export function callDuration(startedAt: string, now: number): string {
  const seconds = Math.max(0, Math.floor((now - Date.parse(startedAt)) / 1000))
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = seconds % 60
  const pad = (value: number) => String(value).padStart(2, "0")
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`
}

/** "Raya is in", "Raya and Rohan are in", "Raya, Rohan and 3 others are in". */
export function whoIsIn(names: string[]): string {
  if (names.length === 0) return "Nobody is in yet"
  if (names.length === 1) return `${names[0]} is in`
  if (names.length === 2) return `${names[0]} and ${names[1]} are in`
  if (names.length === 3)
    return `${names[0]}, ${names[1]} and ${names[2]} are in`
  return `${names[0]}, ${names[1]} and ${names.length - 2} others are in`
}
