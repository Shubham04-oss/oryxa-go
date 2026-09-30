// Query keys and query options for everything in lib/agents. Keys start
// ["agents", organizationId], so one organization's cache never answers for
// another's. A collection's key (`agentKeys.requests`) is the prefix of every
// key beneath it: invalidating it refreshes the list, each detail and what
// hangs off them.
import { queryOptions } from "@tanstack/react-query"
import { getAgentRequest, listAgentRequests } from "./agent-requests"
import type { AgentRequestBox } from "./agent-requests"
import { getAgent, listAgents } from "./managed-agents"
import {
  getOrganizationAgent,
  getOrganizationContext,
  listOrganizationFollowUps,
} from "./organization-agent"

/** Request boxes re-read this often: other members' agents write to them. */
export const AGENT_REQUESTS_POLL_MS = 10_000

/** Page sizes the old app used; the server's own defaults differ. */
export const AGENT_REQUESTS_LIMIT = 50
export const ORGANIZATION_FOLLOW_UPS_LIMIT = 20

function all(organizationId: string) {
  return ["agents", organizationId] as const
}

function agents(organizationId: string) {
  return [...all(organizationId), "agents"] as const
}

function requests(organizationId: string) {
  return [...all(organizationId), "requests"] as const
}

function organizationAgent(organizationId: string) {
  return [...all(organizationId), "organization-agent"] as const
}

export const agentKeys = {
  all,

  /** Every managed-agent query: the list and each agent. */
  agents,
  agentList: (organizationId: string) =>
    [...agents(organizationId), "list"] as const,
  agent: (organizationId: string, agentId: string) =>
    [...agents(organizationId), "detail", agentId] as const,

  /** Every agent-request query: both boxes and each request. */
  requests,
  /** Without `limit`, the prefix of every page size. */
  requestBox: (
    organizationId: string,
    box: AgentRequestBox,
    limit?: number
  ) => {
    const prefix = [...requests(organizationId), box] as const
    return limit === undefined ? prefix : ([...prefix, limit] as const)
  },
  request: (organizationId: string, requestId: string) =>
    [...requests(organizationId), "detail", requestId] as const,

  /** The organization agent; the prefix of its context and follow-ups. */
  organizationAgent,
  organizationContext: (organizationId: string) =>
    [...organizationAgent(organizationId), "context"] as const,
  /** Without `limit`, the prefix of every page size. */
  organizationFollowUps: (organizationId: string, limit?: number) => {
    const prefix = [...organizationAgent(organizationId), "follow-ups"] as const
    return limit === undefined ? prefix : ([...prefix, limit] as const)
  },
}

/** `listAgents`: the primary agent and every specialist. */
export function agentsQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: agentKeys.agentList(organizationId),
    queryFn: ({ signal }) => listAgents(organizationId, { signal }),
  })
}

/** `getAgent`. */
export function agentQueryOptions(organizationId: string, agentId: string) {
  return queryOptions({
    queryKey: agentKeys.agent(organizationId, agentId),
    queryFn: ({ signal }) => getAgent(organizationId, agentId, { signal }),
  })
}

/** `listAgentRequests` for one box, re-read every 10 s. */
export function agentRequestsQueryOptions(
  organizationId: string,
  box: AgentRequestBox,
  limit = AGENT_REQUESTS_LIMIT
) {
  return queryOptions({
    queryKey: agentKeys.requestBox(organizationId, box, limit),
    queryFn: ({ signal }) =>
      listAgentRequests(organizationId, box, { limit, signal }),
    refetchInterval: AGENT_REQUESTS_POLL_MS,
  })
}

/** `getAgentRequest`. */
export function agentRequestQueryOptions(
  organizationId: string,
  requestId: string
) {
  return queryOptions({
    queryKey: agentKeys.request(organizationId, requestId),
    queryFn: ({ signal }) =>
      getAgentRequest(organizationId, requestId, { signal }),
  })
}

/** `getOrganizationAgent`. */
export function organizationAgentQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: agentKeys.organizationAgent(organizationId),
    queryFn: ({ signal }) => getOrganizationAgent(organizationId, { signal }),
  })
}

/** `getOrganizationContext`: the organization-wide synthesis. */
export function organizationContextQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: agentKeys.organizationContext(organizationId),
    queryFn: ({ signal }) => getOrganizationContext(organizationId, { signal }),
  })
}

/** `listOrganizationFollowUps`, newest first. */
export function organizationFollowUpsQueryOptions(
  organizationId: string,
  limit = ORGANIZATION_FOLLOW_UPS_LIMIT
) {
  return queryOptions({
    queryKey: agentKeys.organizationFollowUps(organizationId, limit),
    queryFn: ({ signal }) =>
      listOrganizationFollowUps(organizationId, { limit, signal }),
  })
}
