// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { WorkspaceShell } from "./workspace-shell"
import { AgentsFocus } from "./agents-mode"
import {
  ORG,
  VIEWER,
  agent,
  fakeAgentsBackend,
  primary,
  request,
  team,
} from "./agents-fake-backend"
import type { FakeAgentsBackend } from "./agents-fake-backend"
import type { Session } from "@/lib/auth/client"
import type { AgentWorkflow } from "@/lib/agents/agent-workflows"

// A specialist's focused view holds its chat object's socket; the Agents
// SDK stands in (src/test/room-socket.ts, src/test/agent-chat.ts).
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)
vi.mock(
  "@cloudflare/ai-chat/react",
  async () => (await import("@/test/agent-chat")).aiChatReact
)

const session: Session = {
  user: { id: VIEWER, email: "member@example.com", display_name: "Member" },
  organizations: [{ id: ORG, name: "Simulated Org", slug: "sim" }],
}

const scout = agent({
  id: "agt_scout",
  name: "Scout",
  description: "Finds and checks sources",
})
const quill = agent({ id: "agt_quill", name: "Quill", status: "disabled" })
const crew = team(
  {
    id: "atm_crew",
    name: "Pipeline crew",
    description: "Keeps the pipeline honest",
  },
  [{ agent: scout, role: "Research" }]
)
const weekly: AgentWorkflow = {
  id: "awf_weekly",
  organization_id: ORG,
  owner_user_id: VIEWER,
  name: "Weekly review",
  team_id: crew.id,
  status: "active",
  assignments: [{ agent_id: scout.id, task: "Summarise the week" }],
  schedule: { expression: "rate(7 days)", timezone: "UTC", state: "enabled" },
  revision: 1,
  created_at: "2026-09-20T10:00:00Z",
  updated_at: "2026-09-20T10:00:00Z",
}

function renderShell(
  backend: FakeAgentsBackend,
  section: "agents" | "multi-agents" | "specialists" = "agents"
) {
  vi.stubGlobal("fetch", vi.fn(backend.fetch))
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <WorkspaceShell
        session={session}
        selectedOrganization={session.organizations[0]}
        initialMode="agents"
        initialSection={section}
      />
    </QueryClientProvider>
  )
}

function stat(label: string) {
  const term = screen.getByText(label, { selector: "dt" })
  return within(term.parentElement!)
}

