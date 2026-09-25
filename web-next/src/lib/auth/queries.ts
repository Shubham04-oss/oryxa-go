// Query keys and options for what the auth service answers about an
// organization. The members list's key is the one Settings → People, Teams
// and a teamspace's creator already read, so every reader shares one copy.
import { queryOptions } from "@tanstack/react-query"
import { getMembers } from "./invitations"

export const organizationKeys = {
  members: (organizationId: string) =>
    ["organizations", organizationId, "members"] as const,
}

/** `GET /v1/organizations/{org}/members`: everyone in the organization. */
export function membersQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: organizationKeys.members(organizationId),
    queryFn: () => getMembers(organizationId),
    staleTime: 60_000,
  })
}
