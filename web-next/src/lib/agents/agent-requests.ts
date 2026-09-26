// Cross-member agent requests ("interactions"): one member's agent asks
// another member's agent for bounded work, the target member approves or
// rejects it, then answers it. Over HTTP the caller always acts as their
// primary agent, so the inbox holds every request addressed to any of their
// agents. Handlers are in agents/primary/internal/httpserver/server.go; the
// rules are in agents/primary/internal/interactions.
import { apiRequest } from "@/lib/api/client"
import { organizationPath, pick, withSearch } from "./http"
import type { ReadOptions } from "./http"

export type AgentRequestBox = "inbox" | "sent"

/**
 * `expired` is computed when read: a pending or approved request past
 * `expires_at`. `review` holds a finished answer the answer guard found
 * personal data in until its target member reviews it (not terminal);
 * `withheld` is one they chose not to share, or one that expired before
 * they reviewed it.
 */
export type AgentRequestStatus =
  | "pending"
  | "approved"
  | "review"
  | "rejected"
  | "completed"
  | "failed"
  | "expired"
  | "withheld"

/**
 * The toolboxes a request may ask for (`requested_tools`) and its approval
 * allow (`allowed_tools`, a subset): `apps`, the target member's connected
 * apps, read-only, and `knowledge`, the team's knowledge search. Go takes
 * other names too, but they grant nothing.
 */
export const AGENT_REQUEST_TOOLBOXES = ["apps", "knowledge"] as const

export type AgentRequestToolbox = (typeof AGENT_REQUEST_TOOLBOXES)[number]

/**
 * What the answer guard found in a result another member reads: nothing
 * (`clear`), only kinds it always masks (`masked`, delivered masked), or a
 * kind it holds for the target member's review (`held`, status `review`).
 * `kinds` are entity type names only (`EMAIL`, `PHONE`, …), never what
 * matched; `UNCHECKED` when the check itself failed.
 */
export type AgentRequestGuard = {
  state: "clear" | "masked" | "held"
  /** Absent when it found nothing; for a held answer, only the kinds it holds it for. */
  kinds?: string[]
}

/** The target member's choice for a held answer: share it, share it fully masked, or not at all. */
export type AgentRequestReviewAction = "send" | "send_masked" | "withhold"

/** Who recorded a decision or a review: the member (`kind: "member"`), an agent, or Oryxa (`system`). */
export type AgentRequestDecider = {
  kind: string
  user_id?: string
  agent_id?: string
}

/**
 * The target member's review of a held answer: made by them (`decided_by`
 * a `member`), or a withholding by Oryxa (`system`), with its `reason`:
 * "expired before review" (its `decided_at` the request's expiry), or
 * "Oryxa withheld this answer because …" when the request no longer stood
 * in its teamspace as it was to be sent.
 */
export type AgentRequestReview = {
  action: AgentRequestReviewAction
  decided_by: AgentRequestDecider
  decided_at: string
  reason?: string
}

export type AgentRequestParty = {
  user_id: string
  agent_id: string
  agent_name: string
  user_email?: string
  user_display_name?: string
}

export type AgentRequestContextItem = { label: string; value: string }

export type AgentRequestDecision = {
  decision: "approved" | "rejected"
  /**
   * A member's own words; an agent's decision reads in fixed words
   * ("Approved by their agent." or "Declined by their agent."), and a
   * request Oryxa closed says why ("Oryxa closed this request because …").
   */
  message: string
  allowed_tools: string[]
  granted_permissions: string[]
  decided_at: string
  /** Who decided: absent from a decision an older build recorded. */
  decided_by?: AgentRequestDecider
}

export type AgentRequestResult = {
  status: "completed" | "failed"
  summary: string
  content: string
  tools_used: string[]
  artifacts: string[]
  completed_at: string
}

