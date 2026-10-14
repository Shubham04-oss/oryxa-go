// A member's persistent agents: their primary agent (Krude's identity for
// them, created on first use) and the specialists they create. Handlers are
// in agents/primary/internal/httpserver/server.go; the rules behind them are
// in agents/primary/internal/managedagents.
import { apiRequest } from "@/lib/api/client"
import { organizationPath, pick } from "./http"
import type { ReadOptions } from "./http"

export type AgentKind = "primary" | "specialist"
export type AgentStatus = "active" | "disabled"

/** The model an agent answers on: lite for a specialist unless set to main. */
export type ModelTier = "lite" | "main"

/** "Every app": a grant's `apps` may be just this. */
export const ALL_APPS = "*"

/**
 * What a specialist may reach in its owner's apps (`runprofile.Grant`).
 * `apps` are toolkit slugs, or `["*"]` for every app; it reads them. It may
 * change things only in `write_apps`. The default is `{apps: ["*"]}`: reads
 * everything, writes nothing.
 */
export type AgentGrant = {
  apps: string[]
  tools?: string[]
  write_apps?: string[]
  write_tools?: string[]
}

/** The most entries one of a grant's lists may hold. */
export const MAX_GRANT_ENTRIES = 200

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
  /** A specialist's grant; a primary has none and reaches all its member does. */
  grant?: AgentGrant
  /** Always main for a primary. Older servers leave it out. */
  model_tier?: ModelTier
}

export type CreateAgentInput = {
  /** 1–60 characters, unique in the organization across every member. */
  name: string
  /** Up to 500 characters. */
  description?: string
  /** Required; up to 20,000 characters. */
  instructions: string
}

/** Send at least one field; the server answers 400 for an empty change. */
export type UpdateAgentInput = {
  name?: string
  description?: string
  /** A specialist's only: a primary's instructions are not editable (400). */
  instructions?: string
  status?: AgentStatus
  /** Specialists only, owner only: replaces the whole grant. */
  grant?: AgentGrant
  /** Specialists only. */
  model_tier?: ModelTier
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

/** `POST …/agents` (server.createAgent): an active specialist owned by the caller, answered 201. */
export function createAgent(
  organizationId: string,
  input: CreateAgentInput
): Promise<ManagedAgent> {
  return apiRequest<ManagedAgent>(agentsPath(organizationId), {
    method: "POST",
    body: pick(input, ["name", "description", "instructions"]),
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
    body: pick(input, [
      "name",
      "description",
      "instructions",
      "status",
      "grant",
      "model_tier",
    ]),
  })
}

/** `DELETE …/agents/{agentID}` (server.deleteAgent): 204; 409 agent_in_use while on a team or teamspace. */
export async function deleteAgent(
  organizationId: string,
  agentId: string
): Promise<void> {
  await apiRequest(agentsPath(organizationId, agentId), { method: "DELETE" })
}
