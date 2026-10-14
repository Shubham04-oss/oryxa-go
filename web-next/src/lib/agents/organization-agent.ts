// The organization's one shared agent, which reads what teamspaces publish
// and keeps an organization-wide synthesis with proposed follow-ups. Every
// member may read it; owners and admins may change it. Handlers are in
// agents/primary/internal/httpserver/server.go; the rules are in
// agents/primary/internal/teamspaces and organizationagents.
import { apiRequest } from "@/lib/api/client"
import { organizationPath, pick, withSearch } from "./http"
import type { ReadOptions } from "./http"

export type OrganizationAgentStatus = "active" | "disabled"

export type OrganizationAgent = {
  id: string
  organization_id: string
  name: string
  description?: string
  instructions: string
  status: OrganizationAgentStatus
  revision: number
  /** Whether the caller is an organization owner or admin. */
  can_manage: boolean
  created_at: string
  updated_at: string
}

/** Owners and admins only (403 otherwise). Send at least one field. */
export type UpdateOrganizationAgentInput = {
  /** 1–100 characters. */
  name?: string
  /** Up to 2,000 characters. */
  description?: string
  /** Must stay non-empty; up to 20,000 characters. */
  instructions?: string
  status?: OrganizationAgentStatus
}

/**
 * A synthesis: the organization-wide one, or one teamspace's (with
 * `teamspace_id`), which only its team and the organization's owners and
 * admins may read. Before anything was synthesized it is empty: revision 0,
 * `agent_id` "", no `updated_at`.
 */
export type OrganizationContext = {
  organization_id: string
  /** The teamspace whose team it is for; none for the organization's. */
  teamspace_id?: string
  agent_id: string
  revision: number
  summary: string
  decisions: string[]
  risks: string[]
  open_questions: string[]
  source_entry_id?: string
  updated_at?: string
}

export type OrganizationFollowUp = {
  id: string
  organization_id: string
  synthesis_revision: number
  source_entry_id: string
  teamspace_id: string
  target_agent_id?: string
  task: string
  rationale: string
  /** "proposed" is the only status the service writes today. */
  status: string
  created_at: string
}

function organizationAgentPath(organizationId: string, ...segments: string[]) {
  return organizationPath(organizationId, "organization-agent", ...segments)
}

/** `GET …/organization-agent` (server.getOrganizationAgent): created on first read. */
export function getOrganizationAgent(
  organizationId: string,
  { signal }: ReadOptions = {}
): Promise<OrganizationAgent> {
  return apiRequest<OrganizationAgent>(organizationAgentPath(organizationId), {
    signal,
  })
}

/** `PATCH …/organization-agent` (server.updateOrganizationAgent). */
export function updateOrganizationAgent(
  organizationId: string,
  input: UpdateOrganizationAgentInput
): Promise<OrganizationAgent> {
  return apiRequest<OrganizationAgent>(organizationAgentPath(organizationId), {
    method: "PATCH",
    body: pick(input, ["name", "description", "instructions", "status"]),
  })
}

/** `GET …/organization-agent/context` (server.getOrganizationAgentContext). */
export function getOrganizationContext(
  organizationId: string,
  { signal }: ReadOptions = {}
): Promise<OrganizationContext> {
  return apiRequest<OrganizationContext>(
    organizationAgentPath(organizationId, "context"),
    { signal }
  )
}

/**
 * `GET …/teamspaces/{teamspaceID}/synthesis` (server.getTeamspaceSynthesis):
 * the team's own synthesis, built from posts only that team may read. 403
 * for anyone else, who sees no panel.
 */
export function getTeamspaceSynthesis(
  organizationId: string,
  teamspaceId: string,
  { signal }: ReadOptions = {}
): Promise<OrganizationContext> {
  return apiRequest<OrganizationContext>(
    organizationPath(organizationId, "teamspaces", teamspaceId, "synthesis"),
    { signal }
  )
}

/** `GET …/organization-agent/follow-ups[?limit=]` (server.listOrganizationAgentFollowUps): newest first; 100 by default, at most 500. Only those from the caller's teams' teamspaces, or every one for an owner or admin. */
export async function listOrganizationFollowUps(
  organizationId: string,
  { limit, signal }: ReadOptions & { limit?: number } = {}
): Promise<OrganizationFollowUp[]> {
  const result = await apiRequest<{ follow_ups: OrganizationFollowUp[] }>(
    withSearch(organizationAgentPath(organizationId, "follow-ups"), { limit }),
    { signal }
  )
  return result.follow_ups
}
