// A member's persistent agents: their primary agent (Krude's identity for
// them, created on first use). The service also keeps specialists the member
// creates; the page does not offer them (they were archived with
// `archive/pre-simplify-2026-09-30`). Handlers are in
// agents/primary/internal/httpserver/server.go; the rules behind them are in
// agents/primary/internal/managedagents.
import { apiRequest } from "@/lib/api/client"
import { organizationPath, pick } from "./http"
import type { ReadOptions } from "./http"

export type AgentKind = "primary" | "specialist"
export type AgentStatus = "active" | "disabled"

/** One of the caller's own agents (`managedagents.Definition`). */
export type ManagedAgent = {
  id: string
  organization_id: string
  owner_user_id: string
  kind: AgentKind
  name: string
  description: string
  instructions: string
  status: AgentStatus
  revision: number
  created_at: string
  updated_at: string
}

/** Send at least one field; the server answers 400 for an empty change. */
export type UpdateAgentInput = {
  name?: string
  description?: string
  instructions?: string
  status?: AgentStatus
}

function agentsPath(organizationId: string, ...segments: string[]) {
  return organizationPath(organizationId, "agents", ...segments)
}

/** `GET …/agents` (server.listAgents): the primary agent first, then specialists by name. */
export async function listAgents(
  organizationId: string,
  { signal }: ReadOptions = {}
): Promise<ManagedAgent[]> {
  const result = await apiRequest<{ agents: ManagedAgent[] }>(
    agentsPath(organizationId),
    { signal }
  )
  return result.agents
}

/** `GET …/agents/{agentID}` (server.getAgent); 404 for anybody else's agent. */
export function getAgent(
  organizationId: string,
  agentId: string,
  { signal }: ReadOptions = {}
): Promise<ManagedAgent> {
  return apiRequest<ManagedAgent>(agentsPath(organizationId, agentId), {
    signal,
  })
}

/** `PATCH …/agents/{agentID}` (server.updateAgent). */
export function updateAgent(
  organizationId: string,
  agentId: string,
  input: UpdateAgentInput
): Promise<ManagedAgent> {
  return apiRequest<ManagedAgent>(agentsPath(organizationId, agentId), {
    method: "PATCH",
    body: pick(input, ["name", "description", "instructions", "status"]),
  })
}
