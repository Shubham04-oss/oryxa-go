// What needs the member, from one place (N5, D3, D4): the requests that wait
// on them, by the one rule `waitsOnOwner` keeps, and the chats in which a
// tool waits for their OK. Every count of it (the sidebar's Requests badge,
// the Requests header and inbox, Home's "Needs you", the agent panel and the
// agent overview) reads these two queries, so the numbers never disagree and
// the inbox is polled once, under one key, whoever shows it.
import { queryOptions, useQuery } from "@tanstack/react-query"
import { useHydrated } from "@tanstack/react-router"
import { waitsOnOwner } from "@/components/request-card-format"
import { ApiError } from "@/lib/api/client"
import { listAgentRequests } from "@/lib/agents/agent-requests"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import { isUnavailableHere } from "@/lib/agents/policy"
import { AGENT_REQUESTS_POLL_MS, agentKeys } from "@/lib/agents/queries"
import { listPendingConfirmations } from "@/lib/home/confirmations"
import type { PendingChat } from "@/lib/home/confirmations"
import { authorizedData } from "@/lib/home/queries"
import { retryUnlessRefused, threadKeys } from "@/lib/threads/client"

/** The most requests the one inbox read holds: the most Go gives. */
export const INBOX_LIMIT = 100

/** How often the chats' confirmations are read again while a page is in view. */
export const CONFIRMATIONS_POLL_MS = 30_000

export const needsYouKeys = {
  // Beneath `agentKeys.requestBox`, so every invalidation lib/agents and
  // Requests make after a decision refreshes it; the viewer's own, so a
  // member who takes over a page never reads another's inbox.
  inbox: (organizationId: string, viewerId: string) =>
    [
      ...agentKeys.requestBox(organizationId, "inbox"),
      "member",
      viewerId,
      INBOX_LIMIT,
    ] as const,
  // Beneath the organization's threads, but not their list, whose entries
  // are all thread arrays.
  confirmations: (organizationId: string, viewerId: string) =>
    [
      ...threadKeys.all(organizationId),
      "pending-confirmations",
      viewerId,
    ] as const,
}

// A request or a confirmation answered elsewhere, in Slack or on another
// device, must show here without a reload: re-read on every focus and on a
// timer while the tab is in view. Pages mounting together share one read.
const settling = {
  staleTime: 5_000,
  refetchOnWindowFocus: "always",
  refetchIntervalInBackground: false,
  retry: retryUnlessRefused,
} as const

/** The member's inbox: the one read of it every page shares. */
export function inboxQueryOptions(organizationId: string, viewerId: string) {
  return queryOptions({
    queryKey: needsYouKeys.inbox(organizationId, viewerId),
    queryFn: ({ signal }) =>
      listAgentRequests(organizationId, "inbox", {
        limit: INBOX_LIMIT,
        signal,
      }),
    refetchInterval: AGENT_REQUESTS_POLL_MS,
    ...settling,
  })
}

/** The member's chats with tool confirmations waiting, across all of them. */
export function pendingConfirmationsQueryOptions(
  organizationId: string,
  viewerId: string
) {
  return queryOptions({
    queryKey: needsYouKeys.confirmations(organizationId, viewerId),
    queryFn: ({ signal }) =>
      listPendingConfirmations(organizationId, { signal }),
    refetchInterval: CONFIRMATIONS_POLL_MS,
    ...settling,
  })
}

/** The requests among `rows` that wait on the member: the one rule, `waitsOnOwner`. */
export function waitingRequests(
  rows: readonly AgentRequest[],
  viewerId: string,
  now: number
): AgentRequest[] {
  return rows.filter((request) => waitsOnOwner(request, viewerId, now))
}

/** A read this server does not serve: nothing to count, rather than an error. */
function notServed(error: unknown): boolean {
  return (
    isUnavailableHere(error) ||
    (error instanceof ApiError && error.status === 404)
  )
}

/** The inbox read, shared by every page that shows it. */
export function useInbox(
  organizationId: string | undefined,
  viewerId: string | undefined,
  enabled = true
) {
  const hydrated = useHydrated()
  return useQuery({
    ...inboxQueryOptions(organizationId ?? "", viewerId ?? ""),
    enabled: enabled && hydrated && Boolean(organizationId && viewerId),
  })
}

/**
 * The member's chats with tools waiting for their OK, for anything that marks
 * them (Home, the chat list): `waiting(threadId)` is how many wait in one.
 */
export function usePendingConfirmations(
  organizationId: string | undefined,
  viewerId: string | undefined,
  enabled = true
) {
  const hydrated = useHydrated()
  const query = useQuery({
    ...pendingConfirmationsQueryOptions(organizationId ?? "", viewerId ?? ""),
    enabled: enabled && hydrated && Boolean(organizationId && viewerId),
  })
  const chats: PendingChat[] | undefined =
    query.isError && notServed(query.error)
      ? []
      : authorizedData(query)?.threads
  return {
    query,
    chats,
    waiting: (threadId: string) =>
      chats?.find((chat) => chat.id === threadId)?.pending_confirmations ?? 0,
  }
}

export type NeedsYou = {
  /** Requests waiting on the member, newest first; undefined until the inbox has answered. */
  requests: AgentRequest[] | undefined
  /** Chats with tools waiting; undefined until that read has answered. */
  chats: PendingChat[] | undefined
  /** One per request and per chat; undefined until both reads have answered. */
  count: number | undefined
  /** The inbox read is full, so more may wait behind it. */
  capped: boolean
  inbox: ReturnType<typeof useInbox>
  confirmations: ReturnType<typeof usePendingConfirmations>["query"]
}

/** Everything that waits on the member, and how much. */
export function useNeedsYou(
  organizationId: string | undefined,
  viewerId: string | undefined,
  enabled = true
): NeedsYou {
  const inbox = useInbox(organizationId, viewerId, enabled)
  const confirmations = usePendingConfirmations(
    organizationId,
    viewerId,
    enabled
  )
  const rows = authorizedData(inbox)
  const requests = rows
    ? waitingRequests(rows, viewerId ?? "", Date.now())
    : undefined
  const chats = confirmations.chats
  return {
    requests,
    chats,
    count: requests && chats ? requests.length + chats.length : undefined,
    capped: (rows?.length ?? 0) >= INBOX_LIMIT,
    inbox,
    confirmations: confirmations.query,
  }
}

/**
 * How many things wait on the member: requests to decide or review, and
 * chats with a tool waiting for their OK. Undefined until both reads have
 * answered, so nothing shows a count it does not know.
 */
export function useNeedsYouCount(
  organizationId: string | undefined,
  viewerId: string | undefined
): number | undefined {
  return useNeedsYou(organizationId, viewerId).count
}

/** A count as a badge says it: "100+" when the inbox read is full. */
export function needsYouBadge(needs: NeedsYou): string | undefined {
  if (!needs.count) return undefined
  return needs.capped ? `${needs.count}+` : String(needs.count)
}
