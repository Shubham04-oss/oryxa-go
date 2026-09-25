// Teamspaces: one shared space per human team, where teammates' agents
// ("whisperers", one per member) and the specialists their owners contribute
// work, and where finished work is published to a ledger the organization
// agent reads. Who may see or change one is resolved from the live team
// directory on every call. Handlers are in
// agents/primary/internal/httpserver/server.go; the rules are in
// agents/primary/internal/teamspaces.
import { apiRequest } from "@/lib/api/client"
import { organizationPath, pick, withSearch } from "@/lib/agents/http"
import type { ReadOptions } from "@/lib/agents/http"
import type { AgentStatus } from "@/lib/agents/managed-agents"
import type { OrganizationAgent } from "@/lib/agents/organization-agent"

export type TeamspaceStatus = "active" | "disabled"

/**
 * `request` keeps each piece of work behind the owner's decide/respond step;
 * `automatic` lets the organization agent start bounded work without asking.
 */
export type DelegationMode = "request" | "automatic"

/** A teammate's primary agent as the teamspace sees it; nothing private. */
export type Whisperer = {
  user_id: string
  user_display_name?: string
  agent_id: string
  agent_name: string
  mode: "whisperer"
}

/** A specialist its owner made available to the teamspace. */
export type TeamspaceContribution = {
  agent_id: string
  owner_user_id: string
  name: string
  description?: string
  status: AgentStatus
  role: string
  delegation_mode: DelegationMode
  position: number
  created_at: string
}

export type Teamspace = {
  id: string
  organization_id: string
  team_id: string
  team_name: string
  name: string
  description?: string
  created_by_user_id: string
  organization_agent: OrganizationAgent
  whisperers: Whisperer[]
  status: TeamspaceStatus
  revision: number
  contributions: TeamspaceContribution[]
  /** The caller manages the team: may rename, pause or delete the space. */
  can_manage: boolean
  /** The caller is on the team: may contribute, publish and read the ledger. */
  is_member: boolean
  created_at: string
  updated_at: string
}

/** Team managers only. One teamspace per team; names unique in the organization. */
export type CreateTeamspaceInput = {
  team_id: string
  /** 1–100 characters. */
  name: string
  /** Up to 2,000 characters. */
  description?: string
}

/** Team managers only. Send at least one field. */
export type UpdateTeamspaceInput = {
  name?: string
  description?: string
  status?: TeamspaceStatus
}

/** A team member's own active specialist, not already contributed. */
export type AddTeamspaceContributionInput = {
  agent_id: string
  /** Required; up to 500 characters. */
  role: string
  delegation_mode: DelegationMode
}

/** The specialist's owner only. Send at least one field. */
export type UpdateTeamspaceContributionInput = {
  role?: string
  delegation_mode?: DelegationMode
}

export type TeamspaceEntryKind =
  "update" | "result" | "artifact" | "handoff" | "decision"

/** A reference published with an entry. Untrusted: see `safeArtifactHref`. */
export type TeamspaceArtifact = {
  name: string
  /** HTTPS, or a path starting with one "/". */
  uri: string
  media_type?: string
}

/** One immutable publication in a teamspace's ledger. */
export type TeamspaceEntry = {
  id: string
  teamspace_id: string
  organization_id: string
  author_user_id: string
  author_agent_id: string
  author_agent_name: string
  kind: TeamspaceEntryKind
  title: string
  summary: string
  tools_used: string[]
  artifacts: TeamspaceArtifact[]
  created_at: string
}

/** A team member publishing as their whisperer or their own active contribution. */
export type PublishTeamspaceEntryInput = {
  author_agent_id: string
  kind: TeamspaceEntryKind
  /** 1–200 characters. */
  title: string
  /** 1–20,000 characters. */
  summary: string
  /** Up to 100 names of up to 200 characters. */
  tools_used?: string[]
  /** Up to 50. */
  artifacts?: TeamspaceArtifact[]
}

function teamspacesPath(organizationId: string, ...segments: string[]) {
  return organizationPath(organizationId, "teamspaces", ...segments)
}

/** `GET …/teamspaces` (server.listTeamspaces): the spaces of teams the caller is on or manages, by name. */
export async function listTeamspaces(
  organizationId: string,
  { signal }: ReadOptions = {}
): Promise<Teamspace[]> {
  const result = await apiRequest<{ teamspaces: Teamspace[] }>(
    teamspacesPath(organizationId),
    { signal }
  )
  return result.teamspaces
}

