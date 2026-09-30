import { QueryClient } from "@tanstack/react-query"
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  AGENT_REQUESTS_POLL_MS,
  agentKeys,
  agentQueryOptions,
  agentRequestQueryOptions,
  agentRequestsQueryOptions,
  agentsQueryOptions,
  organizationAgentQueryOptions,
  organizationContextQueryOptions,
  organizationFollowUpsQueryOptions,
} from "./queries"

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
  })
}

afterEach(() => vi.unstubAllGlobals())

describe("agentKeys", () => {
  it("scopes every key to its organisation, with collections as prefixes", () => {
    expect(agentKeys.all("org-1")).toEqual(["agents", "org-1"])
    expect(agentKeys.agents("org-1")).toEqual(["agents", "org-1", "agents"])
    expect(agentKeys.agent("org-1", "agt_1")).toEqual([
      "agents",
      "org-1",
      "agents",
      "detail",
      "agt_1",
    ])
    expect(agentKeys.requestBox("org-1", "inbox", 50)).toEqual([
      "agents",
      "org-1",
      "requests",
      "inbox",
      50,
    ])
    expect(agentKeys.request("org-1", "irq_1")).toEqual([
      "agents",
      "org-1",
      "requests",
      "detail",
      "irq_1",
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
        requests: [],
        follow_ups: [],
      })
    )
    vi.stubGlobal("fetch", fetchMock)
    const queryClient = new QueryClient()
    const cases = [
      [agentsQueryOptions("org-1"), "/agents"],
      [agentQueryOptions("org-1", "agt_1"), "/agents/agt_1"],
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

  it("polls request boxes every 10 s, and the agent list never", () => {
    const inbox = agentRequestsQueryOptions("org-1", "inbox")
    expect(inbox.refetchInterval).toBe(AGENT_REQUESTS_POLL_MS)
    expect(AGENT_REQUESTS_POLL_MS).toBe(10_000)

    expect(agentsQueryOptions("org-1").refetchInterval).toBeUndefined()
  })
})
