import { afterEach, describe, expect, it, vi } from "vitest"
import {
  createAgentWorkflow,
  deleteAgentWorkflow,
  getAgentWorkflow,
  getWorkflowExecution,
  listAgentWorkflows,
  listWorkflowExecutions,
  startAgentWorkflow,
  updateAgentWorkflow,
} from "./agent-workflows"
import type { AgentWorkflow, WorkflowExecution } from "./agent-workflows"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function stubFetch(respond: (url: string, init: RequestInit) => Response) {
  const fetchMock = vi.fn(async (url: string, init: RequestInit) =>
    respond(url, init)
  )
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

function sent(fetchMock: ReturnType<typeof stubFetch>) {
  return fetchMock.mock.calls.map(([url, init]) => [
    init.method,
    url,
    init.body,
  ])
}

const workflow: AgentWorkflow = {
  id: "awf_pipeline",
  organization_id: "org-1",
  owner_user_id: "user-1",
  name: "Weekly pipeline",
  team_id: "atm_revenue",
  status: "active",
  assignments: [{ agent_id: "agt_sales", task: "Review pipeline" }],
  schedule: {
    expression: "cron(0 9 ? * MON *)",
    timezone: "Asia/Kolkata",
    state: "enabled",
  },
  revision: 1,
  created_at: "2026-09-23T00:00:00Z",
  updated_at: "2026-09-23T00:00:00Z",
}

const queued: WorkflowExecution = {
  id: "awx_1",
  workflow_id: "awf_pipeline",
  organization_id: "org-1",
  owner_user_id: "user-1",
  started_by: "api",
  status: "queued",
  created_at: "2026-09-23T00:00:00Z",
}

afterEach(() => vi.unstubAllGlobals())

describe("agent workflows API", () => {
  it("lists, reads and deletes the caller's workflows", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init.method === "DELETE") return new Response(null, { status: 204 })
      return url.endsWith("/agent-workflows")
        ? json({ workflows: [workflow] })
        : json(workflow)
    })

    expect(await listAgentWorkflows("org-1")).toEqual([workflow])
    expect(await getAgentWorkflow("org-1", "awf_pipeline")).toEqual(workflow)
    await expect(
      deleteAgentWorkflow("org-1", "awf_pipeline")
    ).resolves.toBeUndefined()

    expect(sent(fetchMock)).toEqual([
      ["GET", "/v1/organizations/org-1/agent-workflows", undefined],
      [
        "GET",
        "/v1/organizations/org-1/agent-workflows/awf_pipeline",
        undefined,
      ],
      [
        "DELETE",
        "/v1/organizations/org-1/agent-workflows/awf_pipeline",
        undefined,
      ],
    ])
  })

  it("creates a workflow with only the fields the handler reads", async () => {
    const fetchMock = stubFetch(() => json(workflow, 201))

    await createAgentWorkflow("org-1", {
      name: "Weekly pipeline",
      team_id: "atm_revenue",
      assignments: [
        { agent_id: "agt_sales", task: "Review pipeline", context: undefined },
      ],
      // The server fills in the timezone ("UTC") and state ("enabled").
      schedule: { expression: "rate(7 days)" },
    })
    // Handed a stored workflow, extra fields stay behind.
    await createAgentWorkflow("org-1", workflow)

    expect(sent(fetchMock)).toEqual([
      [
        "POST",
        "/v1/organizations/org-1/agent-workflows",
        '{"name":"Weekly pipeline","team_id":"atm_revenue","assignments":[{"agent_id":"agt_sales","task":"Review pipeline"}],"schedule":{"expression":"rate(7 days)"}}',
      ],
      [
        "POST",
        "/v1/organizations/org-1/agent-workflows",
        '{"name":"Weekly pipeline","team_id":"atm_revenue","assignments":[{"agent_id":"agt_sales","task":"Review pipeline"}],"schedule":{"expression":"cron(0 9 ? * MON *)","timezone":"Asia/Kolkata","state":"enabled"}}',
      ],
    ])
  })

  it("updates fields, replaces a schedule, and clears one only when asked", async () => {
    const fetchMock = stubFetch(() => json(workflow))

    await updateAgentWorkflow("org-1", "awf_pipeline", { status: "disabled" })
    await updateAgentWorkflow("org-1", "awf_pipeline", {
      schedule: {
        expression: "rate(1 day)",
        timezone: "UTC",
        state: "disabled",
      },
    })
    await updateAgentWorkflow("org-1", "awf_pipeline", { clear_schedule: true })
    await updateAgentWorkflow("org-1", "awf_pipeline", {
      assignments: [
        { agent_id: "agt_sales", task: "Review", context: "Q3 only" },
      ],
      clear_schedule: false,
    })

    const path = "/v1/organizations/org-1/agent-workflows/awf_pipeline"
    expect(sent(fetchMock)).toEqual([
      ["PATCH", path, '{"status":"disabled"}'],
      [
        "PATCH",
        path,
        '{"schedule":{"expression":"rate(1 day)","timezone":"UTC","state":"disabled"}}',
      ],
      ["PATCH", path, '{"clear_schedule":true}'],
      [
        "PATCH",
        path,
        '{"assignments":[{"agent_id":"agt_sales","task":"Review","context":"Q3 only"}]}',
      ],
    ])
  })

  it("starts a run with an empty POST and returns the queued execution", async () => {
    const fetchMock = stubFetch(() => json(queued, 202))

    expect(await startAgentWorkflow("org-1", "awf_pipeline")).toEqual(queued)
    const [[url, init]] = fetchMock.mock.calls
    expect(url).toBe(
      "/v1/organizations/org-1/agent-workflows/awf_pipeline/executions"
    )
    expect(init).toMatchObject({ method: "POST", credentials: "include" })
    expect(init.body).toBeUndefined()
    expect(init.headers).toBeUndefined()
  })

  it("lists executions with an optional limit and reads one by its own path", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("/executions")
        ? json({ executions: [queued] })
        : json(queued)
    )

    expect(await listWorkflowExecutions("org-1", "awf_pipeline")).toEqual([
      queued,
    ])
    await listWorkflowExecutions("org-1", "awf_pipeline", { limit: 20 })
    expect(await getWorkflowExecution("org-1", "awx_1")).toEqual(queued)

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/v1/organizations/org-1/agent-workflows/awf_pipeline/executions",
      "/v1/organizations/org-1/agent-workflows/awf_pipeline/executions?limit=20",
      "/v1/organizations/org-1/agent-workflow-executions/awx_1",
    ])
  })

  it("keeps the service's codes for a paused workflow and a missing orchestrator", async () => {
    const answers = [
      json(
        {
          error: {
            code: "workflow_disabled",
            message: "agent workflow is disabled",
          },
        },
        409
      ),
      json(
        {
          error: {
            code: "agent_workflows_unavailable",
            message: "agent workflow orchestration is unavailable",
          },
        },
        503
      ),
    ]
    stubFetch(() => answers.shift() ?? json({}))

    await expect(
      startAgentWorkflow("org-1", "awf_pipeline")
    ).rejects.toMatchObject({ status: 409, code: "workflow_disabled" })
    await expect(
      startAgentWorkflow("org-1", "awf_pipeline")
    ).rejects.toMatchObject({
      status: 503,
      code: "agent_workflows_unavailable",
    })
  })
})
