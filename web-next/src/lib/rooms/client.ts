// A teamspace's room: the team's people and their agents talking in one
// place. Handlers are in agents/primary/internal/httpserver/server.go; the
// rules are in agents/primary/internal/rooms.
//
// A page's room is the teamspace's Durable Object (src/teamspace-room-agent.ts,
// through useTeamspaceRoom), which makes these calls as each member, with
// that member's cookie (`roomApi`). Presence rides on them: every read,
// catch-up and post records that the member is in the room, and somebody who
// has not read it for 90 seconds counts as away, and then their own agent
// may answer for them. So the object reads the room as each member who has
// it open in a visible tab, and never as anyone else.
import { apiRequest } from "@/lib/api/client"
import { organizationPath, withSearch } from "@/lib/agents/http"
import type { ReadOptions } from "@/lib/agents/http"
import { teamspaceRoomAgentName } from "./room-agent"

/** The most a room read returns; larger limits are read as this. */
export const ROOM_READ_LIMIT = 200

// How many pages one read may take before returning what it has; the next
// read carries on from there.
const MAX_PAGES_PER_READ = 10

export type SeatKind = "person" | "agent"

/** One participant, as the room shows them to everybody. */
export type RoomSeat = {
  /** How the seat is addressed in a message, without the "@". */
  handle: string
  kind: SeatKind
  /** The person, or for an agent the person whose agent it is. */
  user_id: string
  agent_id?: string
  agent_name?: string
  /** The user_id this agent speaks for: set on a member's own agent, never on a shared specialist. */
  speaks_for?: string
  display_name: string
  /** What the seat is here for; the room routes unaddressed work on it. */
  role?: string
  /** The person read the room in the last 90 seconds. False for agents. */
  present: boolean
}

/** One thing said in a room. */
export type RoomMessage = {
  id: string
  room_id: string
  /** Gapless per room, from 1. */
  seq: number
  author_handle: string
  author_kind: SeatKind
  author_user_id: string
  author_agent_id?: string
  body: string
  /** The handles the body named, the author excluded. */
  addressed_to?: string[]
  in_reply_to_seq?: number
  created_at: string
  /**
   * What the message is: something said ("message", and every message from
   * a server before kinds), or the notes a parley left ("parley_notes",
   * posted by the organization agent when a call in the teamspace ends,
   * #392). `body` always says it in words, so a kind this client does not
   * know reads as its body.
   */
  kind?: RoomMessageKind
  /** What a client draws a non-plain kind from; absent for anything said. */
  data?: unknown
}

export type RoomMessageKind = "message" | "parley_notes" | (string & {})

/** One thing somebody took away from a parley. */
export type ParleyAction = {
  text: string
  /** Who took it, as the call knew them; absent for nobody in particular. Not checked against the team. */
  owner_user_id?: string
}

/** A "parley_notes" message's `data`: the call, how long it ran, what was decided and who owes what. */
export type ParleyNotes = {
  call_id: string
  /** How long the call ran; absent when it isn't a length. */
  duration_ms?: number
  decisions: string[]
  actions: ParleyAction[]
}

/**
 * The parley notes a message carries, or undefined when it carries none (any
 * other kind, or data this client can't read): such a message shows its
 * body. An item that isn't words is left out rather than guessed at.
 */
export function parleyNotesOf(message: RoomMessage): ParleyNotes | undefined {
  if (message.kind !== "parley_notes") return undefined
  const data = message.data
  if (!data || typeof data !== "object") return undefined
  const { call_id, duration_ms, decisions, actions } = data as Record<
    string,
    unknown
  >
  if (!Array.isArray(decisions) || !Array.isArray(actions)) return undefined
  return {
    call_id: typeof call_id === "string" ? call_id : "",
    ...(typeof duration_ms === "number" &&
    Number.isFinite(duration_ms) &&
    duration_ms >= 0
      ? { duration_ms }
      : {}),
    decisions: decisions.filter(
      (decision): decision is string =>
        typeof decision === "string" && decision.trim() !== ""
    ),
    actions: actions.flatMap((action): ParleyAction[] => {
      if (!action || typeof action !== "object") return []
      const { text, owner_user_id } = action as Record<string, unknown>
      if (typeof text !== "string" || text.trim() === "") return []
      return [
        typeof owner_user_id === "string" && owner_user_id !== ""
          ? { text, owner_user_id }
          : { text },
      ]
    }),
  }
}

/**
 * What the room amounts to: what an agent reads instead of the scrollback,
 * and what a member coming back is handed. Before the first compaction it
 * is empty: revision 0, through_seq 0, and a zero `updated_at`
 * ("0001-01-01T00:00:00Z").
 */
export type RoomBrief = {
  room_id: string
  revision: number
  /** The last message the brief accounts for. */
  through_seq: number
  summary: string
  decided: string[]
  open: string[]
  owed: string[]
  updated_at: string
}

export type RoomRead = { messages: RoomMessage[]; seats: RoomSeat[] }

export type RoomCatchUp = { brief: RoomBrief; messages: RoomMessage[] }

/** What a read added, and the last seq it got to: the next read starts after it. */
export type RoomReadAfter = RoomRead & { readThrough: number }

/**
 * How a room call reaches Go: `apiRequest` from a page, or the room's
 * Durable Object acting as one member. Resolves the JSON answer, or throws an
 * `ApiError` with Go's status, code and words.
 */
export type RoomRequest = <T>(
  path: string,
  options?: { method?: "GET" | "POST"; body?: unknown; signal?: AbortSignal }
) => Promise<T>