/** `GET …/teamspaces/{teamspaceID}` (server.getTeamspace); 404 for a team the caller is neither on nor manages. */
export function getTeamspace(
  organizationId: string,
  teamspaceId: string,
  { signal }: ReadOptions = {}
): Promise<Teamspace> {
  return apiRequest<Teamspace>(teamspacesPath(organizationId, teamspaceId), {
    signal,
  })
}

/** `POST …/teamspaces` (server.createTeamspace): 201; 409 teamspace_conflict when the team has one or the name is taken. */
export function createTeamspace(
  organizationId: string,
  input: CreateTeamspaceInput
): Promise<Teamspace> {
  return apiRequest<Teamspace>(teamspacesPath(organizationId), {
    method: "POST",
    body: pick(input, ["team_id", "name", "description"]),
  })
}

/** `PATCH …/teamspaces/{teamspaceID}` (server.updateTeamspace). */
export function updateTeamspace(
  organizationId: string,
  teamspaceId: string,
  input: UpdateTeamspaceInput
): Promise<Teamspace> {
  return apiRequest<Teamspace>(teamspacesPath(organizationId, teamspaceId), {
    method: "PATCH",
    body: pick(input, ["name", "description", "status"]),
  })
}

/** `DELETE …/teamspaces/{teamspaceID}` (server.deleteTeamspace): 204. */
export async function deleteTeamspace(
  organizationId: string,
  teamspaceId: string
): Promise<void> {
  await apiRequest(teamspacesPath(organizationId, teamspaceId), {
    method: "DELETE",
  })
}

/** `POST …/teamspaces/{teamspaceID}/agents` (server.addTeamspaceContribution): 201 with the whole teamspace. */
export function addTeamspaceContribution(
  organizationId: string,
  teamspaceId: string,
  input: AddTeamspaceContributionInput
): Promise<Teamspace> {
  return apiRequest<Teamspace>(
    teamspacesPath(organizationId, teamspaceId, "agents"),
    {
      method: "POST",
      body: pick(input, ["agent_id", "role", "delegation_mode"]),
    }
  )
}

/** `PATCH …/teamspaces/{teamspaceID}/agents/{agentID}` (server.updateTeamspaceContribution): the whole teamspace. */
export function updateTeamspaceContribution(
  organizationId: string,
  teamspaceId: string,
  agentId: string,
  input: UpdateTeamspaceContributionInput
): Promise<Teamspace> {
  return apiRequest<Teamspace>(
    teamspacesPath(organizationId, teamspaceId, "agents", agentId),
    {
      method: "PATCH",
      body: pick(input, ["role", "delegation_mode"]),
    }
  )
}

/** `DELETE …/teamspaces/{teamspaceID}/agents/{agentID}` (server.deleteTeamspaceContribution): 200 with the whole teamspace, not 204. */
export function deleteTeamspaceContribution(
  organizationId: string,
  teamspaceId: string,
  agentId: string
): Promise<Teamspace> {
  return apiRequest<Teamspace>(
    teamspacesPath(organizationId, teamspaceId, "agents", agentId),
    { method: "DELETE" }
  )
}

/** `GET …/teamspaces/{teamspaceID}/entries[?limit=]` (server.listTeamspaceEntries): newest first; 100 by default, at most 500; members only. */
export async function listTeamspaceEntries(
  organizationId: string,
  teamspaceId: string,
  { limit, signal }: ReadOptions & { limit?: number } = {}
): Promise<TeamspaceEntry[]> {
  const result = await apiRequest<{ entries: TeamspaceEntry[] }>(
    withSearch(teamspacesPath(organizationId, teamspaceId, "entries"), {
      limit,
    }),
    { signal }
  )
  return result.entries
}

/** `POST …/teamspaces/{teamspaceID}/entries` (server.publishTeamspaceEntry): 201 with the entry. */
export function publishTeamspaceEntry(
  organizationId: string,
  teamspaceId: string,
  input: PublishTeamspaceEntryInput
): Promise<TeamspaceEntry> {
  return apiRequest<TeamspaceEntry>(
    teamspacesPath(organizationId, teamspaceId, "entries"),
    {
      method: "POST",
      body: {
        ...pick(input, [
          "author_agent_id",
          "kind",
          "title",
          "summary",
          "tools_used",
        ]),
        ...(input.artifacts
          ? {
              artifacts: input.artifacts.map((artifact) =>
                pick(artifact, ["name", "uri", "media_type"])
              ),
            }
          : {}),
      },
    }
  )
}
