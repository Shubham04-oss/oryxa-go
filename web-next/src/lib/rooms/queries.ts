// Query keys, query options and cache updates for a teamspace's room. The
// room is keyed under its teamspace (teamspaceKeys.room), so deleting the
// teamspace drops it and a contribution change re-reads its seats.
import { queryOptions } from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"
import { teamspaceKeys } from "@/lib/teamspaces/queries"
import type { ReadOptions } from "@/lib/agents/http"
import {
  ROOM_READ_LIMIT,
  catchUpOnRoom,
  mergeRoomMessages,
  readRoom,
} from "./client"
import type { RoomMessage, RoomRead } from "./client"

/** How often an open room is re-read: this is also what keeps its member present. */
export const ROOM_POLL_MS = 5_000
/** The most messages the room cache keeps, newest last. */
export const ROOM_HISTORY_LIMIT = 1_000
// How many pages one fetch may read before returning what it has; the next
// poll carries on from there.
const MAX_PAGES_PER_FETCH = 10

/** A room as the cache holds it. */
export type RoomView = RoomRead & {
  /**
   * The last seq a read returned; the next read starts after it. A post's
   * answer is merged into `messages` without moving it, because another
   * member's message can sit between the seqs a post returns.
   */
  readThrough: number
}

export const roomKeys = {
  room: teamspaceKeys.room,
  catchUp: (organizationId: string, teamspaceId: string, after: number) =>
    [
      ...teamspaceKeys.room(organizationId, teamspaceId),
      "catch-up",
      after,
    ] as const,
}

/**
 * Reads what the room added after `held`. With nothing held it starts from
 * the room's latest page: a read without `after` would start at the room's
 * first message, so a catch-up (the brief's through_seq and what follows it)
 * says first where the room ends.
 */
export async function readRoomForward(
  organizationId: string,
  teamspaceId: string,
  held: RoomView | undefined,
  { signal }: ReadOptions = {}
): Promise<RoomView> {
  let readThrough: number
  if (held) {
    readThrough = held.readThrough
  } else {
    const caught = await catchUpOnRoom(organizationId, teamspaceId, { signal })
    const end = Math.max(
      caught.brief.through_seq,
      caught.messages.at(-1)?.seq ?? 0
    )
    // One short of a page, so that a full page means more has arrived.
    readThrough = Math.max(0, end - (ROOM_READ_LIMIT - 1))
  }
  let messages = held?.messages ?? []
  let seats = held?.seats ?? []
  for (let page = 0; page < MAX_PAGES_PER_FETCH; page += 1) {
    const read = await readRoom(organizationId, teamspaceId, {
      after: readThrough,
      limit: ROOM_READ_LIMIT,
      signal,
    })
    messages = mergeRoomMessages(messages, read.messages)
    seats = read.seats
    readThrough = read.messages.at(-1)?.seq ?? readThrough
    if (read.messages.length < ROOM_READ_LIMIT) break
  }
  return { messages: messages.slice(-ROOM_HISTORY_LIMIT), seats, readThrough }
}

/**
 * The room, re-read every 5 s while it is on screen and never in a
 * background tab. Reading is how the room knows the member is there: stop
 * and after 90 s they count as away, and their own agent may answer for
 * them. Enable it only for a member of an active teamspace (`canUseRoom`).
 */
export function roomQueryOptions(organizationId: string, teamspaceId: string) {
  return queryOptions({
    queryKey: roomKeys.room(organizationId, teamspaceId),
    queryFn: async ({ client, queryKey, signal }): Promise<RoomView> => {
      const next = await readRoomForward(
        organizationId,
        teamspaceId,
        client.getQueryData<RoomView>(queryKey),
        { signal }
      )
      // Keep whatever was merged in while this read was out, such as a
      // post's answer.
      const latest = client.getQueryData<RoomView>(queryKey)
      if (!latest) return next
      return {
        ...next,
        messages: mergeRoomMessages(latest.messages, next.messages).slice(
          -ROOM_HISTORY_LIMIT
        ),
      }
    },
    refetchInterval: ROOM_POLL_MS,
    refetchIntervalInBackground: false,
  })
}

/**
 * The room's brief and what the member has not seen since `after`, for a
 * member coming back. Not polled; it records presence like any read.
 */
export function roomCatchUpQueryOptions(
  organizationId: string,
  teamspaceId: string,
  after: number
) {
  return queryOptions({
    queryKey: roomKeys.catchUp(organizationId, teamspaceId, after),
    queryFn: ({ signal }) =>
      catchUpOnRoom(organizationId, teamspaceId, { after, signal }),
  })
}

/**
 * Folds messages from `sayInRoom` or `catchUpOnRoom` into a room already in
 * the cache, without moving where its next read starts.
 */
export function mergeIntoRoom(
  queryClient: QueryClient,
  organizationId: string,
  teamspaceId: string,
  arrived: readonly RoomMessage[]
): void {
  queryClient.setQueryData<RoomView>(
    roomKeys.room(organizationId, teamspaceId),
    (held) =>
      held && {
        ...held,
        messages: mergeRoomMessages(held.messages, arrived).slice(
          -ROOM_HISTORY_LIMIT
        ),
      }
  )
}

/**
 * After `sayInRoom`: shows what the room added at once, then re-reads the
 * room and the ledger, since the room publishes what it concludes to the
 * teamspace's ledger itself.
 */
export async function afterSayInRoom(
  queryClient: QueryClient,
  organizationId: string,
  teamspaceId: string,
  said: readonly RoomMessage[]
): Promise<void> {
  mergeIntoRoom(queryClient, organizationId, teamspaceId, said)
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: roomKeys.room(organizationId, teamspaceId),
      exact: true,
    }),
    queryClient.invalidateQueries({
      queryKey: teamspaceKeys.entries(organizationId, teamspaceId),
    }),
  ])
}
