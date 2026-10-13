import type {
  AgentRequest,
  AgentRequestStatus,
} from "@/lib/agents/agent-requests"
import { AGENT_REQUESTS_POLL_MS } from "@/lib/agents/queries"
import { ApiError } from "@/lib/api/client"
import type { RequestEntry } from "./timeline"

/** Refused reads stop; open requests and transient failures are rechecked. */
export function requestCardPollInterval(
  request: AgentRequest | undefined,
  error: unknown
): number | false {
  if (isRequestReadRefused(error)) return false
  return !request ||
    request.status === "pending" ||
    request.status === "approved" ||
    request.status === "review"
    ? AGENT_REQUESTS_POLL_MS
    : false
}

export function isRequestReadRefused(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    (error.status === 401 || error.status === 403 || error.status === 404)
  )
}

const recordedStateWords: Partial<
  Record<NonNullable<RequestEntry["status"]>, string>
> = {
  asked: "Waiting for approval",
  pending: "Waiting for approval",
  approved: "Approved · working",
  held: "Waiting for the answer to be reviewed",
  review: "Waiting for the answer to be reviewed",
  answered: "Answered",
  completed: "Answered",
  declined: "Declined",
  rejected: "Declined",
  closed: "Closed",
  failed: "Failed",
  expired: "Expired",
  withheld: "Not shared",
}

export function requestCardStatus(
  status: AgentRequestStatus | RequestEntry["status"]
): string {
  return status
    ? (recordedStateWords[status] ?? "Request filed")
    : "Request filed"
}

/** Only published results are shown, even if an older API carries a held draft. */
export function requestCardAnswer(request: AgentRequest): string | undefined {
  if (request.status !== "completed" && request.status !== "failed")
    return undefined
  if (!request.result) return undefined
  const summary = request.result.summary.trim()
  const content = request.result.content.trim()
  return (
    [summary, ...(content && content !== summary ? [content] : [])]
      .filter(Boolean)
      .join("\n\n") || undefined
  )
}
