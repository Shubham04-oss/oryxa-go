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
import {
  ORG,
  VIEWER,
  agent,
  fakeAgentsBackend,
  primary,
  team,
} from "./agents-fake-backend"
import type { FakeAgentsBackend } from "./agents-fake-backend"
import type { Session } from "@/lib/auth/client"

// The shell holds the member's DM inbox open: its socket is a stand-in.
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
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
const atlas = agent({ id: "agt_atlas", name: "Atlas" })
const quill = agent({ id: "agt_quill", name: "Quill", status: "disabled" })
const crew = team(
  {
    id: "atm_crew",
    name: "Pipeline crew",
    description: "Keeps the pipeline honest",
  },
  [{ agent: scout, role: "Research" }]
)

function renderTeams(backend: FakeAgentsBackend) {
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
        initialSection="multi-agents"
      />
    </QueryClientProvider>
  )
}

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

function workPanel() {
  return within(screen.getByRole("region", { name: /work panel$/ }))
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

describe("Multi-agent teams", () => {
  it("lists teams with their agents, or says there are none", async () => {
    renderTeams(fakeAgentsBackend({ agents: [primary, scout] }))
    expect(await screen.findByText("No multi-agent teams yet.")).toBeTruthy()
    cleanup()

    renderTeams(fakeAgentsBackend({ agents: [primary, scout], teams: [crew] }))
    const card = await screen.findByRole("button", {
      name: "Open Pipeline crew",
    })
    expect(within(card).getByText("Keeps the pipeline honest")).toBeTruthy()
    expect(within(card).getByLabelText("Leo")).toBeTruthy()
    expect(within(card).getByLabelText("Scout")).toBeTruthy()
    expect(screen.getByText("1 team")).toBeTruthy()
  })

  it("creates a team from active specialists, with their roles", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, atlas, quill, scout],
    })
    renderTeams(backend)
    await screen.findByText("No multi-agent teams yet.")

    fireEvent.click(screen.getByRole("button", { name: "New team" }))
    const form = screen.getByRole("form", { name: "New multi-agent team" })
    // A paused specialist can't join.
    expect(within(form).queryByText("Quill")).toBeNull()
    fireEvent.click(within(form).getByRole("button", { name: "Create team" }))
    expect(within(form).getByText("Give the team a name.")).toBeTruthy()
    expect(within(form).getByText("Pick at least one specialist.")).toBeTruthy()

    fill("Name", "Launch  squad")
    fill("Purpose optional", "Ship the launch")
    fireEvent.click(within(form).getByRole("checkbox", { name: /Scout/ }))
    fireEvent.click(within(form).getByRole("checkbox", { name: /Atlas/ }))
    // Scout's role starts from its description; Atlas has none yet.
    expect(
      within(form).getByLabelText<HTMLInputElement>("Scout's role").value
    ).toBe("Finds and checks sources")
    fireEvent.click(within(form).getByRole("button", { name: "Create team" }))
    expect(within(form).getByText("Give Atlas a role.")).toBeTruthy()
    expect(backend.writes()).toEqual([])

    fill("Atlas's role", "Plans the rollout")
    fireEvent.click(within(form).getByRole("button", { name: "Create team" }))

    expect(
      await screen.findByRole("heading", { name: "Launch squad", level: 1 })
    ).toBeTruthy()
    expect(backend.writes()).toEqual([
      {
        method: "POST",
        path: `/v1/organizations/${ORG}/agent-teams`,
        body: {
          name: "Launch squad",
          description: "Ship the launch",
          members: [
            { agent_id: scout.id, role: "Finds and checks sources" },
            { agent_id: atlas.id, role: "Plans the rollout" },
          ],
        },
      },
    ])
    expect(
      screen.getByRole("textbox", { name: "Message Launch squad" })
    ).toBeTruthy()
  })

  it("won't offer a team the server would refuse", async () => {
    renderTeams(
      fakeAgentsBackend({ agents: [{ ...primary, status: "disabled" }, scout] })
    )
    const reason = await screen.findByText(
      "Leo, your primary agent, is paused. It coordinates every team, so resume it on Agent overview first."
    )
    expect(reason).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "New team" }).hasAttribute("disabled")
    ).toBe(true)
    cleanup()

    renderTeams(fakeAgentsBackend({ agents: [primary, quill] }))
    expect(
      await screen.findByText(
        "A team needs at least one active specialist. Create one in Specialists first."
      )
    ).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "New team" }).hasAttribute("disabled")
    ).toBe(true)
  })

  it("shows the server's refusals: a taken name and an invalid team", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      teams: [crew],
    })
    renderTeams(backend)
    await screen.findByRole("button", { name: "Open Pipeline crew" })

    fireEvent.click(screen.getByRole("button", { name: "New team" }))
    fill("Name", "pipeline CREW")
    fireEvent.click(screen.getByRole("checkbox", { name: /Scout/ }))
    fireEvent.click(screen.getByRole("button", { name: "Create team" }))
    expect(
      await screen.findByText("You already have a team called “pipeline CREW”.")
    ).toBeTruthy()

    backend.failNext("POST", /\/agent-teams$/, {
      status: 400,
      code: "invalid_request",
      message: "invalid agent team: an active primary agent is required",
    })
    fill("Name", "Night shift")
    fireEvent.click(screen.getByRole("button", { name: "Create team" }))
    expect(
      await screen.findByText("An active primary agent is required.")
    ).toBeTruthy()
    expect(backend.state.teams).toHaveLength(1)
  })

  it("edits, pauses, resumes and deletes a team", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, atlas, scout],
      teams: [crew],
    })
    renderTeams(backend)
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Pipeline crew" })
    )

    // A rename sends only the name: the roster did not change.
    fireEvent.click(workPanel().getByRole("button", { name: "Edit" }))
    fill("Name", "Deal desk")
    fireEvent.click(workPanel().getByRole("button", { name: "Save changes" }))
    expect(
      await screen.findByRole("heading", { name: "Deal desk", level: 1 })
    ).toBeTruthy()

    fireEvent.click(workPanel().getByRole("button", { name: "Edit" }))
    fireEvent.click(screen.getByRole("checkbox", { name: /Atlas/ }))
    fill("Atlas's role", "Plans the rollout")
    fireEvent.click(workPanel().getByRole("button", { name: "Save changes" }))
    await waitFor(() =>
      expect(
        workPanel().queryByRole("button", { name: "Save changes" })
      ).toBeNull()
    )
    fireEvent.click(screen.getByRole("button", { name: "Members" }))
    expect(workPanel().getByRole("button", { name: "Open Atlas" })).toBeTruthy()
    expect(workPanel().getByText("Plans the rollout · @atlas")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Overview" }))

    fireEvent.click(workPanel().getByRole("button", { name: "Pause" }))
    await waitFor(() =>
      expect(screen.getByText("Multi-agent team · Paused")).toBeTruthy()
    )
    const composer = screen.getByRole("textbox", { name: "Message Deal desk" })
    expect(composer.hasAttribute("disabled")).toBe(true)
    expect(
      screen.getByText("This team is paused. Resume it to send messages.")
    ).toBeTruthy()
    fireEvent.click(workPanel().getByRole("button", { name: "Resume" }))
    await waitFor(() =>
      expect(screen.getByText("Multi-agent team · Active")).toBeTruthy()
    )

    fireEvent.click(workPanel().getByRole("button", { name: "Delete…" }))
    fireEvent.click(screen.getByRole("button", { name: "Delete team" }))
    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Multi-agent teams", level: 1 })
      ).toBeTruthy()
    )
    expect(await screen.findByText("No multi-agent teams yet.")).toBeTruthy()

    expect(backend.writes()).toEqual([
      {
        method: "PATCH",
        path: `/v1/organizations/${ORG}/agent-teams/${crew.id}`,
        body: { name: "Deal desk" },
      },
      {
        method: "PATCH",
        path: `/v1/organizations/${ORG}/agent-teams/${crew.id}`,
        body: {
          members: [
            { agent_id: scout.id, role: "Research" },
            { agent_id: atlas.id, role: "Plans the rollout" },
          ],
        },
      },
      {
        method: "PATCH",
        path: `/v1/organizations/${ORG}/agent-teams/${crew.id}`,
        body: { status: "disabled" },
      },
      {
        method: "PATCH",
        path: `/v1/organizations/${ORG}/agent-teams/${crew.id}`,
        body: { status: "active" },
      },
      {
        method: "DELETE",
        path: `/v1/organizations/${ORG}/agent-teams/${crew.id}`,
        body: undefined,
      },
    ])
  })

  it("keeps Go's roster and workflow rules", async () => {
    const pausedScout = { ...scout, status: "disabled" as const }
    const backend = fakeAgentsBackend({
      agents: [primary, atlas, pausedScout],
      teams: [crew],
      workflows: [
        {
          id: "awf_weekly",
          organization_id: ORG,
          owner_user_id: VIEWER,
          name: "Weekly review",
          team_id: crew.id,
          status: "active",
          assignments: [{ agent_id: scout.id, task: "Summarise the week" }],
          revision: 1,
          created_at: "2026-09-20T10:00:00Z",
          updated_at: "2026-09-20T10:00:00Z",
        },
      ],
    })
    renderTeams(backend)
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Pipeline crew" })
    )

    // A paused member blocks a roster change until it's resumed or removed.
    fireEvent.click(workPanel().getByRole("button", { name: "Edit" }))
    fireEvent.click(screen.getByRole("checkbox", { name: /Atlas/ }))
    fill("Atlas's role", "Plans")
    fireEvent.click(workPanel().getByRole("button", { name: "Save changes" }))
    expect(
      workPanel().getByText(
        "Scout is paused. Resume it or take it off the team before changing the roster."
      )
    ).toBeTruthy()
    fireEvent.click(workPanel().getByRole("button", { name: "Cancel" }))

    // A workflow that runs the team blocks deleting it.
    await waitFor(() =>
      expect(workPanel().getByRole("button", { name: "Delete…" })).toBeTruthy()
    )
    fireEvent.click(workPanel().getByRole("button", { name: "Delete…" }))
    const confirm = screen.getByRole("group", { name: "Delete Pipeline crew?" })
    await waitFor(() =>
      expect(
        within(confirm).getByText(
          "Weekly review runs this team. Delete that workflow first."
        )
      ).toBeTruthy()
    )
    expect(
      within(confirm)
        .getByRole("button", { name: "Delete team" })
        .hasAttribute("disabled")
    ).toBe(true)
    // Workflows show only on Agent overview; the team's tabs don't add them.
    expect(screen.queryByRole("button", { name: "Workflows" })).toBeNull()
    expect(backend.writes()).toEqual([])
  })
})

