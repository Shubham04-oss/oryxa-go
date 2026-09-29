// Where a request in a DM's log stands, read from Go as the member. A note
// (REALTIME.md, "Request notes") is only Go's word to the Worker that a
// request moved, carrying identities and a state: it is a hint, and what a
// line shows is what Go says to the member's own session when the line is
// on the page. So each line reads its request (`GET …/agent-requests/{id}`,
// as the member), again as its revision changes, and, while the request can
// still move, again when it is due to expire, which no event says. Go
// refusing the member the request (404, or 403) hides the line: it is not
// theirs to show. Until Go has answered, or when it cannot, the note's word
// stands. A request that can no longer move cannot change, so its read is
// kept for a good while (`settledStaleMs`), and the two views of one DM a
// page may hold (the canvas and the panel) share it.
import { useQuery } from "@tanstack/react-query"
import { getAgentRequest } from "@/lib/agents/agent-requests"
import type {
  AgentRequest,
  AgentRequestStatus,
} from "@/lib/agents/agent-requests"
import { ApiError } from "@/lib/api/client"
import { isOpenRequestState } from "./request-notes"
import type { DmRequestNote, RequestLineState } from "./request-notes"

/** Go's status for a request, as a line says it. */
const stateOfStatus: Record<AgentRequestStatus, RequestLineState> = {
  pending: "waiting",
  approved: "approved",
  review: "held",
  rejected: "declined",
  completed: "answered",
  failed: "failed",
  expired: "expired",
  withheld: "withheld",
}

/** The least, and the most, a request that can still move waits to be read again for its expiry. */
const recheckBounds = { minMs: 5_000, maxMs: 60 * 60_000 }

/** How long a read of a request that can no longer move is kept before a view asks again. */
export const settledStaleMs = 10 * 60_000

/** How long until an open request expires, plus a moment: when to read it again, or never for one that is settled. */
export function recheckIn(
  request: AgentRequest | undefined,
  now = Date.now()
): number | false {
  if (!request) return false
  const state = stateOfStatus[request.status]
  const expiresAt = Date.parse(request.expires_at)
  if (!isOpenRequestState(state) || Number.isNaN(expiresAt)) return false
  return Math.min(
    Math.max(expiresAt - now + 1_000, recheckBounds.minMs),
    recheckBounds.maxMs
  )
}

/** Where a note's request stands for the member. */
export type RequestStanding = {
  /** Go's word once it has answered, the note's until then. */
  state: RequestLineState
  /** Go will not show the member this request: its line is not theirs to show. */
  hidden: boolean
}

/**
 * A note's request as the member's own session sees it. `organizationId` is
 * the organization the DM is in; without it nothing is read and the note's
 * word stands.
 */
export function useRequestStanding(
  organizationId: string | undefined,
  note: DmRequestNote
): RequestStanding {
  const query = useQuery({
    // One read for each revision: the line reads again as the request moves.
    queryKey: [
      "dms",
      organizationId,
      "request-standing",
      note.requestId,
      note.revision,
    ],
    queryFn: ({ signal }) =>
      getAgentRequest(organizationId ?? "", note.requestId, { signal }),
    enabled: organizationId !== undefined,
    // What cannot change is not asked again for a while.
    ...(isOpenRequestState(note.state) ? {} : { staleTime: settledStaleMs }),
    // A refusal is an answer, and any other failure leaves the note's word.
    retry: false,
    refetchInterval: (current) => recheckIn(current.state.data),
  })
  if (
    query.error instanceof ApiError &&
    (query.error.status === 404 || query.error.status === 403)
  ) {
    return { state: note.state, hidden: true }
  }
  const status = query.data?.status
  return { state: status ? stateOfStatus[status] : note.state, hidden: false }
}
