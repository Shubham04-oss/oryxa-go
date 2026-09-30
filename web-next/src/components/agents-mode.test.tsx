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
  request,
} from "./agents-fake-backend"
import type { FakeAgentsBackend } from "./agents-fake-backend"
import type { Session } from "@/lib/auth/client"

// The shell holds the primary agent's chat socket in its right panel; the
// Agents SDK stands in (src/test/room-socket.ts, src/test/agent-chat.ts).
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

function renderShell(backend: FakeAgentsBackend) {
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
        initialSection="agents"
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
  it("shows the primary agent and the requests waiting on the member, from the service", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary],
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

    // Only the unexpired pending request waits on the member.
    await waitFor(() =>
      expect(stat("Requests waiting").getByText("1")).toBeTruthy()
    )
    expect(
      within(
        stat("Requests waiting").getByText("For you to approve or answer")
          .parentElement!
      ).getByRole("button", { name: "View requests" })
    ).toBeTruthy()
  })

  it("shows none of the archived work: no specialists, teams or workflows", async () => {
    renderShell(fakeAgentsBackend({ agents: [primary, scout] }))

    await screen.findByRole("region", { name: "Primary agent" })
    await waitFor(() =>
      expect(stat("Requests waiting").getByText("0")).toBeTruthy()
    )
    for (const archived of [
      "Specialists",
      "Multi-agent teams",
      "Workflows",
      "Agentic work",
      "Scout",
    ])
      expect(screen.queryByText(archived)).toBeNull()
    expect(screen.queryByRole("button", { name: "Open Scout" })).toBeNull()
    expect(screen.queryByText(/No specialists or teams yet/)).toBeNull()
  })

  it("goes on to Requests", async () => {
    renderShell(fakeAgentsBackend({ agents: [primary] }))

    fireEvent.click(
      await screen.findByRole("button", { name: "View requests" })
    )

    expect(
      await screen.findByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()
  })

  it("names what this server doesn't run instead of failing", async () => {
    const backend = fakeAgentsBackend({ agents: [primary] })
    backend.unavailable("agent-requests")
    renderShell(backend)

    await screen.findByRole("region", { name: "Primary agent" })
    await waitFor(() =>
      expect(
        stat("Requests waiting").getByText("Not set up on this server")
      ).toBeTruthy()
    )
  })

  it("offers to resume a paused primary agent", async () => {
    const backend = fakeAgentsBackend({
      agents: [{ ...primary, status: "disabled" }],
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
    const navigation = screen.getByRole("navigation", {
      name: "Agents navigation",
    })
    expect(
      within(navigation)
        .getAllByRole("button")
        .map((item) => item.textContent)
    ).toEqual(["Agent overview", "Requests"])
    const canvas = screen.getByRole("main", { name: "Agent overview canvas" })
    expect(within(canvas).queryByRole("button", { name: /^Open / })).toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
