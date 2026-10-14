import { QueryClient } from "@tanstack/react-query"
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  AGENT_REQUESTS_POLL_MS,
  AGENT_TEAM_MESSAGES_POLL_MS,
  WORKFLOW_EXECUTIONS_POLL_MS,
  agentKeys,
  agentQueryOptions,
  agentRequestQueryOptions,
  agentRequestsQueryOptions,
  agentTeamMessagesQueryOptions,
  agentTeamQueryOptions,
  agentTeamThreadsQueryOptions,
  agentTeamsQueryOptions,
  agentWorkflowQueryOptions,
  agentWorkflowsQueryOptions,
  agentsQueryOptions,
  organizationAgentQueryOptions,
  organizationContextQueryOptions,
  organizationFollowUpsQueryOptions,
  workflowExecutionQueryOptions,
  workflowExecutionsQueryOptions,
} from "./queries"
import type { WorkflowExecution } from "./agent-workflows"

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
  })
}

function execution(status: WorkflowExecution["status"]): WorkflowExecution {
  return {
    id: `awx_${status}`,
    workflow_id: "awf_1",
    organization_id: "org-1",
    owner_user_id: "user-1",
    started_by: "api",
    status,
    created_at: "2026-09-23T00:00:00Z",
  }
}

afterEach(() => vi.unstubAllGlobals())

describe("agentKeys", () => {
  it("scopes every key to its organisation, with collections as prefixes", () => {
    expect(agentKeys.all("org-1")).toEqual(["agents", "org-1"])
    expect(agentKeys.teams("org-1")).toEqual(["agents", "org-1", "teams"])
    expect(agentKeys.teamList("org-1")).toEqual([
      "agents",
      "org-1",
      "teams",
      "list",
    ])
    expect(agentKeys.teamMessages("org-1", "atm_1")).toEqual([
      "agents",
      "org-1",
      "teams",
      "detail",
      "atm_1",
      "messages",
    ])
    expect(agentKeys.workflowExecutions("org-1", "awf_1")).toEqual([
      "agents",
      "org-1",
      "workflows",
      "detail",
      "awf_1",
      "executions",
    ])
    expect(agentKeys.workflowExecutions("org-1", "awf_1", 20)).toEqual([
      ...agentKeys.workflowExecutions("org-1", "awf_1"),
      20,
    ])
    expect(agentKeys.execution("org-1", "awx_1")).toEqual([
      "agents",
      "org-1",
      "workflows",
      "execution",
      "awx_1",
    ])
    expect(agentKeys.requestBox("org-1", "inbox", 50)).toEqual([
      "agents",
      "org-1",
      "requests",
      "inbox",
      50,
    ])
    expect(agentKeys.organizationFollowUps("org-1", 20)).toEqual([
      "agents",
      "org-1",
      "organization-agent",
      "follow-ups",
      20,
    ])
    expect(agentKeys.agentList("org-2")).not.toEqual(
      agentKeys.agentList("org-1")
    )
  })
})

