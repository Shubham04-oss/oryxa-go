// What the teamspace room's Durable Object (src/teamspace-room-agent.ts) and
// the page's useTeamspaceRoom share: the object's name, its synced state, and
// what travels on its socket. REALTIME.md says what the object does.
import type { RoomMessage, RoomSeat } from "./client"

/** The object's class and binding, useAgent's `agent`: the Worker serves it at `/agents/teamspace-room-agent/{name}`. */
export const TEAMSPACE_ROOM_AGENT = "TeamspaceRoomAgent"

// The Worker routes a name only when both ids are made of these characters,
// so a name always splits back into the same two ids.
const agentNameId = /^[\w-]+$/

/** The room's Durable Object name: one object per teamspace, `{organization}~{teamspace}`. */
export function teamspaceRoomAgentName(
  organizationId: string,
  teamspaceId: string
): string {
  return `${organizationId}~${teamspaceId}`
}

/**
 * The ids a Durable Object name was made from, or nothing for a name
 * `teamspaceRoomAgentName` could not have made, as the Worker checks it
 * before routing (src/server.ts).
 */
export function parseTeamspaceRoomAgentName(
  name: string
): { organizationId: string; teamspaceId: string } | undefined {
  const parts = name.split("~")
  if (parts.length !== 2) return undefined
  const [organizationId, teamspaceId] = parts
  if (!agentNameId.test(organizationId) || !agentNameId.test(teamspaceId)) {
    return undefined
  }
  return { organizationId, teamspaceId }
}

/**
 * The object's synced state: every socket is sent it (useAgent's `state`),
 * and only the object changes it.
 */
export type TeamspaceRoomState = {
  /**
   * Go's seats as of the object's latest read: who has a seat, and whom Go
   * counts as present (read in the last 90 seconds).
   */
  seats: RoomSeat[]
  /**
   * The people who have the room open in a visible tab right now, by user
   * id. It changes the moment they arrive, where Go's `present` waits for a
   * read, and it is what the object reads Go for.
   */
  here: string[]
}

export const emptyRoomState: TeamspaceRoomState = { seats: [], here: [] }

/** The most messages the object keeps, and sends a socket as it opens: one read's page. */
export const ROOM_WINDOW = 200

/** The most messages a page keeps, newest last. */
export const ROOM_HISTORY_LIMIT = 1_000

/** The close code of a socket Go no longer lets in (403 or 404). Terminal: the page does not reopen it. */
export const roomGoneCloseCode = 4404

/** What a page says on the socket. */
export type RoomClientMessage =
  /**
   * Whether the page's tab is visible: first on every socket, then on every
   * change. The object reads Go as a member, which is what keeps them present
   * and their agent quiet, only while one of their tabs is visible.
   */
  | { type: "room-visibility"; visible: boolean }
  /**
   * Says `body` in the room as the socket's member. `id` is the page's own,
   * for the answer: the author is always whoever the socket's cookie is.
   */
  | { type: "room-post"; id: string; body: string }

/** What became of a post, as the object tells its author's sockets. */
export type RoomPostResult =
  /** Go's 201: the member's message, then whatever the room's agents answered. */
  | { id: string; ok: true; messages: RoomMessage[] }
  /**
   * Go's refusal with its status, code and words; status 0 when Go could not
   * be reached or never answered. `session_expired` (`sessionExpiredCode`)
   * for a 401: nothing was said, and the page may say it again once its
   * socket carries a fresh cookie.
   */
  | { id: string; ok: false; status: number; code?: string; message: string }

/** Where the room stands for a socket, whenever that changes. */
export type RoomSocketStatus =
  /** Go answers reads again. */
  | "live"
  /** Go could not be read. The object keeps trying. */
  | "error"
  /** 400: the teamspace is paused. Nothing is read or said until it is resumed. */
  | "paused"
  /** 503 `rooms_unavailable`: this server does not run rooms. */
  | "unavailable"
  /** 403 or 404 for this member: the socket is closed next, with `roomGoneCloseCode`. */
  | "gone"
  /** 401 for this socket's cookie: the object will not use it again. Reopen the socket with a fresh one. */
  | "session_expired"

