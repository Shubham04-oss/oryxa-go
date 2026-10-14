// A room on the edge, as a page reaches it (F5): through the Worker at
// /live/{org}/rooms/{kind}/{id}… (src/edge-proxy.ts), which names the member
// to the edge. The shapes are the edge's (edge/src/contracts).
import { ApiError, apiRequest } from "@/lib/api/client"

export type RoomKind = "thread" | "teamspace" | "dm" | "side"
export type LiveRoom = { kind: RoomKind; id: string }

export type LiveActor = {
  type: "member" | "agent" | "system"
  id: string
  onBehalfOf?: string
}

/** One event in a room's log. */
export type LiveEvent = {
  seq: number
  at: string
  org: string
  room: LiveRoom
  actor: LiveActor
  run: string | null
  type: string
  summary: string
  data: Record<string, unknown>
  durationMs: number
  cost?: { unit: string; amount: number; model?: string }
  private: boolean
}

export type LogPage = { events: LiveEvent[]; next: number; more: boolean }

/** The path of `rest` in `room`, on the app's own origin. */
export function livePath(
  organizationId: string,
  room: LiveRoom,
  rest: string
): string {
  return `/live/${encodeURIComponent(organizationId)}/rooms/${room.kind}/${encodeURIComponent(room.id)}${rest}`
}

type LiveOptions = Parameters<typeof apiRequest>[1]

// Rooms Go has admitted the member to on the edge, this page: each is asked
// once (A18), and forgotten when the edge refuses the member anyway.
const admissions = new Map<string, Promise<void>>()

function admissionKey(organizationId: string, room: LiveRoom) {
  return `${organizationId}/${room.kind}/${room.id}`
}

/**
 * Asks Go to let the member into `room` on the edge before the page opens it
 * there (`POST …/edge/rooms/{kind}/{id}/admit`, A18): Go checks they may be
 * in it the way its own routes do, then tells the edge. 204 is in; 404 is a
 * room that isn't theirs. Either way the edge has the last word (its 403 or
 * 404 reaches the page), so a refusal here, or a server without the route,
 * stops nothing.
 */
export function admit(organizationId: string, room: LiveRoom): Promise<void> {
  const key = admissionKey(organizationId, room)
  let admission = admissions.get(key)
  if (!admission) {
    admission = apiRequest(
      `/v1/organizations/${encodeURIComponent(organizationId)}/edge/rooms/${room.kind}/${encodeURIComponent(room.id)}/admit`,
      { method: "POST" }
    ).catch(() => {
      // Not admitted, or no admit route yet: the edge decides.
      admissions.delete(key)
    })
    admissions.set(key, admission)
  }
  return admission
}

/** Forgets every admission; for tests. */
export function forgetAdmissions() {
  admissions.clear()
}

/**
 * A request to `rest` in `room`, once the member has been admitted to it.
 * The edge refusing the member (403) forgets the admission, so the next
 * request asks Go again: their access may have changed.
 */
export async function liveRequest<T = void>(
  organizationId: string,
  room: LiveRoom,
  rest: string,
  options?: LiveOptions
): Promise<T> {
  await admit(organizationId, room)
  try {
    return await apiRequest<T>(livePath(organizationId, room, rest), options)
  } catch (error) {
    if (error instanceof ApiError && error.status === 403) {
      admissions.delete(admissionKey(organizationId, room))
    }
    throw error
  }
}

/** The room's events after `after`, a page at a time. */
export function readLog(
  organizationId: string,
  room: LiveRoom,
  { after = 0, limit }: { after?: number; limit?: number } = {},
  signal?: AbortSignal
): Promise<LogPage> {
  const query = new URLSearchParams({ after: String(after) })
  if (limit !== undefined) query.set("limit", String(limit))
  return liveRequest<LogPage>(
    organizationId,
    room,
    `/log?${query.toString()}`,
    { signal }
  )
}

/** The WebSocket URL of `rest` in `room` (`/live` or `/voice/live`). */
export function liveSocketUrl(
  organizationId: string,
  room: LiveRoom,
  rest: "/live" | "/voice/live",
  origin = window.location.origin
): string {
  const url = new URL(livePath(organizationId, room, rest), origin)
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:"
  return url.toString()
}

/** A live socket's frame: an event appended to the room's log. */
export function parseLiveFrame(data: unknown): LiveEvent | undefined {
  if (typeof data !== "string" || data === "pong") return undefined
  try {
    const frame = JSON.parse(data) as { type?: unknown; event?: unknown }
    if (frame.type !== "event") return undefined
    const event = frame.event as Partial<LiveEvent> | null
    if (typeof event?.seq !== "number" || typeof event.type !== "string") {
      return undefined
    }
    return event as LiveEvent
  } catch {
    return undefined
  }
}
