// Query keys, query options and cache updates for teamspaces. Everything a
// teamspace holds (its ledger, its room) is keyed under that teamspace, so
// removing the teamspace's key removes all of it.
import { queryOptions } from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"
import { getTeamspace, listTeamspaceEntries, listTeamspaces } from "./client"
import type { Teamspace } from "./client"

/** The ledger page the old app read. */
export const TEAMSPACE_ENTRIES_LIMIT = 100

function all(organizationId: string) {
  return ["teamspaces", organizationId] as const
}

function list(organizationId: string) {
  return [...all(organizationId), "list"] as const
}

function teamspace(organizationId: string, teamspaceId: string) {
  return [...all(organizationId), "detail", teamspaceId] as const
}

/** Without `limit`, the prefix of every page size. */
function entries(organizationId: string, teamspaceId: string, limit?: number) {
  const prefix = [...teamspace(organizationId, teamspaceId), "entries"] as const
  return limit === undefined ? prefix : ([...prefix, limit] as const)
}

/**
 * What a page reads of the teamspace's room beside its Durable Object: the
 * catch-up brief (lib/rooms). The room itself arrives over the object's
 * socket (useTeamspaceRoom), not as a query.
 */
function room(organizationId: string, teamspaceId: string) {
  return [...teamspace(organizationId, teamspaceId), "room"] as const
}

export const teamspaceKeys = { all, list, teamspace, entries, room }

/** `listTeamspaces`, under `teamspaceKeys.list`. */
export function teamspacesQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: teamspaceKeys.list(organizationId),
    queryFn: ({ signal }) => listTeamspaces(organizationId, { signal }),
  })
}

/** `getTeamspace`, under `teamspaceKeys.teamspace`. */
export function teamspaceQueryOptions(
  organizationId: string,
  teamspaceId: string
) {
  return queryOptions({
    queryKey: teamspaceKeys.teamspace(organizationId, teamspaceId),
    queryFn: ({ signal }) =>
      getTeamspace(organizationId, teamspaceId, { signal }),
  })
}

/** `listTeamspaceEntries`, newest first. Members only: gate on `is_member`. */
export function teamspaceEntriesQueryOptions(
  organizationId: string,
  teamspaceId: string,
  limit = TEAMSPACE_ENTRIES_LIMIT
) {
  return queryOptions({
    queryKey: teamspaceKeys.entries(organizationId, teamspaceId, limit),
    queryFn: ({ signal }) =>
      listTeamspaceEntries(organizationId, teamspaceId, { limit, signal }),
  })
}

/** After `createTeamspace`: caches the new space and refreshes the list. */
export async function afterCreateTeamspace(
  queryClient: QueryClient,
  organizationId: string,
  created: Teamspace
): Promise<void> {
  queryClient.setQueryData(
    teamspaceKeys.teamspace(organizationId, created.id),
    created
  )
  await queryClient.invalidateQueries({
    queryKey: teamspaceKeys.list(organizationId),
  })
}

/** After `updateTeamspace`: caches the answer and refreshes the list. */
export async function afterUpdateTeamspace(
  queryClient: QueryClient,
  organizationId: string,
  updated: Teamspace
): Promise<void> {
  queryClient.setQueryData(
    teamspaceKeys.teamspace(organizationId, updated.id),
    updated
  )
  await queryClient.invalidateQueries({
    queryKey: teamspaceKeys.list(organizationId),
  })
}

/** After `deleteTeamspace`: drops the space, its ledger and its room, and refreshes the list. */
export async function afterDeleteTeamspace(
  queryClient: QueryClient,
  organizationId: string,
  teamspaceId: string
): Promise<void> {
  queryClient.removeQueries({
    queryKey: teamspaceKeys.teamspace(organizationId, teamspaceId),
  })
  await queryClient.invalidateQueries({
    queryKey: teamspaceKeys.list(organizationId),
  })
}

/**
 * After `addTeamspaceContribution`, `updateTeamspaceContribution` or
 * `deleteTeamspaceContribution`, each of which answers with the whole
 * teamspace: caches it, refreshes the list, and re-reads what the page holds
 * of the room (its catch-up). The room's seats, which include every active
 * contributed specialist, come from its Durable Object's next read of Go.
 */
export async function afterTeamspaceContributionChange(
  queryClient: QueryClient,
  organizationId: string,
  updated: Teamspace
): Promise<void> {
  queryClient.setQueryData(
    teamspaceKeys.teamspace(organizationId, updated.id),
    updated
  )
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: teamspaceKeys.list(organizationId),
    }),
    queryClient.invalidateQueries({
      queryKey: teamspaceKeys.room(organizationId, updated.id),
    }),
  ])
}

/**
 * After `publishTeamspaceEntry`: refreshes the ledger. The organization
 * agent synthesizes the entry later, from an event, so its context and
 * follow-ups change on their own time rather than now.
 */
export async function afterPublishTeamspaceEntry(
  queryClient: QueryClient,
  organizationId: string,
  teamspaceId: string
): Promise<void> {
  await queryClient.invalidateQueries({
    queryKey: teamspaceKeys.entries(organizationId, teamspaceId),
  })
}
