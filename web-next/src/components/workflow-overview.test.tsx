// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { WorkflowOverview } from "./workflow-overview"
import { WorkflowForm } from "./workflow-form"
import { executionStatus } from "./workflow-execution"
import { agent, ORG, primary, team, VIEWER } from "./agents-fake-backend"
import type {
  AgentWorkflow,
  WorkflowExecution,
} from "@/lib/agents/agent-workflows"
import { agentKeys } from "@/lib/agents/queries"

const scout = agent({ id: "agt_scout", name: "Scout" })
const quill = agent({ id: "agt_quill", name: "Quill" })
const crew = team({ id: "atm_crew", name: "Pipeline crew" }, [
  { agent: scout, role: "Research" },
  { agent: quill, role: "Write" },
])
const weekly: AgentWorkflow = {
  id: "awf_weekly",
  organization_id: ORG,
  owner_user_id: VIEWER,
  name: "Weekly review",
  description: "Review this week's work",
  team_id: crew.id,
  status: "active",
  assignments: [
    {
      agent_id: scout.id,
      task: "Summarise the week",
      context: "Use the weekly notes",
    },
  ],
  schedule: {
    expression: "rate(7 days)",
    timezone: "Asia/Kolkata",
    state: "disabled",
  },
  revision: 1,
  created_at: "2026-09-20T10:00:00Z",
  updated_at: "2026-09-20T10:00:00Z",
}
const execution: WorkflowExecution = {
  id: "awx_1",
  workflow_id: weekly.id,
  organization_id: ORG,
  owner_user_id: VIEWER,
  started_by: "api",
  status: "queued",
  created_at: weekly.created_at,
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}
function refusal(status: number, code: string, message: string) {
  return json({ error: { code, message } }, status)
}
type Call = { method: string; path: string; body: unknown }
function backend({
  workflows = [weekly],
  executions = [] as WorkflowExecution[],
  teams = [crew],
  fail,
  gate,
}: {
  workflows?: AgentWorkflow[]
  executions?: WorkflowExecution[]
  teams?: (typeof crew)[]
  fail?: (method: string, path: string) => Response | undefined
  gate?: Promise<void>
} = {}) {
  let items = [...workflows]
  let runs = [...executions]
  const writes: Call[] = []
  const fetchMock = vi.fn(
    async (input: string | URL | Request, options?: RequestInit) => {
      const path = String(input).split("?")[0]
      const method = options?.method ?? "GET"
      const body = options?.body ? JSON.parse(String(options.body)) : undefined
      if (method !== "GET") writes.push({ method, path, body })
      const refused = fail?.(method, path)
      if (refused) return refused
      if (method !== "GET" && gate) await gate
      if (path.endsWith("/agent-teams")) return json({ teams })
      if (path.includes("/agent-workflow-executions/"))
        return json(
          runs.find((entry) => path.endsWith(entry.id)) ?? runs[0] ?? execution
        )
      if (path.endsWith("/executions")) {
        if (method === "POST") {
          runs = [execution, ...runs]
          return json(execution, 202)
        }
        return json({ executions: runs })
      }
      if (path.endsWith("/agent-workflows")) {
        if (method === "POST") {
          const created = {
            ...weekly,
            ...body,
            id: "awf_new",
            schedule: body.schedule,
          }
          items = [...items, created]
          return json(created, 201)
        }
        return json({ workflows: items })
      }
      const item = items.find((entry) => path.endsWith(entry.id))
      if (!item) return refusal(404, "not_found", "not found")
      if (method === "DELETE") {
        items = items.filter((entry) => entry.id !== item.id)
        return new Response(null, { status: 204 })
      }
      if (method === "PATCH") {
        const updated = {
          ...item,
          ...body,
          ...(body.clear_schedule ? { schedule: undefined } : {}),
        }
        items = items.map((entry) => (entry.id === item.id ? updated : entry))
        return json(updated)
      }
      return json(item)
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { writes, fetchMock }
}
function wrapper(children: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
  const rendered = render(
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  return { ...rendered, client }
}
function overview() {
  return wrapper(<WorkflowOverview organizationId={ORG} />)
}
async function open() {
  fireEvent.click(
    await screen.findByRole("button", { name: "Open Weekly review workflow" })
  )
  const run = await screen.findByRole("button", { name: "Run now" })
  await waitFor(() =>
    expect(screen.queryByText("Loading executions…")).toBeNull()
  )
  return run
}
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("workflow overview", () => {
  it("shows an explicit access refusal and an exit when an edited workflow is no longer owned", async () => {
    let revoked = false
    backend({
      fail: (method, path) =>
        revoked && method === "GET" && path.endsWith(weekly.id)
          ? refusal(403, "forbidden", "access rejected SECRET")
          : undefined,
    })
    const { client } = overview()
    await open()
    fireEvent.click(screen.getByRole("button", { name: "Edit workflow" }))
    revoked = true
    await client.invalidateQueries({
      queryKey: agentKeys.workflow(ORG, weekly.id),
      exact: true,
    })
    await screen.findByText("You can’t access this workflow or execution.")
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull()
    expect(document.body.textContent).not.toContain("SECRET")
    fireEvent.click(screen.getByRole("button", { name: "Cancel editing" }))
    expect(
      (
        screen.getByRole("button", {
          name: "Close Weekly review workflow",
        }) as HTMLButtonElement
      ).disabled
    ).toBe(false)
  })

  it("shows that pending workflow approvals cannot be continued from this surface", async () => {
    backend({
      executions: [
        {
          ...execution,
          status: "needs_confirmation",
          result: {
            team_id: crew.id,
            results: [
              {
                agent_id: scout.id,
                agent_name: "Scout",
                status: "needs_confirmation",
                pending_confirmations: ["internal_confirmation_id"],
              },
            ],
          },
        },
      ],
    })
    overview()
    await open()
    fireEvent.click(
      screen.getByRole("button", {
        name: /^View Waiting for your approval .* execution$/,
      })
    )
    await screen.findByText(
      "This assignment needs your approval. It cannot be resumed from this view yet."
    )
    expect(
      screen.getByText(/1 action needs your approval. It hasn't completed./)
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: /approve|resume/i })).toBeNull()
    expect(document.body.textContent).not.toContain("internal_confirmation_id")
  })
  it.each(["detail", "list"])(
    "keeps an unsaved editor mounted through a failed cached %s refetch and recovery",
    async (queryKind) => {
      let failRefresh = false
      const api = backend({
        fail: (method, path) =>
          failRefresh &&
          method === "GET" &&
          path.endsWith(queryKind === "detail" ? weekly.id : "/agent-workflows")
            ? refusal(500, "internal", "upstream SECRET")
            : undefined,
      })
      const { client } = overview()
      await open()
      fireEvent.click(screen.getByRole("button", { name: "Edit workflow" }))
      const task = screen.getByLabelText("Task for assignment 1")
      fireEvent.change(task, { target: { value: "Keep this unsaved draft" } })
      failRefresh = true
      await client.invalidateQueries({
        queryKey:
          queryKind === "detail"
            ? agentKeys.workflow(ORG, weekly.id)
            : agentKeys.workflowList(ORG),
        exact: true,
      })
      await screen.findByText(
        queryKind === "detail"
          ? "Could not refresh this workflow. Your draft is kept."
          : "Could not load your workflows."
      )
      expect(screen.getByLabelText("Task for assignment 1")).toBe(task)
      expect((task as HTMLTextAreaElement).value).toBe(
        "Keep this unsaved draft"
      )
      expect(
        (
          screen.getByRole("button", {
            name: "Save changes",
          }) as HTMLButtonElement
        ).disabled
      ).toBe(true)
      expect(document.body.textContent).not.toContain("SECRET")
      failRefresh = false
      fireEvent.click(screen.getByRole("button", { name: "Try again" }))
      await waitFor(() =>
        expect(
          (
            screen.getByRole("button", {
              name: "Save changes",
            }) as HTMLButtonElement
          ).disabled
        ).toBe(false)
      )
      expect(screen.getByLabelText("Task for assignment 1")).toBe(task)
      expect((task as HTMLTextAreaElement).value).toBe(
        "Keep this unsaved draft"
      )
      fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
      await screen.findByText("Keep this unsaved draft")
      expect(api.writes[0].body).toMatchObject({
        assignments: [
          {
            agent_id: scout.id,
            task: "Keep this unsaved draft",
            context: "Use the weekly notes",
          },
        ],
      })
    }
  )
  it("keeps each workflow readable when its history fails and retries before offering a run", async () => {
    let unavailable = true
    const api = backend({
      fail: (_method, path) =>
        unavailable && path.endsWith("/executions")
          ? refusal(500, "internal", "database: SECRET")
          : undefined,
    })
    overview()
    const run = await open()
    expect((run as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText("Summarise the week")).toBeTruthy()
    expect(screen.getByText("Could not load executions.")).toBeTruthy()
    expect(document.body.textContent).not.toContain("SECRET")
    unavailable = false
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    await screen.findByText("No executions yet.")
    expect((run as HTMLButtonElement).disabled).toBe(false)
    expect(api.writes).toEqual([])
  })

  it("reports a detail that disappeared, without changing the remaining overview list", async () => {
    backend({
      fail: (_method, path) =>
        path.endsWith(weekly.id)
          ? refusal(404, "not_found", "workflow not found")
          : undefined,
    })
    overview()
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Weekly review workflow" })
    )
    await screen.findByText("It no longer exists.")
    expect(screen.getByText("Weekly review")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Run now" })).toBeNull()
  })

  it("keeps a refused scheduled edit in place and shows a useful service message", async () => {
    const api = backend({
      fail: (method) =>
        method === "PATCH"
          ? refusal(
              503,
              "agent_workflows_unavailable",
              "EventBridge ARN SECRET"
            )
          : undefined,
    })
    overview()
    await open()
    fireEvent.click(screen.getByRole("button", { name: "Edit workflow" }))
    fireEvent.change(screen.getByLabelText("Schedule expression"), {
      target: { value: "rate(1 day)" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await screen.findByText(
      "Workflow scheduling isn't available on this server. Your changes haven't been saved."
    )
    expect(
      (screen.getByLabelText("Schedule expression") as HTMLInputElement).value
    ).toBe("rate(1 day)")
    expect(document.body.textContent).not.toContain("SECRET")
    expect(api.writes.length).toBe(1)
  })

  it("leaves a workflow intact when its schedule cannot be deleted", async () => {
    const api = backend({
      fail: (method) =>
        method === "DELETE"
          ? refusal(503, "agent_workflows_unavailable", "scheduler SECRET")
          : undefined,
    })
    overview()
    await open()
    fireEvent.click(screen.getByRole("button", { name: "Delete workflow" }))
    fireEvent.click(
      screen.getByRole("button", { name: "Delete Weekly review" })
    )
    await screen.findByText(
      "The workflow schedule can't be removed on this server. The workflow hasn't been deleted."
    )
    expect(screen.getByText("Weekly review")).toBeTruthy()
    expect(document.body.textContent).not.toContain("SECRET")
    expect(api.writes.length).toBe(1)
  })
  it("creates team-bound assignments and a schedule through the real client, without granting a role", async () => {
    const api = backend({ workflows: [] })
    overview()
    const button = await screen.findByRole("button", { name: "New workflow" })
    await waitFor(() =>
      expect((button as HTMLButtonElement).disabled).toBe(false)
    )
    fireEvent.click(button)
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "  Morning   review " },
    })
    fireEvent.change(screen.getByLabelText("Agent team"), {
      target: { value: crew.id },
    })
    fireEvent.change(screen.getByLabelText("Agent for assignment 1"), {
      target: { value: scout.id },
    })
    fireEvent.change(screen.getByLabelText("Task for assignment 1"), {
      target: { value: " Review notes " },
    })
    fireEvent.click(screen.getByRole("button", { name: "Add assignment" }))
    expect(
      (
        within(screen.getByLabelText("Agent for assignment 2")).getByRole(
          "option",
          { name: "Scout" }
        ) as HTMLOptionElement
      ).disabled
    ).toBe(true)
    fireEvent.change(screen.getByLabelText("Agent for assignment 2"), {
      target: { value: quill.id },
    })
    fireEvent.change(screen.getByLabelText("Task for assignment 2"), {
      target: { value: "Write summary" },
    })
    fireEvent.click(screen.getByLabelText("Run on a schedule"))
    fireEvent.change(screen.getByLabelText("Schedule expression"), {
      target: { value: "rate(1 day)" },
    })
    fireEvent.change(screen.getByLabelText("Timezone"), {
      target: { value: "Asia/Kolkata" },
    })
    fireEvent.change(screen.getByLabelText("Schedule status"), {
      target: { value: "disabled" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Create workflow" }))
    await screen.findByRole("button", { name: "Close Morning review workflow" })
    expect(api.writes).toEqual([
      {
        method: "POST",
        path: `/v1/organizations/${ORG}/agent-workflows`,
        body: {
          name: "Morning review",
          description: "",
          team_id: crew.id,
          assignments: [
            { agent_id: scout.id, task: "Review notes", context: "" },
            { agent_id: quill.id, task: "Write summary", context: "" },
          ],
          schedule: {
            expression: "rate(1 day)",
            timezone: "Asia/Kolkata",
            state: "disabled",
          },
        },
      },
    ])
  })

  it("edits status and removes a schedule without sending schedule and clear_schedule together", async () => {
    const api = backend()
    overview()
    await open()
    fireEvent.click(screen.getByRole("button", { name: "Edit workflow" }))
    fireEvent.change(screen.getByLabelText("Workflow status"), {
      target: { value: "disabled" },
    })
    fireEvent.click(screen.getByLabelText("Run on a schedule"))
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await screen.findByText(
      "This workflow is paused. Edit it to make it active before running."
    )
    expect(api.writes[0].body).toMatchObject({
      status: "disabled",
      clear_schedule: true,
    })
    expect(api.writes[0].body).not.toHaveProperty("schedule")
  })

  it("starts once while pending, shows a queued execution and loads full detail", async () => {
    let release!: () => void
    const api = backend({
      gate: new Promise<void>((resolve) => {
        release = resolve
      }),
    })
    overview()
    const button = await open()
    fireEvent.click(button)
    fireEvent.click(button)
    await waitFor(() => expect(api.writes.length).toBe(1))
    expect(
      screen.getByRole("button", { name: "Starting…" }).hasAttribute("disabled")
    ).toBe(true)
    release()
    await screen.findByRole("region", { name: "Execution detail" })
    expect(
      await screen.findByText("Queued. The assignments haven't finished yet.")
    ).toBeTruthy()
    expect(screen.queryByText("Succeeded")).toBeNull()
    expect(api.writes[0]).toEqual({
      method: "POST",
      path: `/v1/organizations/${ORG}/agent-workflows/${weekly.id}/executions`,
      body: undefined,
    })
    expect(
      api.fetchMock.mock.calls.some(([path]) =>
        String(path).includes(`/agent-workflow-executions/${execution.id}`)
      )
    ).toBe(true)
  })

  it("deletes only after a named confirmation, invalidates the list and prevents duplicate deletion", async () => {
    let release!: () => void
    const api = backend({
      gate: new Promise<void>((resolve) => {
        release = resolve
      }),
    })
    overview()
    await open()
    fireEvent.click(screen.getByRole("button", { name: "Delete workflow" }))
    const confirm = screen.getByRole("group", {
      name: "Delete “Weekly review”?",
    })
    expect(api.writes).toEqual([])
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete Weekly review" })
    )
    fireEvent.click(within(confirm).getByRole("button", { name: "Deleting…" }))
    await waitFor(() => expect(api.writes.length).toBe(1))
    release()
    await screen.findByText(/^No workflows yet\./)
    expect(api.writes[0].method).toBe("DELETE")
  })

  it("shows failed/incomplete and approval-needed assignment outcomes without infrastructure diagnostics", async () => {
    const failed: WorkflowExecution = {
      ...execution,
      status: "succeeded",
      step_functions_execution_arn: "arn:aws:SECRET",
      error: "postgres://SECRET",
      result: {
        team_id: crew.id,
        results: [
          {
            agent_id: scout.id,
            agent_name: "Scout",
            status: "needs_input",
            output: "Please provide the weekly notes.",
          },
        ],
      },
    }
    backend({ executions: [failed] })
    overview()
    await open()
    fireEvent.click(
      screen.getByRole("button", { name: /^View Failed .* execution$/ })
    )
    const detail = await screen.findByRole("region", {
      name: "Execution detail",
    })
    expect(await within(detail).findByText("Needs your input")).toBeTruthy()
    expect(
      within(detail).getByText("Please provide the weekly notes.")
    ).toBeTruthy()
    expect(within(detail).queryByText("Succeeded")).toBeNull()
    expect(document.body.textContent).not.toContain("SECRET")
    expect(
      executionStatus({
        ...failed,
        result: {
          team_id: crew.id,
          results: [
            {
              agent_id: scout.id,
              agent_name: "Scout",
              status: "needs_confirmation",
            },
          ],
        },
      })
    ).toBe("needs_confirmation")
  })

  it.each([404, 503])(
    "treats optional workflow service %s as unavailable",
    async (status) => {
      backend({
        fail: (_method, path) =>
          path.endsWith("/agent-workflows")
            ? refusal(status, "agent_workflows_unavailable", "not configured")
            : undefined,
      })
      overview()
      await screen.findByText("Workflows aren't set up on this server.")
      expect(
        (
          screen.getByRole("button", {
            name: "New workflow",
          }) as HTMLButtonElement
        ).disabled
      ).toBe(true)
    }
  )

  it("allows preparing paused teams but refuses running paused workflow/team/assignments", async () => {
    backend({ teams: [{ ...crew, status: "disabled" }] })
    overview()
    await open()
    expect(
      screen.getByText(
        "The team is paused. Resume it in Multi-agent teams before running."
      )
    ).toBeTruthy()
    expect(
      (screen.getByRole("button", { name: "Run now" }) as HTMLButtonElement)
        .disabled
    ).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "Edit workflow" }))
    expect(
      screen.getByText(
        "You can prepare this workflow while the team is paused. Resume the team before running it."
      )
    ).toBeTruthy()
    expect(
      (
        screen.getByRole("button", {
          name: "Save changes",
        }) as HTMLButtonElement
      ).disabled
    ).toBe(false)
  })

  it("handles run refusal without optimistic success or raw debug output", async () => {
    backend({
      fail: (method) =>
        method === "POST"
          ? refusal(
              503,
              "agent_workflows_unavailable",
              "arn:aws:SECRET unavailable"
            )
          : undefined,
    })
    overview()
    fireEvent.click(await open())
    await screen.findByText("Running workflows isn't available on this server.")
    expect(document.body.textContent).not.toContain("SECRET")
    expect(
      screen.queryByRole("region", { name: "Execution detail" })
    ).toBeNull()
  })
})

describe("workflow form", () => {
  function form() {
    return wrapper(
      <WorkflowForm
        organizationId={ORG}
        teams={[crew]}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    )
  }
  function fill() {
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Review" },
    })
    fireEvent.change(screen.getByLabelText("Agent team"), {
      target: { value: crew.id },
    })
    fireEvent.change(screen.getByLabelText("Agent for assignment 1"), {
      target: { value: scout.id },
    })
    fireEvent.change(screen.getByLabelText("Task for assignment 1"), {
      target: { value: "Review the notes" },
    })
  }
  it("validates required fields, exact rune bounds, timezone and schedule before sending", () => {
    const api = backend()
    form()
    fireEvent.click(screen.getByRole("button", { name: "Create workflow" }))
    expect(screen.getByText("Give the workflow a name.")).toBeTruthy()
    fill()
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "🐈".repeat(81) },
    })
    fireEvent.change(screen.getByLabelText(/Description/), {
      target: { value: "x".repeat(1001) },
    })
    fireEvent.change(screen.getByLabelText("Task for assignment 1"), {
      target: { value: "x".repeat(10001) },
    })
    fireEvent.change(screen.getByLabelText(/Context for assignment 1/), {
      target: { value: "x".repeat(10001) },
    })
    fireEvent.click(screen.getByLabelText("Run on a schedule"))
    fireEvent.change(screen.getByLabelText("Schedule expression"), {
      target: { value: "each day" },
    })
    fireEvent.change(screen.getByLabelText("Timezone"), {
      target: { value: "Mars/Olympus" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Create workflow" }))
    expect(screen.getByText("Use at most 80 characters.")).toBeTruthy()
    expect(screen.getByText("Use at most 1,000 characters.")).toBeTruthy()
    expect(screen.getAllByText("Use at most 10,000 characters.").length).toBe(2)
    expect(
      screen.getByText("Use an IANA timezone, such as UTC or Asia/Kolkata.")
    ).toBeTruthy()
    expect(api.writes).toEqual([])
  })
  it("resets assignments on team changes and never sends an agent from another team", () => {
    const api = backend()
    wrapper(
      <WorkflowForm
        organizationId={ORG}
        teams={[
          crew,
          team({ id: "atm_other", name: "Other" }, [
            { agent: primary, role: "Wrong" },
          ]),
        ]}
        onDone={vi.fn()}
        onCancel={vi.fn()}
      />
    )
    fill()
    fireEvent.change(screen.getByLabelText("Agent team"), {
      target: { value: "atm_other" },
    })
    expect(
      (screen.getByLabelText("Task for assignment 1") as HTMLTextAreaElement)
        .value
    ).toBe("")
    fireEvent.click(screen.getByRole("button", { name: "Create workflow" }))
    expect(screen.getByText("Choose an agent on this team.")).toBeTruthy()
    expect(api.writes).toEqual([])
  })
  it("keeps field conflict visible and does not submit twice", async () => {
    const api = backend({
      fail: () => refusal(409, "name_conflict", "name conflict"),
    })
    form()
    fill()
    const submit = screen.getByRole("button", { name: "Create workflow" })
    fireEvent.click(submit)
    fireEvent.click(submit)
    await screen.findByText("You already have a workflow called “Review”.")
    expect(api.writes.length).toBe(1)
    expect(screen.getByLabelText("Name").getAttribute("aria-invalid")).toBe(
      "true"
    )
  })
})
