// What each lib/agents mutation changes in the cache. Call the matching
// helper from the mutation's onSuccess. Where the server answers with the
// changed object it is cached at once; lists and whatever embeds the object
// are invalidated, which re-reads only the queries a page is showing.
import type { QueryClient } from "@tanstack/react-query"
import { teamspaceKeys } from "@/lib/teamspaces/queries"
import type { AgentRequest } from "./agent-requests"
import { mergeAgentTeamMessages } from "./agent-teams"
import type { AgentTeam, AgentTeamMessage } from "./agent-teams"
import type { AgentWorkflow, WorkflowExecution } from "./agent-workflows"
import type { ManagedAgent } from "./managed-agents"
import type { OrganizationAgent } from "./organization-agent"
import { agentKeys } from "./queries"

/** After `createAgent`: the new specialist joins the agent list. */
export async function afterCreateAgent(
  queryClient: QueryClient,
  organizationId: string
): Promise<void> {
  await queryClient.invalidateQueries({
    queryKey: agentKeys.agentList(organizationId),
  })
}

/**
 * After `updateAgent`. Teams show each member's live name, description and
 * status and the coordinator's name; teamspaces show whisperers (primary
 * agents) and contributed specialists the same way, and so do room seats.
 * All of them are refreshed.
 */
export async function afterUpdateAgent(
  queryClient: QueryClient,
  organizationId: string,
  updated: ManagedAgent
): Promise<void> {
  queryClient.setQueryData(agentKeys.agent(organizationId, updated.id), updated)
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: agentKeys.agentList(organizationId),
    }),
    queryClient.invalidateQueries({
      queryKey: agentKeys.teams(organizationId),
    }),
    queryClient.invalidateQueries({
      queryKey: teamspaceKeys.all(organizationId),
    }),
  ])
}

/**
 * After `deleteAgent`. The server refuses to delete a specialist that is on
 * a team or in a teamspace, so nothing else still shows it. Its own threads
 * belong to lib/specialists and are not touched here.
 */
export async function afterDeleteAgent(
  queryClient: QueryClient,
  organizationId: string,
  agentId: string
): Promise<void> {
  queryClient.removeQueries({
    queryKey: agentKeys.agent(organizationId, agentId),
  })
  await queryClient.invalidateQueries({
    queryKey: agentKeys.agentList(organizationId),
  })
}

/** After `createAgentTeam`. */
export async function afterCreateAgentTeam(
  queryClient: QueryClient,
  organizationId: string,
  created: AgentTeam
): Promise<void> {
  queryClient.setQueryData(agentKeys.team(organizationId, created.id), created)
  await queryClient.invalidateQueries({
    queryKey: agentKeys.teamList(organizationId),
  })
}

/** After `updateAgentTeam`. */
export async function afterUpdateAgentTeam(
  queryClient: QueryClient,
  organizationId: string,
  updated: AgentTeam
): Promise<void> {
  queryClient.setQueryData(agentKeys.team(organizationId, updated.id), updated)
  await queryClient.invalidateQueries({
    queryKey: agentKeys.teamList(organizationId),
  })
}

/** After `deleteAgentTeam`: drops the team, its conversation and its thread pointers. */
export async function afterDeleteAgentTeam(
  queryClient: QueryClient,
  organizationId: string,
  teamId: string
): Promise<void> {
  queryClient.removeQueries({
    queryKey: agentKeys.team(organizationId, teamId),
  })
  await queryClient.invalidateQueries({
    queryKey: agentKeys.teamList(organizationId),
  })
}

/**
 * After `postAgentTeamMessage`: shows the member's line and every reply at
 * once, then re-reads the conversation and the thread pointers, which the
 * agents that answered may have just created.
 */
export async function afterPostAgentTeamMessage(
  queryClient: QueryClient,
  organizationId: string,
  teamId: string,
  added: AgentTeamMessage[]
): Promise<void> {
  queryClient.setQueryData<AgentTeamMessage[]>(
    agentKeys.teamMessages(organizationId, teamId),
    // Only into a conversation already read: a cache seeded with just these
    // lines would pass for the whole conversation.
    (held) => held && mergeAgentTeamMessages(held, added)
  )
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: agentKeys.teamMessages(organizationId, teamId),
    }),
    queryClient.invalidateQueries({
      queryKey: agentKeys.teamThreads(organizationId, teamId),
    }),
  ])
}