/** One request (`agents.Interaction`). */
export type AgentRequest = {
  id: string
  organization_id: string
  /** The teamspace it was made in; absent for a request made outside one. */
  teamspace_id?: string
  /**
   * Who decides it, fixed when it was made: `agent` (the addressed agent, or
   * its member's primary agent), or `owner` (only the target member, in
   * their own session: a teammate's primary agent, or a specialist
   * contributed with `request` delegation). Anything but `agent` is held for
   * the owner.
   */
  approval?: "agent" | "owner"
  requester: AgentRequestParty
  target: AgentRequestParty
  purpose: string
  task: string
  context: AgentRequestContextItem[]
  /** Toolbox names (`AGENT_REQUEST_TOOLBOXES`): as the requester named them, or as Jev judged them (`tools_judged_by`). */
  requested_tools: string[]
  /** Who chose `requested_tools`: `jev`, the `requester`, or `none` (Jev unset, unsure or failing: no tools). */
  tools_judged_by?: "jev" | "requester" | "none"
  requested_permissions: string[]
  status: AgentRequestStatus
  decision?: AgentRequestDecision
  /**
   * Absent for the requester while the answer is in `review`, and once it
   * is `withheld`. While it is in review the target member's reads carry
   * the held answer instead, which `src/lib/side/asking.ts` reads
   * (`HELD_ANSWER_FIELDS`).
   */
  result?: AgentRequestResult
  /** The answer guard's finding, once the target's agent answered: the requester reads it only beside a result. */
  guard?: AgentRequestGuard
  /** The target member's review of a held answer, once they made it. */
  review?: AgentRequestReview
  revision: number
  expires_at: string
  created_at: string
  updated_at: string
}

/** Only the target member decides, and only while the request is pending. */
export type DecideAgentRequestInput = {
  decision: "approved" | "rejected"
  /** Required; up to 4,000 characters. */
  message: string
  /** A subset of `requested_tools`; nothing when rejecting. */
  allowed_tools?: string[]
  /** A subset of `requested_permissions`; nothing when rejecting. */
  granted_permissions?: string[]
}

/** Only the target member answers, and only once the request is approved. */
export type RespondToAgentRequestInput = {
  status: "completed" | "failed"
  /** Required; up to 4,000 characters. */
  summary: string
  /** Up to 20,000 characters. */
  content?: string
  /** Within the decision's `allowed_tools`; up to 32 entries of 1–120 characters. */
  tools_used?: string[]
  /** Up to 32 entries of 1–120 characters. */
  artifacts?: string[]
}

function requestsPath(organizationId: string, ...segments: string[]) {
  return organizationPath(organizationId, "agent-requests", ...segments)
}

/** `GET …/agent-requests/{inbox|sent}[?limit=]` (server.interactionInbox, server.interactionSent): latest activity first; 20 by default, at most 100. */
export async function listAgentRequests(
  organizationId: string,
  box: AgentRequestBox,
  { limit, signal }: ReadOptions & { limit?: number } = {}
): Promise<AgentRequest[]> {
  const result = await apiRequest<{ requests: AgentRequest[] }>(
    withSearch(requestsPath(organizationId, box), { limit }),
    { signal }
  )
  return result.requests
}

/** `GET …/agent-requests/{requestID}` (server.getInteraction); 404 unless the caller sent or received it. */
export function getAgentRequest(
  organizationId: string,
  requestId: string,
  { signal }: ReadOptions = {}
): Promise<AgentRequest> {
  return apiRequest<AgentRequest>(requestsPath(organizationId, requestId), {
    signal,
  })
}

/** `POST …/agent-requests/{requestID}/decision` (server.decideInteraction). */
export function decideAgentRequest(
  organizationId: string,
  requestId: string,
  input: DecideAgentRequestInput
): Promise<AgentRequest> {
  return apiRequest<AgentRequest>(
    requestsPath(organizationId, requestId, "decision"),
    {
      method: "POST",
      body: pick(input, [
        "decision",
        "message",
        "allowed_tools",
        "granted_permissions",
      ]),
    }
  )
}

/** `POST …/agent-requests/{requestID}/response` (server.respondInteraction). */
export function respondToAgentRequest(
  organizationId: string,
  requestId: string,
  input: RespondToAgentRequestInput
): Promise<AgentRequest> {
  return apiRequest<AgentRequest>(
    requestsPath(organizationId, requestId, "response"),
    {
      method: "POST",
      body: pick(input, [
        "status",
        "summary",
        "content",
        "tools_used",
        "artifacts",
      ]),
    }
  )
}