beforeEach(() => {
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("Agent overview", () => {
  it("shows the primary agent, counts, work and workflows from the service", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, quill, scout],
      teams: [crew],
      workflows: [weekly],
      executions: {
        [weekly.id]: [
          {
            id: "awx_2",
            workflow_id: weekly.id,
            organization_id: ORG,
            owner_user_id: VIEWER,
            started_by: "schedule",
            status: "succeeded",
            created_at: "2026-09-20T09:00:00Z",
            completed_at: "2026-09-20T09:01:00Z",
          },
        ],
      },
      inbox: [
        request({ id: "req_open", status: "pending" }),
        request({ id: "req_done", status: "completed" }),
        request({
          id: "req_lapsed",
          status: "pending",
          expires_at: "2000-01-01T00:00:00Z",
        }),
      ],
    })
    renderShell(backend)

    const primaryCard = await screen.findByRole("region", {
      name: "Primary agent",
    })
    expect(within(primaryCard).getByText("Leo")).toBeTruthy()
    expect(within(primaryCard).getByText("Active")).toBeTruthy()
    expect(
      screen.getByRole("heading", { name: "Agent overview", level: 1 })
    ).toBeTruthy()

    // The hero's numbers: what Go can count, inline.
    await waitFor(() => expect(stat("team").getByText("1")).toBeTruthy())
    expect(stat("specialists").getByText("2")).toBeTruthy()
    // Only the unexpired pending request waits on the member.
    await waitFor(() =>
      expect(stat("waiting for you").getByText("1")).toBeTruthy()
    )
    // No count Go can't give: answered today, files made, usage.
    expect(
      screen.queryByText(/answered today|files made|times this week/)
    ).toBeNull()

    // The board's sections, each with its count and a way to make another.
    for (const heading of [
      "Multi-agent teams · 1",
      "Specialists · 2",
      "Workflows · 1",
    ]) {
      expect(
        await screen.findByRole("heading", { name: heading, level: 2 })
      ).toBeTruthy()
    }
    for (const action of ["New team", "New specialist", "New workflow"]) {
      expect(screen.getByRole("button", { name: action })).toBeTruthy()
    }

    for (const name of ["Pipeline crew", "Scout", "Quill"]) {
      expect(screen.getByRole("button", { name: `Open ${name}` })).toBeTruthy()
    }
    const quillCard = screen.getByRole("article", { name: "Quill" })
    expect(within(quillCard).getByText("Specialist · Paused")).toBeTruthy()
    expect(within(quillCard).getByText(/^Paused · updated /)).toBeTruthy()
    const crewCard = screen.getByRole("button", { name: "Open Pipeline crew" })
    expect(
      within(crewCard).getByText(
        "Keeps the pipeline honest · led by Leo, 1 specialist"
      )
    ).toBeTruthy()

    const workflow = screen.getByText("Weekly review").closest("li")!
    expect(
      within(workflow).getByText("Pipeline crew · rate(7 days), UTC")
    ).toBeTruthy()
    expect(await within(workflow).findByText(/^Succeeded /)).toBeTruthy()

    // Nothing from the old example data.
    for (const example of [
      "Launch crew",
      "Research specialist",
      "Review agent",
    ])
      expect(screen.queryByText(example)).toBeNull()
  })

  it("says plainly when there is nothing yet", async () => {
    renderShell(fakeAgentsBackend())

    expect(await screen.findByText(/^No specialists yet\./)).toBeTruthy()
    expect(await screen.findByText(/^No teams yet\./)).toBeTruthy()
    expect(await screen.findByText(/^No workflows yet\./)).toBeTruthy()
    expect(stat("specialists").getByText("0")).toBeTruthy()
    await waitFor(() =>
      expect(stat("waiting for you").getByText("0")).toBeTruthy()
    )
    // A team needs a specialist first, and says so.
    expect(
      screen.getByRole<HTMLButtonElement>("button", { name: "New team" })
        .disabled
    ).toBe(true)

    // New specialist opens its form right here.
    fireEvent.click(screen.getByRole("button", { name: "New specialist" }))
    expect(screen.getByRole("form", { name: "New specialist" })).toBeTruthy()
  })

  it("names what this server doesn't run instead of failing", async () => {
    const backend = fakeAgentsBackend({ agents: [primary, scout] })
    backend.unavailable("agent-teams")
    backend.unavailable("agent-workflows")
    backend.unavailable("agent-requests")
    renderShell(backend)

    expect(
      await screen.findByText("Multi-agent teams aren't set up on this server.")
    ).toBeTruthy()
    expect(
      await screen.findByText("Workflows aren't set up on this server.")
    ).toBeTruthy()
    await waitFor(() =>
      expect(stat("waiting for you").getByText("–")).toBeTruthy()
    )
    expect(screen.getByRole("button", { name: "Open Scout" })).toBeTruthy()
  })

  it("pauses a specialist from its card's menu", async () => {
    const backend = fakeAgentsBackend({ agents: [primary, scout] })
    renderShell(backend)

    const card = await screen.findByRole("article", { name: "Scout" })
    fireEvent.click(
      within(card).getByRole("button", { name: "More for Scout" })
    )
    fireEvent.click(await screen.findByRole("menuitem", { name: "Pause" }))
    await waitFor(() =>
      expect(within(card).getByText("Specialist · Paused")).toBeTruthy()
    )
    expect(backend.writes()).toEqual([
      {
        method: "PATCH",
        path: `/v1/organizations/${ORG}/agents/${scout.id}`,
        body: { status: "disabled" },
      },
    ])
  })

  it("offers to resume a paused primary agent", async () => {
    const backend = fakeAgentsBackend({
      agents: [{ ...primary, status: "disabled" }, scout],
    })
    renderShell(backend)

    const primaryCard = await screen.findByRole("region", {
      name: "Primary agent",
    })
    expect(within(primaryCard).getByText("Paused")).toBeTruthy()
    fireEvent.click(
      within(primaryCard).getByRole("button", { name: "Resume Leo" })
    )

    await waitFor(() =>
      expect(within(primaryCard).getByText("Active")).toBeTruthy()
    )
    expect(backend.writes()).toEqual([
      {
        method: "PATCH",
        path: `/v1/organizations/${ORG}/agents/${primary.id}`,
        body: { status: "active" },
      },
    ])
  })

  it("opens a team as focused work without the left sidebar, and returns", async () => {
    renderShell(fakeAgentsBackend({ agents: [primary, scout], teams: [crew] }))

    fireEvent.click(
      await screen.findByRole("button", { name: "Open Pipeline crew" })
    )
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    expect(
      screen.getByRole("heading", { name: "Pipeline crew", level: 1 })
    ).toBeTruthy()
    expect(screen.getByText("Multi-agent team · Active")).toBeTruthy()
    const panel = screen.getByRole("region", {
      name: "Pipeline crew work panel",
    })
    expect(panel.contains(screen.getByLabelText("Work views"))).toBe(false)
    expect(
      screen.getByRole("textbox", { name: "Message Pipeline crew" })
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Back to workspace" }))
    expect(
      screen.getByRole("complementary", { name: "Workspace navigation" })
    ).toBeTruthy()
    expect(
      screen.getByRole("heading", { name: "Agent overview", level: 1 })
    ).toBeTruthy()
  })
})