/** After `createAgentWorkflow`. */
export async function afterCreateAgentWorkflow(
  queryClient: QueryClient,
  organizationId: string,
  created: AgentWorkflow
): Promise<void> {
  queryClient.setQueryData(
    agentKeys.workflow(organizationId, created.id),
    created
  )
  await queryClient.invalidateQueries({
    queryKey: agentKeys.workflowList(organizationId),
  })
}

/** After `updateAgentWorkflow`. */
export async function afterUpdateAgentWorkflow(
  queryClient: QueryClient,
  organizationId: string,
  updated: AgentWorkflow
): Promise<void> {
  queryClient.setQueryData(
    agentKeys.workflow(organizationId, updated.id),
    updated
  )
  await queryClient.invalidateQueries({
    queryKey: agentKeys.workflowList(organizationId),
  })
}

/** After `deleteAgentWorkflow`: drops the workflow and its execution lists. */
export async function afterDeleteAgentWorkflow(
  queryClient: QueryClient,
  organizationId: string,
  workflowId: string
): Promise<void> {
  queryClient.removeQueries({
    queryKey: agentKeys.workflow(organizationId, workflowId),
  })
  await queryClient.invalidateQueries({
    queryKey: agentKeys.workflowList(organizationId),
  })
}

/**
 * After `startAgentWorkflow`: caches the queued execution and re-reads the
 * workflow's executions, whose 2 s polling then runs until it settles.
 */
export async function afterStartAgentWorkflow(
  queryClient: QueryClient,
  organizationId: string,
  execution: WorkflowExecution
): Promise<void> {
  queryClient.setQueryData(
    agentKeys.execution(organizationId, execution.id),
    execution
  )
  await queryClient.invalidateQueries({
    queryKey: agentKeys.workflowExecutions(
      organizationId,
      execution.workflow_id
    ),
  })
}

async function afterAgentRequestChange(
  queryClient: QueryClient,
  organizationId: string,
  updated: AgentRequest
): Promise<void> {
  queryClient.setQueryData(
    agentKeys.request(organizationId, updated.id),
    updated
  )
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: agentKeys.requestBox(organizationId, "inbox"),
    }),
    queryClient.invalidateQueries({
      queryKey: agentKeys.requestBox(organizationId, "sent"),
    }),
  ])
}

/** After `decideAgentRequest`: caches the request and refreshes both boxes. */
export function afterDecideAgentRequest(
  queryClient: QueryClient,
  organizationId: string,
  updated: AgentRequest
): Promise<void> {
  return afterAgentRequestChange(queryClient, organizationId, updated)
}

/** After `respondToAgentRequest`: caches the request and refreshes both boxes. */
export function afterRespondToAgentRequest(
  queryClient: QueryClient,
  organizationId: string,
  updated: AgentRequest
): Promise<void> {
  return afterAgentRequestChange(queryClient, organizationId, updated)
}

/**
 * After `replyToAgentRequest`: caches the request as its owner now reads it
 * (answered, with their own words) and refreshes both boxes.
 */
export function afterReplyToAgentRequest(
  queryClient: QueryClient,
  organizationId: string,
  updated: AgentRequest
): Promise<void> {
  return afterAgentRequestChange(queryClient, organizationId, updated)
}

/**
 * After `updateOrganizationAgent`. Every teamspace embeds the organization
 * agent, and its status decides whether work can be published there, so
 * teamspaces are refreshed too.
 */
export async function afterUpdateOrganizationAgent(
  queryClient: QueryClient,
  organizationId: string,
  updated: OrganizationAgent
): Promise<void> {
  queryClient.setQueryData(agentKeys.organizationAgent(organizationId), updated)
  await queryClient.invalidateQueries({
    queryKey: teamspaceKeys.all(organizationId),
  })
}
