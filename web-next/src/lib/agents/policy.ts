// Pure helpers that mirror the agents service's own rules, so a page can
// hide what the server would refuse. The server stays the authority: these
// only decide what to offer, and a page still handles the 4xx.
import { ApiError } from "@/lib/api/client"
import type { AgentRequest } from "./agent-requests"
import type { ManagedAgent } from "./managed-agents"

/** The caller's primary agent; the list endpoint always includes it. */
export function primaryAgent(
  agents: readonly ManagedAgent[]
): ManagedAgent | undefined {
  return agents.find((agent) => agent.kind === "primary")
}

function unexpired(request: AgentRequest, now: number): boolean {
  return Date.parse(request.expires_at) > now
}

/** Whether `userId` may approve or reject the request now: its target, while pending. */
export function canDecideAgentRequest(
  request: AgentRequest,
  userId: string,
  now = Date.now()
): boolean {
  return (
    request.target.user_id === userId &&
    request.status === "pending" &&
    unexpired(request, now)
  )
}

/** Whether `userId` may answer the request now: its target, once approved. */
export function canRespondToAgentRequest(
  request: AgentRequest,
  userId: string,
  now = Date.now()
): boolean {
  return (
    request.target.user_id === userId &&
    request.status === "approved" &&
    request.decision?.decision === "approved" &&
    unexpired(request, now)
  )
}

// The 503 codes that mean "this part of the agents service is not set up
// here", as opposed to a failure worth retrying (plain "unavailable" is auth
// being down).
const unavailableHere = new Set([
  "agents_unavailable",
  "agent_teams_unavailable",
  "agent_team_rooms_unavailable",
  "agent_workflows_unavailable",
  "interactions_unavailable",
  "organization_agent_unavailable",
  "preferences_unavailable",
  "rooms_unavailable",
  "teamspaces_unavailable",
])

/** Whether an error says this server does not run that feature, rather than that a request failed. */
export function isUnavailableHere(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    error.status === 503 &&
    error.code !== undefined &&
    unavailableHere.has(error.code)
  )
}