function roomPath(
  organizationId: string,
  teamspaceId: string,
  resource: "messages" | "catchup"
) {
  return organizationPath(organizationId, "teamspaces", teamspaceId, resource)
}

/** Go's room routes, each made through `request`. */
export function roomApi(request: RoomRequest) {
  // Without `after` a read starts at the room's first message, not its
  // latest: a room longer than one page needs `after`, which `readAfter`
  // tracks.
  /** `GET …/teamspaces/{teamspaceID}/messages[?after=&limit=]` (server.readRoom): up to `limit` (≤200) messages after `after`, oldest first, with every seat. */
  async function read(
    organizationId: string,
    teamspaceId: string,
    {
      after,
      limit,
      signal,
    }: ReadOptions & { after?: number; limit?: number } = {}
  ): Promise<RoomRead> {
    const answer = await request<Partial<RoomRead>>(
      withSearch(roomPath(organizationId, teamspaceId, "messages"), {
        after,
        limit,
      }),
      { signal }
    )
    return { messages: answer.messages ?? [], seats: answer.seats ?? [] }
  }

  /** `POST …/teamspaces/{teamspaceID}/messages` (server.postRoomMessage): the member's message, then every agent reply. It answers only once the room's agents have, which can take a minute and more; each message is readable as the room records it. */
  async function say(
    organizationId: string,
    teamspaceId: string,
    body: string,
    { signal }: ReadOptions = {}
  ): Promise<RoomMessage[]> {
    const said = await request<{ messages?: RoomMessage[] }>(
      roomPath(organizationId, teamspaceId, "messages"),
      { method: "POST", body: { body }, signal }
    )
    return said.messages ?? []
  }

  /** `GET …/teamspaces/{teamspaceID}/catchup[?after=]` (server.catchUpOnRoom): the brief, and up to 200 messages after the later of `after` and its through_seq. */
  async function catchUp(
    organizationId: string,
    teamspaceId: string,
    { after, signal }: ReadOptions & { after?: number } = {}
  ): Promise<RoomCatchUp> {
    const caught = await request<{
      brief: RoomBrief
      messages?: RoomMessage[]
    }>(
      withSearch(roomPath(organizationId, teamspaceId, "catchup"), { after }),
      { signal }
    )
    return { brief: caught.brief, messages: caught.messages ?? [] }
  }

  /**
   * What the room added after `readThrough`, page by page. Without it,
   * from the room's latest page: a read without `after` would start at the
   * room's first message, so a catch-up (the brief's through_seq and what
   * follows it) says first where the room ends.
   */
  async function readAfter(
    organizationId: string,
    teamspaceId: string,
    readThrough: number | undefined,
    { signal }: ReadOptions = {}
  ): Promise<RoomReadAfter> {
    let after: number
    if (readThrough !== undefined) {
      after = readThrough
    } else {
      const caught = await catchUp(organizationId, teamspaceId, { signal })
      const end = Math.max(
        caught.brief.through_seq,
        caught.messages.at(-1)?.seq ?? 0
      )
      // One short of a page, so that a full page means more has arrived.
      after = Math.max(0, end - (ROOM_READ_LIMIT - 1))
    }
    let messages: RoomMessage[] = []
    let seats: RoomSeat[] = []
    for (let page = 0; page < MAX_PAGES_PER_READ; page += 1) {
      const answer = await read(organizationId, teamspaceId, {
        after,
        limit: ROOM_READ_LIMIT,
        signal,
      })
      messages = mergeRoomMessages(messages, answer.messages)
      seats = answer.seats
      after = answer.messages.at(-1)?.seq ?? after
      if (answer.messages.length < ROOM_READ_LIMIT) break
    }
    return { messages, seats, readThrough: after }
  }

  return { read, say, catchUp, readAfter }
}

/**
 * `roomApi(apiRequest).catchUp`: the brief and what the member has not seen
 * since `after`, for a member coming back. It records presence like any read.
 */
export function catchUpOnRoom(
  organizationId: string,
  teamspaceId: string,
  options: ReadOptions & { after?: number } = {}
): Promise<RoomCatchUp> {
  return roomApi(apiRequest).catchUp(organizationId, teamspaceId, options)
}

/** Where the room's Durable Object (`TeamspaceRoomAgent`) answers, on the app's own origin (`/agents/{class}/{name}`). */
export function teamspaceRoomPath(
  organizationId: string,
  teamspaceId: string
): string {
  return `/agents/teamspace-room-agent/${encodeURIComponent(
    teamspaceRoomAgentName(organizationId, teamspaceId)
  )}`
}

/**
 * GET /agents/teamspace-room-agent/{org}~{teamspace}: asks the Worker
 * whether the member's socket would be let in, since a browser is never told
 * why an upgrade was refused. The Worker makes the socket's own check with Go
 * and answers with Go's refusal (401, 403, 404, 503), or lets it through to
 * the object, which answers 204. Through `apiFetch`, so an expired cookie is
 * refreshed first. Not a Go route.
 */
export async function checkTeamspaceRoom(
  organizationId: string,
  teamspaceId: string
): Promise<void> {
  await apiRequest(teamspaceRoomPath(organizationId, teamspaceId))
}

/** Folds arrived messages into held ones: one per `seq`, the newer copy winning, in order. */
export function mergeRoomMessages(
  held: readonly RoomMessage[],
  arrived: readonly RoomMessage[]
): RoomMessage[] {
  const bySeq = new Map(held.map((message) => [message.seq, message]))
  for (const message of arrived) bySeq.set(message.seq, message)
  return [...bySeq.values()].sort((left, right) => left.seq - right.seq)
}