describe("agent query options", () => {
  it("read the endpoint their key names", async () => {
    // Every list envelope at once, so each queryFn finds its field.
    const fetchMock = vi.fn(async (_url: string, _init: RequestInit) =>
      json({
        agents: [],
        teams: [],
        messages: [],
        threads: {},
        workflows: [],
        executions: [],
        requests: [],
        follow_ups: [],
      })
    )
    vi.stubGlobal("fetch", fetchMock)
    const queryClient = new QueryClient()
    const cases = [
      [agentsQueryOptions("org-1"), "/agents"],
      [agentQueryOptions("org-1", "agt_1"), "/agents/agt_1"],
      [agentTeamsQueryOptions("org-1"), "/agent-teams"],
      [agentTeamQueryOptions("org-1", "atm_1"), "/agent-teams/atm_1"],
      [
        agentTeamMessagesQueryOptions("org-1", "atm_1"),
        "/agent-teams/atm_1/messages",
      ],
      [
        agentTeamThreadsQueryOptions("org-1", "atm_1"),
        "/agent-teams/atm_1/agent-threads",
      ],
      [agentWorkflowsQueryOptions("org-1"), "/agent-workflows"],
      [agentWorkflowQueryOptions("org-1", "awf_1"), "/agent-workflows/awf_1"],
      [
        workflowExecutionsQueryOptions("org-1", "awf_1"),
        "/agent-workflows/awf_1/executions?limit=20",
      ],
      [
        workflowExecutionQueryOptions("org-1", "awx_1"),
        "/agent-workflow-executions/awx_1",
      ],
      [
        agentRequestsQueryOptions("org-1", "sent"),
        "/agent-requests/sent?limit=50",
      ],
      [agentRequestQueryOptions("org-1", "irq_1"), "/agent-requests/irq_1"],
      [organizationAgentQueryOptions("org-1"), "/organization-agent"],
      [organizationContextQueryOptions("org-1"), "/organization-agent/context"],
      [
        organizationFollowUpsQueryOptions("org-1"),
        "/organization-agent/follow-ups?limit=20",
      ],
    ] as const

    for (const [options] of cases) {
      // Each queryFn returns a different type; fetchQuery only needs one.
      await queryClient.fetchQuery(
        options as unknown as ReturnType<typeof agentsQueryOptions>
      )
    }

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(
      cases.map(([, path]) => `/v1/organizations/org-1${path}`)
    )
  })

  it("carries a custom page size into both the key and the request", async () => {
    const fetchMock = vi.fn(async (_url: string, _init: RequestInit) =>
      json({ requests: [] })
    )
    vi.stubGlobal("fetch", fetchMock)
    const options = agentRequestsQueryOptions("org-1", "inbox", 100)

    expect(options.queryKey).toEqual(
      agentKeys.requestBox("org-1", "inbox", 100)
    )
    await new QueryClient().fetchQuery(options)
    expect(fetchMock.mock.calls[0][0]).toBe(
      "/v1/organizations/org-1/agent-requests/inbox?limit=100"
    )
  })

  it("polls a team conversation every 5 s and request boxes every 10 s", () => {
    const messages = agentTeamMessagesQueryOptions("org-1", "atm_1")
    expect(messages.refetchInterval).toBe(AGENT_TEAM_MESSAGES_POLL_MS)
    expect(AGENT_TEAM_MESSAGES_POLL_MS).toBe(5_000)
    expect(messages.refetchIntervalInBackground).toBe(false)

    const inbox = agentRequestsQueryOptions("org-1", "inbox")
    expect(inbox.refetchInterval).toBe(AGENT_REQUESTS_POLL_MS)
    expect(AGENT_REQUESTS_POLL_MS).toBe(10_000)

    expect(
      agentTeamThreadsQueryOptions("org-1", "atm_1").refetchInterval
    ).toBeUndefined()
    expect(agentsQueryOptions("org-1").refetchInterval).toBeUndefined()
  })

  it("polls executions every 2 s only while one is queued or running", () => {
    const queryClient = new QueryClient()
    const list = workflowExecutionsQueryOptions("org-1", "awf_1")
    const one = workflowExecutionQueryOptions("org-1", "awx_1")
    const listInterval = list.refetchInterval
    const oneInterval = one.refetchInterval
    if (typeof listInterval !== "function" || typeof oneInterval !== "function")
      throw new Error("execution polling must adapt to the data")

    const intervalFor = (data: WorkflowExecution[]) => {
      queryClient.setQueryData(list.queryKey, data)
      const query = queryClient
        .getQueryCache()
        .find({ queryKey: list.queryKey })
      if (!query) throw new Error("query missing")
      return listInterval(
        query as unknown as Parameters<typeof listInterval>[0]
      )
    }
    expect(intervalFor([execution("succeeded"), execution("running")])).toBe(
      WORKFLOW_EXECUTIONS_POLL_MS
    )
    expect(intervalFor([execution("queued")])).toBe(2_000)
    expect(
      intervalFor([execution("succeeded"), execution("needs_confirmation")])
    ).toBe(false)
    expect(intervalFor([])).toBe(false)

    const singleIntervalFor = (data: WorkflowExecution) => {
      queryClient.setQueryData(one.queryKey, data)
      const query = queryClient.getQueryCache().find({ queryKey: one.queryKey })
      if (!query) throw new Error("query missing")
      return oneInterval(query as unknown as Parameters<typeof oneInterval>[0])
    }
    expect(singleIntervalFor(execution("running"))).toBe(2_000)
    expect(singleIntervalFor(execution("failed"))).toBe(false)
  })
})
