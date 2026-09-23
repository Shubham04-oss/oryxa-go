// A member's own agent teams: their primary agent coordinating 1–16 of their
// specialists, and the group conversation each team holds. Handlers are in
// agents/primary/internal/httpserver/server.go; the rules are in
// agents/primary/internal/agentteams and agentteamrooms.
import { apiRequest } from "@/lib/api/client"
import { organizationPath, pick, withSearch } from "./http"
import type { ReadOptions } from "./http"
import type { AgentStatus } from "./managed-agents"

export type AgentTeamStatus = "active" | "disabled"

/** A specialist on a team, with its live name, description and status. */
export type AgentTeamMember = {
  agent_id: string
  name: string
  description?: string
  status: AgentStatus
  role: string
  position: number
}

export type AgentTeam = {
  id: string
  organization_id: string
  owner_user_id: string
  name: string
  description: string
  /** Always the owner's primary agent. */
  coordinator_agent_id: string
  coordinator_name: string
  status: AgentTeamStatus
  revision: number
  members: AgentTeamMember[]
  created_at: string
  updated_at: string
}

/** An active specialist of the caller's; each agent at most once. */
export type AgentTeamMemberInput = {
  agent_id: string
  /** Required; up to 300 characters. */
  role: string
}

export type CreateAgentTeamInput = {
  /** 1–80 characters, unique among the caller's teams. */
  name: string
  /** Up to 1,000 characters. */
  description?: string
  /** 1–16 specialists. */
  members: AgentTeamMemberInput[]
}

/** Send at least one field. `members` replaces the whole roster. */
export type UpdateAgentTeamInput = {
  name?: string
  description?: string
  status?: AgentTeamStatus
  members?: AgentTeamMemberInput[]
}

/** One line in a team's conversation: the member's, or an agent's. */
export type AgentTeamMessage = {
  id: string
  team_id: string
  seq: number
  kind: "member" | "agent"
  author_name: string
  author_agent_id?: string
  body: string
  in_reply_to_seq?: number
  created_at: string
}

/** Each team agent's own thread in this team, by agent id. */
export type AgentTeamThreads = Record<string, string>

function teamsPath(organizationId: string, ...segments: string[]) {
  return organizationPath(organizationId, "agent-teams", ...segments)
}

function memberBodies(members: AgentTeamMemberInput[]) {
  return members.map((member) => pick(member, ["agent_id", "role"]))
}

/** `GET …/agent-teams` (server.listAgentTeams): the caller's teams by name. */
export async function listAgentTeams(
  organizationId: string,
  { signal }: ReadOptions = {}
): Promise<AgentTeam[]> {
  const result = await apiRequest<{ teams: AgentTeam[] }>(
    teamsPath(organizationId),
    { signal }
  )
  return result.teams
}

/** `GET …/agent-teams/{teamID}` (server.getAgentTeam). */
export function getAgentTeam(
  organizationId: string,
  teamId: string,
  { signal }: ReadOptions = {}
): Promise<AgentTeam> {
  return apiRequest<AgentTeam>(teamsPath(organizationId, teamId), { signal })
}

/** `POST …/agent-teams` (server.createAgentTeam): 201 with the team. */
export function createAgentTeam(
  organizationId: string,
  input: CreateAgentTeamInput
): Promise<AgentTeam> {
  return apiRequest<AgentTeam>(teamsPath(organizationId), {
    method: "POST",
    body: {
      ...pick(input, ["name", "description"]),
      members: memberBodies(input.members),
    },
  })
}

/** `PATCH …/agent-teams/{teamID}` (server.updateAgentTeam). */
export function updateAgentTeam(
  organizationId: string,
  teamId: string,
  input: UpdateAgentTeamInput
): Promise<AgentTeam> {
  return apiRequest<AgentTeam>(teamsPath(organizationId, teamId), {
    method: "PATCH",
    body: {
      ...pick(input, ["name", "description", "status"]),
      ...(input.members ? { members: memberBodies(input.members) } : {}),
    },
  })
}

/** `DELETE …/agent-teams/{teamID}` (server.deleteAgentTeam): 204; 409 team_in_use while a workflow runs it. */
export async function deleteAgentTeam(
  organizationId: string,
  teamId: string
): Promise<void> {
  await apiRequest(teamsPath(organizationId, teamId), { method: "DELETE" })
}

/** `GET …/agent-teams/{teamID}/messages[?after_seq=]` (server.readAgentTeamRoom): the latest 200, or up to 200 after `afterSeq`, oldest first. */
export async function listAgentTeamMessages(
  organizationId: string,
  teamId: string,
  { afterSeq, signal }: ReadOptions & { afterSeq?: number } = {}
): Promise<AgentTeamMessage[]> {
  const result = await apiRequest<{ messages: AgentTeamMessage[] }>(
    withSearch(teamsPath(organizationId, teamId, "messages"), {
      after_seq: afterSeq,
    }),
    { signal }
  )
  return result.messages
}

/** `POST …/agent-teams/{teamID}/messages` (server.postAgentTeamRoom): the member's line and every agent reply; blocks while agents answer. */
export async function postAgentTeamMessage(
  organizationId: string,
  teamId: string,
  body: string
): Promise<AgentTeamMessage[]> {
  const result = await apiRequest<{ messages: AgentTeamMessage[] }>(
    teamsPath(organizationId, teamId, "messages"),
    { method: "POST", body: { body } }
  )
  return result.messages
}

/** `GET …/agent-teams/{teamID}/agent-threads` (server.agentTeamRoomThreads): which thread each agent answers this team in. */
export async function getAgentTeamThreads(
  organizationId: string,
  teamId: string,
  { signal }: ReadOptions = {}
): Promise<AgentTeamThreads> {
  const result = await apiRequest<{ threads: AgentTeamThreads }>(
    teamsPath(organizationId, teamId, "agent-threads"),
    { signal }
  )
  return result.threads
}

/** Folds new team messages into held ones: one per id, in `seq` order. */
export function mergeAgentTeamMessages(
  held: readonly AgentTeamMessage[],
  arrived: readonly AgentTeamMessage[]
): AgentTeamMessage[] {
  const byId = new Map(held.map((message) => [message.id, message]))
  for (const message of arrived) byId.set(message.id, message)
  return [...byId.values()].sort((left, right) => left.seq - right.seq)
}