/** What the object says on a socket. */
export type RoomServerMessage =
  /**
   * The room as the socket opens, once Go has been read: the object's recent
   * messages (more may follow as `room-messages`), and what became of this
   * member's posts, still out (`pending`) or finished lately (`results`).
   */
  | {
      type: "room-snapshot"
      messages: RoomMessage[]
      pending: string[]
      results: RoomPostResult[]
    }
  /** Messages the room recorded since, each sent once. */
  | { type: "room-messages"; messages: RoomMessage[] }
  | { type: "room-status"; status: RoomSocketStatus; message?: string }
  /** A post's result, to every socket of its author. */
  | { type: "room-posted"; result: RoomPostResult }

// The largest message a page may send: a post is at most 4,000 characters,
// and the object reads nothing longer.
const maxClientMessage = 64 * 1024

const postId = /^[\w-]{1,64}$/

/** A page's message, or nothing for anything the object does not take. */
export function parseRoomClientMessage(
  message: unknown
): RoomClientMessage | undefined {
  const value = parsedObject(message, maxClientMessage)
  if (!value) return undefined
  if (value.type === "room-visibility" && typeof value.visible === "boolean") {
    return { type: "room-visibility", visible: value.visible }
  }
  if (
    value.type === "room-post" &&
    typeof value.id === "string" &&
    postId.test(value.id) &&
    typeof value.body === "string"
  ) {
    return { type: "room-post", id: value.id, body: value.body }
  }
  return undefined
}

const socketStatuses = new Set<string>([
  "live",
  "error",
  "paused",
  "unavailable",
  "gone",
  "session_expired",
])

/** The object's message, or nothing for anything else on the socket (the SDK's own frames). */
export function parseRoomServerMessage(
  data: unknown
): RoomServerMessage | undefined {
  const value = parsedObject(data)
  if (!value) return undefined
  switch (value.type) {
    case "room-snapshot":
      if (!Array.isArray(value.messages)) return undefined
      return {
        type: "room-snapshot",
        messages: value.messages as RoomMessage[],
        pending: Array.isArray(value.pending)
          ? value.pending.filter((id): id is string => typeof id === "string")
          : [],
        results: Array.isArray(value.results)
          ? (value.results as unknown[]).filter(isPostResult)
          : [],
      }
    case "room-messages":
      if (!Array.isArray(value.messages)) return undefined
      return {
        type: "room-messages",
        messages: value.messages as RoomMessage[],
      }
    case "room-status":
      if (typeof value.status !== "string") return undefined
      if (!socketStatuses.has(value.status)) return undefined
      return {
        type: "room-status",
        status: value.status as RoomSocketStatus,
        ...(typeof value.message === "string"
          ? { message: value.message }
          : {}),
      }
    case "room-posted":
      return isPostResult(value.result)
        ? { type: "room-posted", result: value.result }
        : undefined
    default:
      return undefined
  }
}

function isPostResult(value: unknown): value is RoomPostResult {
  if (typeof value !== "object" || value === null) return false
  const { id, ok } = value as { id?: unknown; ok?: unknown }
  if (typeof id !== "string") return false
  if (ok === true) {
    return Array.isArray((value as { messages?: unknown }).messages)
  }
  const { status, message } = value as { status?: unknown; message?: unknown }
  return (
    ok === false && typeof status === "number" && typeof message === "string"
  )
}

function parsedObject(
  data: unknown,
  maxLength = Number.POSITIVE_INFINITY
): Record<string, unknown> | undefined {
  if (typeof data !== "string" || data.length > maxLength) return undefined
  let value: unknown
  try {
    value = JSON.parse(data)
  } catch {
    return undefined
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined
  }
  return value as Record<string, unknown>
}

/**
 * Whether `messages` already hold a post: the member's words, said after
 * everything the room held when it went out (`after`, the last seq then).
 */
export function hasLanded(
  messages: readonly RoomMessage[],
  post: { body: string; after: number },
  userId: string | undefined
): boolean {
  if (!userId) return false
  const body = post.body.trim()
  return messages.some(
    (message) =>
      message.author_kind === "person" &&
      message.author_user_id === userId &&
      message.seq > post.after &&
      message.body.trim() === body
  )
}
