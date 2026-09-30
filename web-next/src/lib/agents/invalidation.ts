// What each lib/agents mutation changes in the cache. Call the matching
// helper from the mutation's onSuccess. Where the server answers with the
// changed object it is cached at once; lists and whatever embeds the object
// are invalidated, which re-reads only the queries a page is showing.
import type { QueryClient } from "@tanstack/react-query"
import type { AgentRequest } from "./agent-requests"
import type { ManagedAgent } from "./managed-agents"
import type { OrganizationAgent } from "./organization-agent"
import { agentKeys } from "./queries"

/** After `updateAgent`: the agent is cached at once, and the agent list re-read. */
export async function afterUpdateAgent(
  queryClient: QueryClient,
  organizationId: string,
  updated: ManagedAgent
): Promise<void> {
  queryClient.setQueryData(agentKeys.agent(organizationId, updated.id), updated)
  await queryClient.invalidateQueries({
    queryKey: agentKeys.agentList(organizationId),
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

/** After `updateOrganizationAgent`: the organization agent is cached at once. */
export async function afterUpdateOrganizationAgent(
  queryClient: QueryClient,
  organizationId: string,
  updated: OrganizationAgent
): Promise<void> {
  queryClient.setQueryData(agentKeys.organizationAgent(organizationId), updated)
}
