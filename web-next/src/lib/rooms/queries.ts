// Query keys and query options for a teamspace's room. The room itself is
// not a query: it arrives over its Durable Object's socket
// (useTeamspaceRoom). What a page may still read beside it, the catch-up
// brief, is keyed under the teamspace (teamspaceKeys.room), so deleting the
// teamspace drops it.
import { queryOptions } from "@tanstack/react-query"
import { teamspaceKeys } from "@/lib/teamspaces/queries"
import { catchUpOnRoom } from "./client"

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
