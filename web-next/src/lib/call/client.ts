// A room's call (B1–B5, edge/src/call): one RealtimeKit meeting per room.
// A page reaches it through the Worker (lib/live), which names the member
// to the edge. The shapes are edge/src/call/contracts.ts's.
import { liveRequest } from "@/lib/live/client"
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
  agent: {
    present: boolean
    name: string
    /** Every name it answers to, its own first (older edges send none). */
    names?: string[]
    /** False while it has no voice model or speech here. */
    canAnswer?: boolean
    /** Whether the call leaves notes in the room when it ends. */
    notes?: boolean
  }
}

/**
 * The names to tell members to say, room agents' first: they are the names
 * the room shows. Each is its first word ("Arlo" for "Arlo-UJVX").
 */
export function wakeNames(agent: CallView["agent"], max = 2): string[] {
  const names = agent.names?.length ? agent.names : [agent.name]
  const ordered = [...names.slice(1), names[0] ?? agent.name]
  const words = ordered
    .map((name) => name.split(/[\s\-_]+/)[0] ?? "")
    .filter(Boolean)
  return [...new Set(words)].slice(0, max)
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
  const answer = await liveRequest<{ call: CallView | null }>(
    organizationId,
    room,
    "/call",
    { signal }
  )
  return answer.call
}

/**
 * Joins the room's call, starting it when there is none. `agents` are the
 * names the room shows for its agents: the agent in the call answers to them.
 */
export function joinCall(
  organizationId: string,
  room: LiveRoom,
  name?: string,
  agents?: string[]
): Promise<JoinAnswer> {
  const named = (agents ?? [])
    .map((agent) => agent.trim().slice(0, 60))
    .filter(Boolean)
    .slice(0, 20)
  return liveRequest<JoinAnswer>(organizationId, room, "/call/join", {
    method: "POST",
    body: {
      ...(name ? { name } : {}),
      ...(named.length ? { agents: named } : {}),
    },
  })
}

/** Leaves; the last to leave ends the call. */
export function leaveCall(organizationId: string, room: LiveRoom) {
  return liveRequest<{ left: boolean }>(organizationId, room, "/call/leave", {
    method: "POST",
    body: {},
  })
}

/** Ends the call for everyone. */
export function endCall(organizationId: string, room: LiveRoom) {
  return liveRequest<{ ended: boolean }>(organizationId, room, "/call/end", {
    method: "POST",
    body: {},
  })
}

export function sendTranscript(
  organizationId: string,
  room: LiveRoom,
  callId: string,
  segments: TranscriptSegment[]
) {
  return liveRequest<{ accepted: number }>(
    organizationId,
    room,
    "/call/transcript",
    { method: "POST", body: { callId, segments } }
  )
}

export function setRecording(
  organizationId: string,
  room: LiveRoom,
  on: boolean
) {
  return liveRequest<{ recording: RecordingState }>(
    organizationId,
    room,
    "/call/recording",
    { method: "POST", body: { on } }
  )
}

/** Asks the agent into the call, or dismisses it. */
export function setAgentPresent(
  organizationId: string,
  room: LiveRoom,
  present: boolean
) {
  return liveRequest<{ call: CallView }>(organizationId, room, "/call/agent", {
    method: "POST",
    body: { present },
  })
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
  /** What the agent heard the member say to it. */
  | { type: "voice.heard"; text: string }
  /** Why the agent stayed quiet after the member spoke to it. */
  | { type: "voice.trouble"; reason: VoiceTrouble }

export type VoiceTrouble = "no_model" | "model_failed" | "no_speech"

const VOICE_FRAMES = new Set([
  "voice.speaking",
  "voice.audio",
  "voice.stop",
  "voice.state",
  "voice.private",
  "voice.heard",
  "voice.trouble",
])

export function parseVoiceFrame(data: unknown): VoiceFrame | undefined {
  if (typeof data !== "string") return undefined
  try {
    const frame = JSON.parse(data) as { type?: unknown }
    // Only the frames this page knows: a newer one is not mistaken for any.
    return typeof frame.type === "string" && VOICE_FRAMES.has(frame.type)
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
