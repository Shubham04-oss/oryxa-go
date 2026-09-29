// What the request card, Requests and Activity read, and how often: the lists
// by box and origin, one request, Slack's activity, and who the members are.
// Each list key sits beneath `agentKeys.requestBox` (and Activity beneath
// `agentKeys.requests`), so the invalidations lib/agents already makes after a
// decision or a response refresh them too, and one invalidation of
// `agentKeys.requests` refreshes them all.
import { useMemo } from "react"
import { queryOptions, useQuery } from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"
import {
  listAgentActivity,
  listAgentRequests,
} from "@/lib/agents/agent-requests"
import type {
  AgentRequestBox,
  AgentRequestOrigin,
} from "@/lib/agents/agent-requests"
import {
  AGENT_REQUESTS_LIMIT,
  AGENT_REQUESTS_POLL_MS,
  agentKeys,
  agentRequestQueryOptions,
} from "@/lib/agents/queries"
import { membersQueryOptions } from "@/lib/auth/queries"

/** Which requests a list shows: all of them, or those made in Slack or by an agent in Oryxa. */
export type OriginFilter = "all" | AgentRequestOrigin

/** Activity is read whole: the newest hundred, the most Go gives. */
export const ACTIVITY_LIMIT = 100

// A request decided in Slack, or on another device, must show here without a
// reload: these are re-read on every focus and on a timer while the tab is in
// view, and are never taken for fresh.
const settling = {
  staleTime: 0,
  refetchInterval: AGENT_REQUESTS_POLL_MS,
  refetchOnWindowFocus: "always",
} as const

/** `listAgentRequests` for one box, narrowed to where the requests were made. */
export function requestListQueryOptions(
  organizationId: string,
  box: AgentRequestBox,
  origin: OriginFilter = "all",
  limit = AGENT_REQUESTS_LIMIT
) {
  return queryOptions({
    queryKey: [
      ...agentKeys.requestBox(organizationId, box),
      "filtered",
      origin,
      limit,
    ] as const,
    queryFn: ({ signal }) =>
      listAgentRequests(organizationId, box, {
        limit,
        origin: origin === "all" ? undefined : origin,
        signal,
      }),
    ...settling,
  })
}

/** One request, re-read the same way. */
export function requestDetailQueryOptions(
  organizationId: string,
  requestId: string
) {
  return { ...agentRequestQueryOptions(organizationId, requestId), ...settling }
}

/** What happened to the member's Slack asks. */
export function activityQueryOptions(
  organizationId: string,
  limit = ACTIVITY_LIMIT
) {
  return queryOptions({
    queryKey: [...agentKeys.requests(organizationId), "activity", limit] as const,
    queryFn: ({ signal }) =>
      listAgentActivity(organizationId, { limit, signal }),
    ...settling,
  })
}

/** Refreshes every request query of the organization: the lists, each request and Activity. */
export function refreshRequests(
  queryClient: QueryClient,
  organizationId: string
): Promise<void> {
  return queryClient.invalidateQueries({
    queryKey: agentKeys.requests(organizationId),
  })
}

/** Who the members are, by id, for the names a request holds only ids of. */
export type PeopleNames = {
  status: "loading" | "ready" | "error"
  /** A member's name, once the list can say; nothing while it loads, and for anyone it does not list. */
  nameOf: (userId: string) => string | undefined
}

/** The members list, as `nameOf`. It is the list Settings and the DMs already read, so it costs no more. */
export function usePeopleNames(organizationId: string): PeopleNames {
  const members = useQuery(membersQueryOptions(organizationId))
  const { data } = members
  return useMemo(() => {
    const names = new Map(
      (data ?? []).map((member) => [
        member.user_id,
        member.display_name?.trim() || member.email,
      ])
    )
    return {
      status: data ? "ready" : members.isError ? "error" : "loading",
      nameOf: (userId) => names.get(userId),
    }
  }, [data, members.isError])
}
