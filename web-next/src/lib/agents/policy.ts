// Pure helpers that mirror the agents service's own rules, so a page can
// hide what the server would refuse. The server stays the authority: these
// only decide what to offer, and a page still handles the 4xx.
import { ApiError } from "@/lib/api/client"
import type { AgentRequest } from "./agent-requests"
import type { AgentTeam } from "./agent-teams"
import type { WorkflowExecution } from "./agent-workflows"
import type { ManagedAgent } from "./managed-agents"

/** The caller's primary agent; the list endpoint always includes it. */
export function primaryAgent(
  agents: readonly ManagedAgent[]
): ManagedAgent | undefined {
  return agents.find((agent) => agent.kind === "primary")
}

/** The caller's specialists: the list endpoint also returns the primary agent. */
export function specialistAgents(
  agents: readonly ManagedAgent[]
): ManagedAgent[] {
  return agents.filter((agent) => agent.kind === "specialist")
}

/** Specialists a team (or a teamspace) will take: active ones only. */
export function activeSpecialists(
  agents: readonly ManagedAgent[]
): ManagedAgent[] {
  return agents.filter(
    (agent) => agent.kind === "specialist" && agent.status === "active"
  )
}

/** One participant of a team conversation, addressed as `@handle`. */
export type AgentTeamSeat = {
  agent_id: string
  name: string
  role: string
  handle: string
  /** Inactive specialists are never woken by a mention. */
  active: boolean
}

// agentteamrooms.slug: lowercase, spaces to "-", drop anything but a-z, 0-9
// and "-", then trim "-".
function handleSlug(value: string): string {
  return value
    .toLowerCase()
    .replaceAll(" ", "-")
    .replace(/[^a-z0-9-]+/g, "")
    .replace(/^-+|-+$/g, "")
}

/**
 * A team's seats with the handles its conversation routes on, computed as
 * agentteamrooms.participants does: the coordinator is always `@primary`, a
 * specialist is its slugged name, or its slugged id when the name is empty or
 * already taken. The API does not return these handles.
 */
export function agentTeamSeats(team: AgentTeam): AgentTeamSeat[] {
  const seats: AgentTeamSeat[] = [
    {
      agent_id: team.coordinator_agent_id,
      name: team.coordinator_name,
      role: "Coordinator",
      handle: "primary",
      active: true,
    },
  ]
  const used = new Set(["primary"])
  for (const member of team.members) {
    let handle = handleSlug(member.name)
    if (!handle || used.has(handle)) handle = handleSlug(member.agent_id)
    used.add(handle)
    seats.push({
      agent_id: member.agent_id,
      name: member.name,
      role: member.role,
      handle,
      active: member.status === "active",
    })
  }
  return seats
}

/** Queued or running: the execution has not settled yet. */
export function isExecutionInFlight(
  execution: WorkflowExecution | undefined
): boolean {
  return execution?.status === "queued" || execution?.status === "running"
}

/** Whether any listed execution is still queued or running. */
export function hasExecutionInFlight(
  executions: readonly WorkflowExecution[] | undefined
): boolean {
  return executions?.some(isExecutionInFlight) ?? false
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
