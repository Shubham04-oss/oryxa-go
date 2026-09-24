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

const session: Session = {
  user: { id: VIEWER, email: "member@example.com", display_name: "Member" },
  organizations: [{ id: ORG, name: "Simulated Org", slug: "sim" }],
}
const scout = agent({
  id: "agt_scout",
  name: "Scout",
  description: "Finds and checks sources",
  instructions: "Check every claim against two sources.",
})

function renderSpecialists(backend: FakeAgentsBackend) {
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
        initialSection="specialists"
      />
    </QueryClientProvider>
  )
}

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

function workPanel(name: string) {
  return within(screen.getByRole("region", { name: `${name} work panel` }))
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

describe("Specialists", () => {
  it("lists specialists with their status and says so when there are none", async () => {
    renderSpecialists(fakeAgentsBackend())
    expect(await screen.findByText("No specialists yet.")).toBeTruthy()
    cleanup()

    renderSpecialists(
      fakeAgentsBackend({
        agents: [
          primary,
          scout,
          agent({ id: "agt_q", name: "Quill", status: "disabled" }),
        ],
      })
    )
    const card = await screen.findByRole("button", { name: "Open Scout" })
    expect(within(card).getByText("Finds and checks sources")).toBeTruthy()
    expect(within(card).getByText("Active")).toBeTruthy()
    expect(within(card).getByText(/^Created /)).toBeTruthy()
    expect(
      within(screen.getByRole("button", { name: "Open Quill" })).getByText(
        "Paused"
      )
    ).toBeTruthy()
    // The primary agent is not a specialist.
    expect(screen.queryByRole("button", { name: "Open Leo" })).toBeNull()
    expect(screen.getByText("2 specialists")).toBeTruthy()
  })

  it("creates a specialist through the plain form and opens it", async () => {
    const backend = fakeAgentsBackend()
    renderSpecialists(backend)
    await screen.findByText("No specialists yet.")

    fireEvent.click(screen.getByRole("button", { name: "New specialist" }))
    const form = screen.getByRole("form", { name: "New specialist" })
    fireEvent.click(
      within(form).getByRole("button", { name: "Create specialist" })
    )
    expect(within(form).getByText("Give it a name.")).toBeTruthy()
    expect(
      within(form).getByText("Write the instructions it follows.")
    ).toBeTruthy()
    expect(backend.writes()).toEqual([])

    fill("Name", "  Pipeline   reviewer ")
    fill("Description optional", "Reviews the sales pipeline")
    fill("Instructions", "Flag stalled deals.\n")
    fireEvent.click(
      within(form).getByRole("button", { name: "Create specialist" })
    )

    expect(
      await screen.findByRole("heading", {
        name: "Pipeline reviewer",
        level: 1,
      })
    ).toBeTruthy()
    expect(backend.writes()).toEqual([
      {
        method: "POST",
        path: `/v1/organizations/${ORG}/agents`,
        body: {
          name: "Pipeline reviewer",
          description: "Reviews the sales pipeline",
          instructions: "Flag stalled deals.",
        },
      },
    ])
    expect(screen.getByText("Specialist · Active")).toBeTruthy()
    // No chat is faked before the specialist's chat object exists.
    expect(
      screen.getByText(
        "Chat with Pipeline reviewer opens in the agent panel soon."
      )
    ).toBeTruthy()
    expect(
      screen.queryByRole("textbox", { name: /Message Pipeline reviewer/ })
    ).toBeNull()
  })

  it("keeps the form open with the name refused when it is taken", async () => {
    const backend = fakeAgentsBackend({ agents: [primary, scout] })
    renderSpecialists(backend)
    await screen.findByRole("button", { name: "Open Scout" })

    fireEvent.click(screen.getByRole("button", { name: "New specialist" }))
    fill("Name", "scout")
    fill("Instructions", "Anything.")
    fireEvent.click(screen.getByRole("button", { name: "Create specialist" }))

    const name = screen.getByLabelText("Name")
    await waitFor(() => expect(name.getAttribute("aria-invalid")).toBe("true"))
    const error = document.getElementById(
      name.getAttribute("aria-describedby")!
    )
    expect(error?.textContent).toBe(
      "“scout” is taken. Agent names are unique across everyone’s agents in this workspace."
    )
    expect(screen.getByRole("form", { name: "New specialist" })).toBeTruthy()
    expect(backend.state.agents).toHaveLength(2)

    // Another member's agent can hold the name too: the server's 409 is the
    // authority, whatever this member's own list shows.
    backend.failNext("POST", /\/agents$/, {
      status: 409,
      code: "name_conflict",
      message: "agent name is already in use",
    })
    fill("Name", "Atlas")
    fireEvent.click(screen.getByRole("button", { name: "Create specialist" }))
    await waitFor(() =>
      expect(
        screen.getByText(
          "“Atlas” is taken. Agent names are unique across everyone’s agents in this workspace."
        )
      ).toBeTruthy()
    )
  })

  it("edits, pauses, resumes and deletes a specialist", async () => {
    const backend = fakeAgentsBackend({ agents: [primary, scout] })
    renderSpecialists(backend)
    fireEvent.click(await screen.findByRole("button", { name: "Open Scout" }))
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    const panel = workPanel("Scout")
    expect(panel.getByText("Finds and checks sources")).toBeTruthy()

    fireEvent.click(panel.getByRole("button", { name: "Edit" }))
    fill("Description optional", "Checks sources twice")
    fireEvent.click(panel.getByRole("button", { name: "Save changes" }))
    await waitFor(() =>
      expect(panel.getByText("Checks sources twice")).toBeTruthy()
    )

    fireEvent.click(panel.getByRole("button", { name: "Pause" }))
    await waitFor(() =>
      expect(screen.getByText("Specialist · Paused")).toBeTruthy()
    )
    expect(
      screen.getByText("Scout is paused. Resume it to chat with it.")
    ).toBeTruthy()
    fireEvent.click(panel.getByRole("button", { name: "Resume" }))
    await waitFor(() =>
      expect(screen.getByText("Specialist · Active")).toBeTruthy()
    )

    fireEvent.click(panel.getByRole("button", { name: "Delete…" }))
    const confirm = panel.getByRole("group", { name: "Delete Scout?" })
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete specialist" })
    )
    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Specialists", level: 1 })
      ).toBeTruthy()
    )
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Open Scout" })).toBeNull()
    )

    expect(backend.writes()).toEqual([
      {
        method: "PATCH",
        path: `/v1/organizations/${ORG}/agents/${scout.id}`,
        body: { description: "Checks sources twice" },
      },
      {
        method: "PATCH",
        path: `/v1/organizations/${ORG}/agents/${scout.id}`,
        body: { status: "disabled" },
      },
      {
        method: "PATCH",
        path: `/v1/organizations/${ORG}/agents/${scout.id}`,
        body: { status: "active" },
      },
      {
        method: "DELETE",
        path: `/v1/organizations/${ORG}/agents/${scout.id}`,
        body: undefined,
      },
    ])
  })

  it("won't delete a specialist the server would refuse to", async () => {
    const onTeam = team({ id: "atm_crew", name: "Pipeline crew" }, [
      { agent: scout, role: "Research" },
    ])
    const loner = agent({ id: "agt_loner", name: "Loner" })
    const backend = fakeAgentsBackend({
      agents: [primary, loner, scout],
      teams: [onTeam],
    })
    renderSpecialists(backend)

    // On one of the member's teams: known in advance.
    fireEvent.click(await screen.findByRole("button", { name: "Open Scout" }))
    await waitFor(() =>
      expect(workPanel("Scout").getByText("Pipeline crew")).toBeTruthy()
    )
    fireEvent.click(workPanel("Scout").getByRole("button", { name: "Delete…" }))
    const confirm = screen.getByRole("group", { name: "Delete Scout?" })
    expect(
      within(confirm).getByText(
        "It's on Pipeline crew. Take it off that team first."
      )
    ).toBeTruthy()
    expect(
      within(confirm)
        .getByRole("button", { name: "Delete specialist" })
        .hasAttribute("disabled")
    ).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "Back to workspace" }))

    // In a teamspace: only the server knows, and its 409 is shown.
    backend.failNext("DELETE", /\/agents\/agt_loner$/, {
      status: 409,
      code: "agent_in_use",
      message:
        "agent belongs to an agent team: remove the specialist from its teams and teamspaces first",
    })
    fireEvent.click(await screen.findByRole("button", { name: "Open Loner" }))
    fireEvent.click(workPanel("Loner").getByRole("button", { name: "Delete…" }))
    fireEvent.click(screen.getByRole("button", { name: "Delete specialist" }))
    expect(
      await screen.findByText(
        "Loner is on a team or in a teamspace. Take it off first."
      )
    ).toBeTruthy()
    expect(backend.state.agents.map((entry) => entry.name)).toContain("Loner")
  })

  it("shows its instructions and the teams it works in", async () => {
    const onTeam = team({ id: "atm_crew", name: "Pipeline crew" }, [
      { agent: scout, role: "Research" },
    ])
    renderSpecialists(
      fakeAgentsBackend({ agents: [primary, scout], teams: [onTeam] })
    )
    fireEvent.click(await screen.findByRole("button", { name: "Open Scout" }))

    fireEvent.click(screen.getByRole("button", { name: "Instructions" }))
    expect(
      workPanel("Scout").getByText("Check every claim against two sources.")
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Teams" }))
    const panel = workPanel("Scout")
    expect(await panel.findByText("Research")).toBeTruthy()

    fireEvent.click(panel.getByRole("button", { name: "Open Pipeline crew" }))
    expect(
      screen.getByRole("heading", { name: "Pipeline crew", level: 1 })
    ).toBeTruthy()
  })
})
