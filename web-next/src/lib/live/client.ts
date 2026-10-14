// A room on the edge, as a page reaches it (F5): through the Worker at
// /live/{org}/rooms/{kind}/{id}… (src/edge-proxy.ts), which names the member
// to the edge. The shapes are the edge's (edge/src/contracts).
import { apiRequest } from "@/lib/api/client"

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

/** The room's events after `after`, a page at a time. */
export function readLog(
  organizationId: string,
  room: LiveRoom,
  { after = 0, limit }: { after?: number; limit?: number } = {},
  signal?: AbortSignal
): Promise<LogPage> {
  const query = new URLSearchParams({ after: String(after) })
  if (limit !== undefined) query.set("limit", String(limit))
  return apiRequest<LogPage>(
    `${livePath(organizationId, room, "/log")}?${query.toString()}`,
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
