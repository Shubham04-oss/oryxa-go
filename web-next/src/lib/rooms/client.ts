// A teamspace's room: the team's people and their agents talking in one
// place. Handlers are in agents/primary/internal/httpserver/server.go; the
// rules are in agents/primary/internal/rooms.
//
// Presence: every read, catch-up and post records that the member is in
// the room. Somebody who has not read it for 90 seconds counts as away, and
// then their own agent may answer for them. A page showing the room has to
// keep reading it (roomQueryOptions polls every 5 s) or its member walks out.
import { apiRequest } from "@/lib/api/client"
import { organizationPath, withSearch } from "@/lib/agents/http"
import type { ReadOptions } from "@/lib/agents/http"

/** The most a room read returns; larger limits are read as this. */
export const ROOM_READ_LIMIT = 200

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

function roomPath(
  organizationId: string,
  teamspaceId: string,
  resource: "messages" | "catchup"
) {
  return organizationPath(organizationId, "teamspaces", teamspaceId, resource)
}

// Without `after` a read starts at the room's first message, not its latest:
// a room longer than one page needs `after`, which roomQueryOptions tracks.
/** `GET …/teamspaces/{teamspaceID}/messages[?after=&limit=]` (server.readRoom): up to `limit` (≤200) messages after `after`, oldest first, with every seat. */
export async function readRoom(
  organizationId: string,
  teamspaceId: string,
  {
    after,
    limit,
    signal,
  }: ReadOptions & { after?: number; limit?: number } = {}
): Promise<RoomRead> {
  const read = await apiRequest<Partial<RoomRead>>(
    withSearch(roomPath(organizationId, teamspaceId, "messages"), {
      after,
      limit,
    }),
    { signal }
  )
  return { messages: read.messages ?? [], seats: read.seats ?? [] }
}

/** `POST …/teamspaces/{teamspaceID}/messages` (server.postRoomMessage): the member's message, then every agent reply; can take tens of seconds. */
export async function sayInRoom(
  organizationId: string,
  teamspaceId: string,
  body: string
): Promise<RoomMessage[]> {
  const said = await apiRequest<{ messages?: RoomMessage[] }>(
    roomPath(organizationId, teamspaceId, "messages"),
    { method: "POST", body: { body } }
  )
  return said.messages ?? []
}

/** `GET …/teamspaces/{teamspaceID}/catchup[?after=]` (server.catchUpOnRoom): the brief, and up to 200 messages after the later of `after` and its through_seq. */
export async function catchUpOnRoom(
  organizationId: string,
  teamspaceId: string,
  { after, signal }: ReadOptions & { after?: number } = {}
): Promise<RoomCatchUp> {
  const caught = await apiRequest<{
    brief: RoomBrief
    messages?: RoomMessage[]
  }>(withSearch(roomPath(organizationId, teamspaceId, "catchup"), { after }), {
    signal,
  })
  return { brief: caught.brief, messages: caught.messages ?? [] }
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