describe("A team's conversation", () => {
  it("mentions a teammate, shows the post while the team answers, then the replies", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      teams: [crew],
    })
    renderTeams(backend)
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Pipeline crew" })
    )
    const log = await screen.findByRole("log", {
      name: "Conversation with Pipeline crew",
    })
    expect(
      await within(log).findByText(/^Nothing here yet\. Give the team a task/)
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Mention Scout" }))
    const composer = screen.getByRole<HTMLTextAreaElement>("textbox", {
      name: "Message Pipeline crew",
    })
    expect(composer.value).toBe("@scout ")
    expect(document.activeElement).toBe(composer)

    const release = backend.holdPosts()
    fireEvent.change(composer, { target: { value: "@scout find the deal" } })
    fireEvent.click(screen.getByRole("button", { name: "Send message" }))

    expect(await within(log).findByText("Sending…")).toBeTruthy()
    expect(within(log).getByText("@scout find the deal")).toBeTruthy()
    expect(
      within(log).getByText(
        "The team is replying. This can take up to a minute and a half."
      )
    ).toBeTruthy()
    expect(composer.hasAttribute("disabled")).toBe(true)
    expect(composer.value).toBe("")

    release()
    expect(
      await within(log).findByText("find the deal", {
        exact: false,
        selector: "strong",
      })
    ).toBeTruthy()
    await waitFor(() => expect(composer.hasAttribute("disabled")).toBe(false))
    expect(within(log).queryByText("Sending…")).toBeNull()
    expect(within(log).getByRole("article", { name: /^Leo/ })).toBeTruthy()
    expect(backend.writes()).toEqual([
      {
        method: "POST",
        path: `/v1/organizations/${ORG}/agent-teams/${crew.id}/messages`,
        body: { body: "@scout find the deal" },
      },
    ])
  })

  it("hands a refused message back to the composer", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      teams: [crew],
    })
    backend.failNext("POST", /\/messages$/, {
      status: 400,
      code: "invalid_request",
      message: "a message needs 1–4000 characters",
    })
    renderTeams(backend)
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Pipeline crew" })
    )
    const composer = screen.getByRole<HTMLTextAreaElement>("textbox", {
      name: "Message Pipeline crew",
    })
    fireEvent.change(composer, { target: { value: "Plan the week" } })
    fireEvent.click(screen.getByRole("button", { name: "Send message" }))

    expect(
      await screen.findByText("A message needs 1–4000 characters.")
    ).toBeTruthy()
    await waitFor(() => expect(composer.value).toBe("Plan the week"))
  })
})