describe("Agents mode without a workspace", () => {
  it("shows no example agents in the preview", () => {
    const fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <WorkspaceShell initialMode="agents" />
      </QueryClientProvider>
    )

    expect(
      screen.getByText("Your agents appear here once you're signed in.")
    ).toBeTruthy()
    for (const section of ["Agent teams", "Specialists"]) {
      fireEvent.click(screen.getByRole("button", { name: section }))
      expect(
        screen.getByText("Your agents appear here once you're signed in.")
      ).toBeTruthy()
    }
    const canvas = screen.getByRole("main", { name: "Specialists canvas" })
    expect(within(canvas).queryByRole("button", { name: /^Open / })).toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe("The chat seam", () => {
  it("offers Chat for a team's specialists, and a specialist's view is its chat", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      teams: [crew],
    })
    vi.stubGlobal("fetch", vi.fn(backend.fetch))
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const onChat = vi.fn()
    const onOpen = vi.fn()
    const { rerender } = render(
      <QueryClientProvider client={client}>
        <AgentsFocus
          target={{ kind: "specialist", agentId: scout.id }}
          organizationId={ORG}
          onBack={vi.fn()}
          onOpen={onOpen}
          onChat={onChat}
        />
      </QueryClientProvider>
    )

    // Its conversation column is the chat itself, so it needs no button.
    const column = await screen.findByRole("region", {
      name: "Chat with Scout",
    })
    expect(
      within(column).getByRole("textbox", { name: "Message Scout" })
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Chat with Scout" })).toBeNull()
    expect(screen.queryByText(/opens in the agent panel soon/)).toBeNull()

    rerender(
      <QueryClientProvider client={client}>
        <AgentsFocus
          target={{ kind: "team", teamId: crew.id }}
          organizationId={ORG}
          onBack={vi.fn()}
          onOpen={onOpen}
          onChat={onChat}
        />
      </QueryClientProvider>
    )
    fireEvent.click(await screen.findByRole("button", { name: "Members" }))
    fireEvent.click(screen.getByRole("button", { name: "Chat with Scout" }))
    expect(onChat).toHaveBeenCalledExactlyOnceWith(scout.id)
    // The coordinator is chatted with in the agent panel's own tab.
    expect(screen.queryByRole("button", { name: "Chat with Leo" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Open Scout" }))
    expect(onOpen).toHaveBeenCalledWith({
      kind: "specialist",
      agentId: scout.id,
    })
  })
})
