import { QueryClient } from "@tanstack/react-query"
import type { QueryKey } from "@tanstack/react-query"
import { describe, expect, it } from "vitest"
import { teamspaceKeys } from "@/lib/teamspaces/queries"
import {
  afterCreateAgent,
  afterCreateAgentTeam,
  afterCreateAgentWorkflow,
  afterDecideAgentRequest,
  afterDeleteAgent,
  afterDeleteAgentTeam,
  afterDeleteAgentWorkflow,
  afterPostAgentTeamMessage,
  afterRespondToAgentRequest,
  afterStartAgentWorkflow,
  afterUpdateAgent,
  afterUpdateAgentTeam,
  afterUpdateAgentWorkflow,
  afterUpdateOrganizationAgent,
} from "./invalidation"
import { agentKeys } from "./queries"
import type { AgentRequest } from "./agent-requests"
import type { AgentTeam, AgentTeamMessage } from "./agent-teams"
import type { AgentWorkflow, WorkflowExecution } from "./agent-workflows"
import type { ManagedAgent } from "./managed-agents"
import type { OrganizationAgent } from "./organization-agent"

const org = "org-1"

// Every key a page might hold, each seeded with placeholder data.
const keys = {
  agentList: agentKeys.agentList(org),
  agent: agentKeys.agent(org, "agt_1"),
  teamList: agentKeys.teamList(org),
  team: agentKeys.team(org, "atm_1"),
  teamMessages: agentKeys.teamMessages(org, "atm_1"),
  teamThreads: agentKeys.teamThreads(org, "atm_1"),
  otherTeamMessages: agentKeys.teamMessages(org, "atm_2"),
  workflowList: agentKeys.workflowList(org),
  workflow: agentKeys.workflow(org, "awf_1"),
  executions: agentKeys.workflowExecutions(org, "awf_1", 20),
  moreExecutions: agentKeys.workflowExecutions(org, "awf_1", 100),
  otherExecutions: agentKeys.workflowExecutions(org, "awf_2", 20),
  inbox: agentKeys.requestBox(org, "inbox", 50),
  sent: agentKeys.requestBox(org, "sent", 50),
  request: agentKeys.request(org, "irq_1"),
  organizationAgent: agentKeys.organizationAgent(org),
  organizationContext: agentKeys.organizationContext(org),
  followUps: agentKeys.organizationFollowUps(org, 20),
  teamspaceList: teamspaceKeys.list(org),
  teamspace: teamspaceKeys.teamspace(org, "tsp_1"),
  room: teamspaceKeys.room(org, "tsp_1"),
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

function removed(queryClient: QueryClient): Name[] {
  return (Object.keys(keys) as Name[]).filter(
    (name) => queryClient.getQueryState(keys[name]) === undefined
  )
}

describe("agent mutation helpers", () => {
  it("refresh the agent list after a specialist is created", async () => {
    const queryClient = seeded()
    await afterCreateAgent(queryClient, org)
    expect(invalidated(queryClient)).toEqual(["agentList"])
  })

  it("refresh everything that shows an agent after it changes", async () => {
    const queryClient = seeded()
    const updated = { id: "agt_1", name: "Renamed" } as ManagedAgent

    await afterUpdateAgent(queryClient, org, updated)

    expect(queryClient.getQueryData(keys.agent)).toBe(updated)
    expect(invalidated(queryClient)).toEqual([
      "agentList",
      "teamList",
      "team",
      "teamMessages",
      "teamThreads",
      "otherTeamMessages",
      "teamspaceList",
      "teamspace",
      "room",
    ])
  })

  it("drop a deleted agent and refresh the list", async () => {
    const queryClient = seeded()
    await afterDeleteAgent(queryClient, org, "agt_1")
    expect(removed(queryClient)).toEqual(["agent"])
    expect(invalidated(queryClient)).toEqual(["agentList"])
  })

  it("cache a created or changed team and refresh the team list only", async () => {
    const created = seeded()
    const team = { id: "atm_1", name: "Launch" } as AgentTeam
    await afterCreateAgentTeam(created, org, team)
    expect(created.getQueryData(keys.team)).toBe(team)
    expect(invalidated(created)).toEqual(["teamList"])

    const updated = seeded()
    await afterUpdateAgentTeam(updated, org, team)
    expect(updated.getQueryData(keys.team)).toBe(team)
    expect(invalidated(updated)).toEqual(["teamList"])
  })

  it("drop a deleted team with its conversation and thread pointers", async () => {
    const queryClient = seeded()
    await afterDeleteAgentTeam(queryClient, org, "atm_1")
    expect(removed(queryClient)).toEqual([
      "team",
      "teamMessages",
      "teamThreads",
    ])
    expect(invalidated(queryClient)).toEqual(["teamList"])
  })

  it("show a post and its replies at once, then re-read the conversation", async () => {
    const queryClient = seeded()
    const line = (id: string, seq: number) =>
      ({ id, seq, body: id }) as AgentTeamMessage
    queryClient.setQueryData(keys.teamMessages, [line("m1", 1)])

    await afterPostAgentTeamMessage(queryClient, org, "atm_1", [
      line("m3", 3),
      line("m2", 2),
    ])

    expect(
      queryClient
        .getQueryData<AgentTeamMessage[]>(keys.teamMessages)
        ?.map((message) => message.id)
    ).toEqual(["m1", "m2", "m3"])
    expect(invalidated(queryClient)).toEqual(["teamMessages", "teamThreads"])
  })

  it("never seed a conversation nobody has read with just the posted lines", async () => {
    const queryClient = new QueryClient()
    await afterPostAgentTeamMessage(queryClient, org, "atm_1", [
      { id: "m1", seq: 1 } as AgentTeamMessage,
    ])
    expect(queryClient.getQueryData(keys.teamMessages)).toBeUndefined()
  })

  it("cache a created or changed workflow and refresh the workflow list", async () => {
    const workflow = { id: "awf_1", name: "Weekly" } as AgentWorkflow
    for (const after of [afterCreateAgentWorkflow, afterUpdateAgentWorkflow]) {
      const queryClient = seeded()
      await after(queryClient, org, workflow)
      expect(queryClient.getQueryData(keys.workflow)).toBe(workflow)
      expect(invalidated(queryClient)).toEqual(["workflowList"])
    }
  })

  it("drop a deleted workflow with its executions", async () => {
    const queryClient = seeded()
    await afterDeleteAgentWorkflow(queryClient, org, "awf_1")
    expect(removed(queryClient)).toEqual([
      "workflow",
      "executions",
      "moreExecutions",
    ])
    expect(invalidated(queryClient)).toEqual(["workflowList"])
  })

  it("cache a started execution and re-read that workflow's executions", async () => {
    const queryClient = seeded()
    const execution = {
      id: "awx_9",
      workflow_id: "awf_1",
      status: "queued",
    } as WorkflowExecution

    await afterStartAgentWorkflow(queryClient, org, execution)

    expect(queryClient.getQueryData(agentKeys.execution(org, "awx_9"))).toBe(
      execution
    )
    expect(invalidated(queryClient)).toEqual(["executions", "moreExecutions"])
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

  it("refresh teamspaces, which embed the organization agent, after it changes", async () => {
    const queryClient = seeded()
    const updated = { id: "oag_1", status: "disabled" } as OrganizationAgent

    await afterUpdateOrganizationAgent(queryClient, org, updated)

    expect(queryClient.getQueryData(keys.organizationAgent)).toBe(updated)
    expect(invalidated(queryClient)).toEqual([
      "teamspaceList",
      "teamspace",
      "room",
    ])
  })
})
