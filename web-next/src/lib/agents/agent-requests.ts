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

/** `expired` is computed when read: a pending or approved request past `expires_at`. */
export type AgentRequestStatus =
  "pending" | "approved" | "rejected" | "completed" | "failed" | "expired"

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
  message: string
  allowed_tools: string[]
  granted_permissions: string[]
  decided_at: string
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
  requested_tools: string[]
  requested_permissions: string[]
  status: AgentRequestStatus
  decision?: AgentRequestDecision
  result?: AgentRequestResult
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
