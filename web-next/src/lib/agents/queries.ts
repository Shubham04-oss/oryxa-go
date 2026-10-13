// Query keys and query options for everything in lib/agents. Keys start
// ["agents", organizationId], so one organization's cache never answers for
// another's. A collection's key (`agentKeys.teams`) is the prefix of every
// key beneath it: invalidating it refreshes the list, each detail and what
// hangs off them.
import { queryOptions } from "@tanstack/react-query"
import { getAgentRequest, listAgentRequests } from "./agent-requests"
import type { AgentRequestBox } from "./agent-requests"
import {
  getAgentTeam,
  getAgentTeamThreads,
  listAgentTeamMessages,
  listAgentTeams,
} from "./agent-teams"
import {
  getAgentWorkflow,
  getWorkflowExecution,
  listAgentWorkflows,
  listWorkflowExecutions,
} from "./agent-workflows"
import { getAgent, listAgents } from "./managed-agents"
import {
  getOrganizationAgent,
  getOrganizationContext,
  listOrganizationFollowUps,
} from "./organization-agent"
import { hasExecutionInFlight, isExecutionInFlight } from "./policy"

/** An open team conversation re-reads this often. */
export const AGENT_TEAM_MESSAGES_POLL_MS = 5_000
/** Request boxes re-read this often: other members' agents write to them. */
export const AGENT_REQUESTS_POLL_MS = 10_000
/** Executions re-read this often while one is queued or running, else never. */
export const WORKFLOW_EXECUTIONS_POLL_MS = 2_000

/** Page sizes the old app used; the server's own defaults differ. */
export const AGENT_REQUESTS_LIMIT = 50
export const WORKFLOW_EXECUTIONS_LIMIT = 20
export const ORGANIZATION_FOLLOW_UPS_LIMIT = 20

function all(organizationId: string) {
  return ["agents", organizationId] as const
}

function agents(organizationId: string) {
  return [...all(organizationId), "agents"] as const
}

function teams(organizationId: string) {
  return [...all(organizationId), "teams"] as const
}

function team(organizationId: string, teamId: string) {
  return [...teams(organizationId), "detail", teamId] as const
}

function workflows(organizationId: string) {
  return [...all(organizationId), "workflows"] as const
}

function workflow(organizationId: string, workflowId: string) {
  return [...workflows(organizationId), "detail", workflowId] as const
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

  /** Every agent-team query: list, details, conversations, thread pointers. */
  teams,
  teamList: (organizationId: string) =>
    [...teams(organizationId), "list"] as const,
  /** A team's detail; the prefix of its conversation and thread pointers. */
  team,
  teamMessages: (organizationId: string, teamId: string) =>
    [...team(organizationId, teamId), "messages"] as const,
  teamThreads: (organizationId: string, teamId: string) =>
    [...team(organizationId, teamId), "agent-threads"] as const,

  /** Every workflow query: list, details, executions. */
  workflows,
  workflowList: (organizationId: string) =>
    [...workflows(organizationId), "list"] as const,
  /** A workflow's detail; the prefix of its execution lists. */
  workflow,
  /** Without `limit`, the prefix of every page size. */
  workflowExecutions: (
    organizationId: string,
    workflowId: string,
    limit?: number
  ) => {
    const prefix = [
      ...workflow(organizationId, workflowId),
      "executions",
    ] as const
    return limit === undefined ? prefix : ([...prefix, limit] as const)
  },
  /** One execution, addressed without its workflow as the API does. */
  execution: (organizationId: string, executionId: string) =>
    [...workflows(organizationId), "execution", executionId] as const,

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

/** `listAgentTeams`. */
export function agentTeamsQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: agentKeys.teamList(organizationId),
    queryFn: ({ signal }) => listAgentTeams(organizationId, { signal }),
  })
}

/** `getAgentTeam`. */
export function agentTeamQueryOptions(organizationId: string, teamId: string) {
  return queryOptions({
    queryKey: agentKeys.team(organizationId, teamId),
    queryFn: ({ signal }) => getAgentTeam(organizationId, teamId, { signal }),
  })
}

/**
 * `listAgentTeamMessages`: the latest 200 messages, re-read every 5 s while
 * the conversation is open (and never in a background tab).
 */
export function agentTeamMessagesQueryOptions(
  organizationId: string,
  teamId: string
) {
  return queryOptions({
    queryKey: agentKeys.teamMessages(organizationId, teamId),
    queryFn: ({ signal }) =>
      listAgentTeamMessages(organizationId, teamId, { signal }),
    refetchInterval: AGENT_TEAM_MESSAGES_POLL_MS,
    refetchIntervalInBackground: false,
  })
}

/**
 * `getAgentTeamThreads`. Not polled: only posting to the team changes the
 * pointers, and `afterPostAgentTeamMessage` refreshes them.
 */
export function agentTeamThreadsQueryOptions(
  organizationId: string,
  teamId: string
) {
  return queryOptions({
    queryKey: agentKeys.teamThreads(organizationId, teamId),
    queryFn: ({ signal }) =>
      getAgentTeamThreads(organizationId, teamId, { signal }),
  })
}

/** `listAgentWorkflows`. */
export function agentWorkflowsQueryOptions(organizationId: string) {
  return queryOptions({
    queryKey: agentKeys.workflowList(organizationId),
    queryFn: ({ signal }) => listAgentWorkflows(organizationId, { signal }),
  })
}

/** `getAgentWorkflow`. */
export function agentWorkflowQueryOptions(
  organizationId: string,
  workflowId: string
) {
  return queryOptions({
    queryKey: agentKeys.workflow(organizationId, workflowId),
    queryFn: ({ signal }) =>
      getAgentWorkflow(organizationId, workflowId, { signal }),
  })
}

/**
 * `listWorkflowExecutions`, newest first: re-read every 2 s while any is
 * queued or running, and not at all once every one has settled.
 */
export function workflowExecutionsQueryOptions(
  organizationId: string,
  workflowId: string,
  limit = WORKFLOW_EXECUTIONS_LIMIT
) {
  return queryOptions({
    queryKey: agentKeys.workflowExecutions(organizationId, workflowId, limit),
    queryFn: ({ signal }) =>
      listWorkflowExecutions(organizationId, workflowId, { limit, signal }),
    refetchInterval: (query) =>
      hasExecutionInFlight(query.state.data)
        ? WORKFLOW_EXECUTIONS_POLL_MS
        : false,
  })
}

/** `getWorkflowExecution`: re-read every 2 s until it settles. */
export function workflowExecutionQueryOptions(
  organizationId: string,
  executionId: string
) {
  return queryOptions({
    queryKey: agentKeys.execution(organizationId, executionId),
    queryFn: ({ signal }) =>
      getWorkflowExecution(organizationId, executionId, { signal }),
    refetchInterval: (query) =>
      isExecutionInFlight(query.state.data)
        ? WORKFLOW_EXECUTIONS_POLL_MS
        : false,
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
