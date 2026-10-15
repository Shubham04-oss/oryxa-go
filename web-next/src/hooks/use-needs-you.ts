import { useQuery } from "@tanstack/react-query"
import { useHydrated } from "@tanstack/react-router"
import { waitsOnOwner } from "@/components/request-card-format"
import {
  AGENT_REQUESTS_POLL_MS,
  agentRequestsQueryOptions,
} from "@/lib/agents/queries"
import { authorizedData, homeRead } from "@/lib/home/queries"

/** The most requests one read looks at, as My workspace reads them. */
export const NEEDS_YOU_LIMIT = 100

/**
 * How many requests wait on the member: to decide, or to review an answer
 * held for them. It reads the inbox the way My workspace does (the same
 * entry in the cache, so the two never disagree), and is undefined until
 * that read has answered, so nothing shows a count it does not know.
 */
export function useNeedsYouCount(
  organizationId: string | undefined,
  viewerId: string | undefined
): number | undefined {
  const hydrated = useHydrated()
  const org = organizationId ?? ""
  const viewer = viewerId ?? ""
  const inbox = useQuery({
    ...homeRead(
      agentRequestsQueryOptions(org, "inbox", NEEDS_YOU_LIMIT),
      viewer
    ),
    enabled: hydrated && Boolean(organizationId && viewerId),
    refetchInterval: AGENT_REQUESTS_POLL_MS,
  })
  const rows = authorizedData(inbox)
  if (!rows) return undefined
  const now = Date.now()
  return rows.filter((request) => waitsOnOwner(request, viewer, now)).length
}
