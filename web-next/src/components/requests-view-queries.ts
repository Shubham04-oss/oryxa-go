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
  getAgentRequest,
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
} from "@/lib/agents/queries"
import { organizationKeys } from "@/lib/auth/queries"
import { getMembers } from "@/lib/auth/invitations"

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
  requestId: string,
  viewerId?: string
) {
  return {
    queryKey: [
      ...agentKeys.request(organizationId, requestId),
      ...(viewerId ? [viewerId] : []),
    ],
    queryFn: ({ signal }: { signal: AbortSignal }) =>
      getAgentRequest(organizationId, requestId, { signal }),
    ...settling,
  }
}

/** What happened to the member's Slack asks. */
export function activityQueryOptions(
  organizationId: string,
  limit = ACTIVITY_LIMIT
) {
  return queryOptions({
    queryKey: [
      ...agentKeys.requests(organizationId),
      "activity",
      limit,
    ] as const,
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
  /**
   * What tells a member apart from another with the same first name (H5):
   * their email. Nothing for a member whose first name no one else has.
   */
  hintOf: (userId: string) => string | undefined
}

/**
 * The members list, as `nameOf`. The organization remains its invalidation
 * prefix; an optional viewer scopes the authorized cache to that member.
 * `loading` lasts only until the list has answered
 * once: a list that failed stays `error` (names fall back to what a request
 * itself holds) however often it is tried again, so a failing members route
 * never sends every card back to "loading" and forward again, and is not
 * asked again by each card that mounts.
 */
export function usePeopleNames(
  organizationId: string,
  viewerId?: string,
  enabled = true
): PeopleNames {
  const members = useQuery({
    queryKey: [
      ...organizationKeys.members(organizationId),
      ...(viewerId ? [viewerId] : []),
    ],
    queryFn: () => getMembers(organizationId),
    staleTime: 60_000,
    enabled,
    retryOnMount: false,
  })
  const data = !viewerId || members.isSuccess ? members.data : undefined
  const failed = members.errorUpdateCount > 0
  return useMemo(() => {
    const names = new Map(
      (data ?? []).map((member) => [
        member.user_id,
        member.display_name?.trim() || member.email,
      ])
    )
    const first = (name: string) =>
      (name.includes("@") ? name : name.split(/\s+/)[0]).toLowerCase()
    const sharing = new Map<string, number>()
    for (const name of names.values())
      sharing.set(first(name), (sharing.get(first(name)) ?? 0) + 1)
    const emails = new Map(
      (data ?? []).map((member) => [member.user_id, member.email])
    )
    return {
      status: data ? "ready" : failed ? "error" : "loading",
      nameOf: (userId) => names.get(userId),
      hintOf: (userId) => {
        const name = names.get(userId)
        if (!name || (sharing.get(first(name)) ?? 0) < 2) return undefined
        const email = emails.get(userId)
        return email && email !== name ? email : undefined
      },
    }
  }, [data, failed])
}
