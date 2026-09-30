import { QueryClient } from "@tanstack/react-query"
import type { QueryKey } from "@tanstack/react-query"
import { describe, expect, it } from "vitest"
import {
  afterDecideAgentRequest,
  afterRespondToAgentRequest,
  afterUpdateAgent,
  afterUpdateOrganizationAgent,
} from "./invalidation"
import { agentKeys } from "./queries"
import type { AgentRequest } from "./agent-requests"
import type { ManagedAgent } from "./managed-agents"
import type { OrganizationAgent } from "./organization-agent"

const org = "org-1"

// Every key a page might hold, each seeded with placeholder data.
const keys = {
  agentList: agentKeys.agentList(org),
  agent: agentKeys.agent(org, "agt_1"),
  inbox: agentKeys.requestBox(org, "inbox", 50),
  sent: agentKeys.requestBox(org, "sent", 50),
  request: agentKeys.request(org, "irq_1"),
  organizationAgent: agentKeys.organizationAgent(org),
  organizationContext: agentKeys.organizationContext(org),
  followUps: agentKeys.organizationFollowUps(org, 20),
  otherOrganizationAgents: agentKeys.agentList("org-2"),
} satisfies Record<string, QueryKey>

type Name = keyof typeof keys

function seeded() {
  const queryClient = new QueryClient()
  for (const key of Object.values(keys)) {
    queryClient.setQueryData(key, "held")
  }
  return queryClient
}

function invalidated(queryClient: QueryClient): Name[] {
  return (Object.keys(keys) as Name[]).filter(
    (name) => queryClient.getQueryState(keys[name])?.isInvalidated
  )
}

describe("agent mutation helpers", () => {
  it("cache an agent that changed and refresh the agent list", async () => {
    const queryClient = seeded()
    const updated = { id: "agt_1", name: "Renamed" } as ManagedAgent

    await afterUpdateAgent(queryClient, org, updated)

    expect(queryClient.getQueryData(keys.agent)).toBe(updated)
    expect(invalidated(queryClient)).toEqual(["agentList"])
  })

  it("cache a decided or answered request and refresh both boxes", async () => {
    const request = { id: "irq_1", status: "approved" } as AgentRequest
    for (const after of [afterDecideAgentRequest, afterRespondToAgentRequest]) {
      const queryClient = seeded()
      await after(queryClient, org, request)
      expect(queryClient.getQueryData(keys.request)).toBe(request)
      expect(invalidated(queryClient)).toEqual(["inbox", "sent"])
    }
  })

  it("cache the organization agent after it changes, and refresh nothing else", async () => {
    const queryClient = seeded()
    const updated = { id: "oag_1", status: "disabled" } as OrganizationAgent

    await afterUpdateOrganizationAgent(queryClient, org, updated)

    expect(queryClient.getQueryData(keys.organizationAgent)).toBe(updated)
    expect(invalidated(queryClient)).toEqual([])
  })
})
