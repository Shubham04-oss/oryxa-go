// The seam between a teamspace's room and the page that shows it.
//
// Today the room runs on lib/rooms: incremental reads every 5 s, which is
// also how the room knows the member is in it, and a post that answers only
// once the room's agents have. REALTIME.md moves the room into a Durable
// Object shared by the teamspace's people and agents; that object replaces
// the transport behind this hook, and a page that renders
// { messages, send, presence, status } does not change.
import { useCallback, useEffect, useRef, useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import { isUnavailableHere } from "@/lib/agents/policy"
import { sayInRoom } from "@/lib/rooms/client"
import type { RoomMessage, RoomSeat } from "@/lib/rooms/client"
import {
  ROOM_POLL_MS,
  afterSayInRoom,
  roomKeys,
  roomQueryOptions,
} from "@/lib/rooms/queries"
import type { RoomView } from "@/lib/rooms/queries"
import type { Teamspace } from "@/lib/teamspaces/client"
import { canUseRoom } from "@/lib/teamspaces/policy"
import { teamspaceKeys } from "@/lib/teamspaces/queries"

export type { RoomMessage, RoomSeat }

/** Where the member stands with the room. */
export type RoomConnection =
  /** Nothing to open: no organization or teamspace yet. */
  | "closed"
  /** They manage the teamspace without being on its team, so it has no seat for them. */
  | "not-member"
  /** The teamspace is paused; its room answers 400 until it is resumed. */
  | "paused"
  /** The first read is out. */
  | "connecting"
  /** Read on the last poll: the member counts as present. */
  | "live"
  /** 404: they are no longer on the team, or the teamspace is gone. */
  | "gone"
  /** This server does not run rooms (503 rooms_unavailable). */
  | "unavailable"
  /** Any other failure; the next poll tries again. */
  | "error"

/** A post the room has not answered yet. */
export type OutgoingMessage = {
  /** Local only, for rendering. */
  id: string
  body: string
  /** The room has recorded it (a read returned it); its agents may still be answering. */
  landed: boolean
}

export type RoomPresence = {
  /** Every seat: each person on the team, the agent that speaks for them, and each active contributed specialist. */
  seats: RoomSeat[]
  /** People who read the room in the last 90 seconds, the member included. Agents are never present. */
  here: RoomSeat[]
  /** The member's own seat. Without one they can read but not post. */
  me?: RoomSeat
}

export type RoomStatus = {
  connection: RoomConnection
  /** Why reading failed, in words for the member. */
  error?: string
  /** Posts still waiting for the room's answer, oldest first. */
  outgoing: OutgoingMessage[]
  /** Why the last post failed, until the next one goes out. */
  sendError?: string
  /** A post would be taken now: the room is open and the member has a seat. */
  canPost: boolean
}

export type TeamspaceRoom = {
  /** What was said, oldest first. */
  messages: RoomMessage[]
  /**
   * Says `body` in the room. Resolves true once the room has the message,
   * false when it does not (the page keeps the draft). Several posts may be
   * out at once; each shows in `status.outgoing` until the room answers it.
   */
  send: (body: string) => Promise<boolean>
  presence: RoomPresence
  status: RoomStatus
}

const noMessages: RoomMessage[] = []
const noSeats: RoomSeat[] = []

/**
 * The room of `teamspace`, read while the calling component is mounted.
 * Keep it mounted for as long as the room is on screen: reading is what
 * marks `viewerId` present, and 90 seconds after the last read their own
 * agent may answer for them. It reads only when the member may use the room
 * (`canUseRoom`): on the team, in an active teamspace.
 */
export function useTeamspaceRoom(
  organizationId: string | undefined,
  teamspace: Teamspace | undefined,
  viewerId: string | undefined
): TeamspaceRoom {
  const queryClient = useQueryClient()
  const orgId = organizationId ?? ""
  const spaceId = teamspace?.id ?? ""
  const open = Boolean(organizationId && teamspace && canUseRoom(teamspace))
  const room = useQuery({
    ...roomQueryOptions(orgId, spaceId),
    enabled: open,
    // Polling is the retry: a failed read is tried again on the next tick,
    // except when the answer will not change by asking again.
    retry: false,
    refetchInterval: (query) =>
      isSettled(query.state.error) ? false : ROOM_POLL_MS,
  })

  const messages = room.data?.messages ?? noMessages
  const seats = room.data?.seats ?? noSeats
  const me = viewerId
    ? seats.find((seat) => seat.kind === "person" && seat.user_id === viewerId)
    : undefined
  const here = seats.filter((seat) => seat.kind === "person" && seat.present)

  const connection = connectionOf(
    Boolean(organizationId && teamspace),
    teamspace,
    room.error,
    room.data !== undefined
  )
  const canPost =
    Boolean(me) && (connection === "live" || connection === "error")

  // A paused room answers 400 and a room the member may no longer read
  // answers 404: either way the teamspace itself changed, so the page's copy
  // of it is re-read and its header and gates follow.
  const readError = room.error
  useEffect(() => {
    if (!orgId || !spaceId || !(readError instanceof ApiError)) return
    if (readError.status !== 400 && readError.status !== 404) return
    void refreshTeamspace(queryClient, orgId, spaceId)
  }, [queryClient, orgId, spaceId, readError])

  const [pending, setPending] = useState<
    Array<{ id: string; body: string; after: number }>
  >([])
  const [sendError, setSendError] = useState<string>()
  const counter = useRef(0)

  const send = useCallback(
    async (text: string): Promise<boolean> => {
      const body = text.trim()
      if (!body || !orgId || !spaceId || !canPost) return false
      counter.current += 1
      const key = roomKeys.room(orgId, spaceId)
      const item = {
        id: `out-${counter.current}`,
        body,
        // The room gives the message a later seq than anything it held.
        after:
          queryClient.getQueryData<RoomView>(key)?.messages.at(-1)?.seq ?? 0,
      }
      setPending((current) => [...current, item])
      setSendError(undefined)
      try {
        const said = await sayInRoom(orgId, spaceId, body)
        void afterSayInRoom(queryClient, orgId, spaceId, said)
        return true
      } catch (error) {
        const view = queryClient.getQueryData<RoomView>(key)
        const landed = hasLanded(view?.messages ?? [], item, viewerId)
        setSendError(sendErrorMessage(error, landed))
        if (error instanceof ApiError && error.status === 400) {
          void refreshTeamspace(queryClient, orgId, spaceId)
        }
        return landed
      } finally {
        setPending((current) =>
          current.filter((outgoing) => outgoing.id !== item.id)
        )
      }
    },
    [queryClient, orgId, spaceId, canPost, viewerId]
  )

  return {
    messages,
    send,
    presence: { seats, here, me },
    status: {
      connection,
      error: connection === "error" ? readErrorMessage(room.error) : undefined,
      outgoing: pending.map((item) => ({
        id: item.id,
        body: item.body,
        landed: hasLanded(messages, item, viewerId),
      })),
      sendError,
      canPost,
    },
  }
}

function connectionOf(
  known: boolean,
  teamspace: Teamspace | undefined,
  error: unknown,
  hasData: boolean
): RoomConnection {
  if (!known || !teamspace) return "closed"
  if (teamspace.status !== "active") return "paused"
  if (!teamspace.is_member) return "not-member"
  if (error) {
    if (error instanceof ApiError) {
      if (error.status === 400) return "paused"
      if (error.status === 403 || error.status === 404) return "gone"
    }
    if (isUnavailableHere(error)) return "unavailable"
    return "error"
  }
  return hasData ? "live" : "connecting"
}

// Asking again cannot change these answers, so the room stops polling
// until the page changes what it asks for. A 401 is an expired session the
// client could not refresh; polling would only retry the refresh every 5 s.
function isSettled(error: unknown): boolean {
  if (isUnavailableHere(error)) return true
  return (
    error instanceof ApiError &&
    (error.status === 401 || error.status === 403 || error.status === 404)
  )
}

/** Whether the room already shows `item`: the member's words, said after it went out. */
function hasLanded(
  messages: readonly RoomMessage[],
  item: { body: string; after: number },
  viewerId: string | undefined
): boolean {
  return messages.some(
    (message) =>
      message.author_kind === "person" &&
      message.author_user_id === viewerId &&
      message.seq > item.after &&
      message.body.trim() === item.body
  )
}

async function refreshTeamspace(
  queryClient: QueryClient,
  organizationId: string,
  teamspaceId: string
) {
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: teamspaceKeys.teamspace(organizationId, teamspaceId),
      exact: true,
    }),
    queryClient.invalidateQueries({
      queryKey: teamspaceKeys.list(organizationId),
    }),
  ])
}

/** The server's own wording, as a sentence. */
function sentence(message: string): string {
  const text = message.trim()
  if (!text) return text
  const capitalized = text.charAt(0).toUpperCase() + text.slice(1)
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`
}

function readErrorMessage(error: unknown): string {
  if (error instanceof ApiError && error.status === 401) {
    return "Your session has ended. Sign in again to read this room."
  }
  if (error instanceof ApiError && error.status < 500 && error.status !== 429) {
    return sentence(error.message)
  }
  return "Couldn't reach the room."
}

function sendErrorMessage(error: unknown, landed: boolean): string {
  if (landed) {
    return "Your message is in the room, but the agents' answers didn't come back."
  }
  if (isUnavailableHere(error)) {
    return "Rooms aren't available on this server."
  }
  if (error instanceof ApiError) {
    switch (error.status) {
      case 400:
        return sentence(error.message)
      case 401:
        return "Your session has ended. Sign in again to post."
      case 403:
        return "Only teammates with an active agent can post in this room."
      case 404:
        return "This room is no longer available to you."
      case 429:
        return "Too many messages at once. Try again in a moment."
    }
  }
  return "Couldn't send your message. Try again."
}
