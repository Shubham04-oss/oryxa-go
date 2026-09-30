// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { WorkspaceShell } from "./workspace-shell"
import { openRequest, openRequests } from "./requests-view-open"
import {
  ORG,
  VIEWER,
  fakeAgentsBackend,
  primary,
  request,
} from "./agents-fake-backend"
import type { Role, Session } from "@/lib/auth/client"

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

const waiting = request({
  id: "irq_shell",
  status: "pending",
  task: "Share the pipeline total.",
})

const shared = request({
  id: "irq_shared",
  status: "completed",
  task: "A shared pipeline answer.",
  origin: "slack",
  result: {
    status: "completed",
    summary: "The pipeline total was shared.",
    content: "",
    tools_used: [],
    artifacts: [],
    completed_at: "2026-09-20T10:00:00Z",
  },
})

let client: QueryClient

function renderShell(
  initial: {
    section?: "agents" | "requests"
    signedIn?: boolean
    mode?: "home" | "agents"
    organizationRole?: Role
  } = {}
) {
  const backend = fakeAgentsBackend({
    agents: [primary],
    inbox: [waiting],
  })
  // The one request a link names, and the members list, beside the backend.
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const path = new URL(input, "http://localhost").pathname
      if (path === `/v1/organizations/${ORG}/agent-requests/activity`) {
        return new Response(
          JSON.stringify({
            activity: [
              {
                kind: "answered",
                role: "owner",
                request_id: shared.id,
                status: shared.status,
                with_user_id: shared.requester.user_id,
                at: shared.updated_at,
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        )
      }
      if (path === `/v1/organizations/${ORG}/agent-requests/${shared.id}`) {
        return new Response(JSON.stringify(shared), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      }
      if (path === `/v1/organizations/${ORG}/agent-requests/irq_shell`) {
        return new Response(JSON.stringify(waiting), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      }
      if (path === `/v1/organizations/${ORG}/agent-requests/irq_link`) {
        return new Response(
          JSON.stringify(
            request({
              id: "irq_link",
              status: "pending",
              task: "A linked ask.",
            })
          ),
          { status: 200, headers: { "Content-Type": "application/json" } }
        )
      }
      if (path === `/v1/organizations/${ORG}/apps/slack/install`) {
        return new Response(
          JSON.stringify({
            status: "not_installed",
            add_to_slack_url: "https://slack.com/oauth/v2/authorize?state=abc",
            add_to_slack_expires_at: "2999-01-01T00:00:00Z",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        )
      }
      return backend.fetch(input, init)
    })
  )
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const signedIn = initial.signedIn ?? true
  return render(
    <QueryClientProvider client={client}>
      <WorkspaceShell
        session={signedIn ? session : undefined}
        selectedOrganization={signedIn ? session.organizations[0] : undefined}
        organizationRole={initial.organizationRole}
        initialMode={initial.mode ?? "agents"}
        initialSection={initial.section}
      />
    </QueryClientProvider>
  )
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
  client.clear()
  window.history.replaceState({}, "", "/")
  vi.unstubAllGlobals()
})

describe("Requests in the workspace shell", () => {
  it.each(["All", "row"])(
    "opens Home's %s shortcut in Requests, selecting only a named shared-activity row",
    async (target) => {
      renderShell({ mode: "home" })
      const card = await screen.findByRole("region", {
        name:
          target === "All" ? "Needs you" : "Your requests and shared answers",
      })
      fireEvent.click(
        within(card).getByRole("button", {
          name: target === "All" ? "All requests" : /Your agent answered/,
        })
      )

      expect(
        await screen.findByRole("heading", { name: "Requests", level: 1 })
      ).toBeTruthy()
      expect(await screen.findByRole("tab", { name: /Inbox/ })).toBeTruthy()
      if (target === "row") {
        const opened = await screen.findByRole("region", {
          name: "Opened request",
        })
        await within(opened).findByText(shared.task)
        expect(within(opened).queryByText(waiting.task)).toBeNull()
      } else {
        expect(
          screen.queryByRole("region", { name: "Opened request" })
        ).toBeNull()
      }
    }
  )

  it("is the second item of Agents' navigation, and opens the page", async () => {
    renderShell()

    const navigation = await screen.findByRole("navigation", {
      name: "Agents navigation",
    })
    expect(
      within(navigation)
        .getAllByRole("button")
        .map((button) => button.textContent)
    ).toEqual(["Agent overview", "Requests"])
    fireEvent.click(
      within(navigation).getByRole("button", { name: "Requests" })
    )

    expect(
      screen.getByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()
    expect(await screen.findByRole("tab", { name: /Inbox/ })).toBeTruthy()
    await screen.findByText("Share the pipeline total.")
    expect(
      within(navigation)
        .getByRole("button", { name: "Requests" })
        .getAttribute("aria-current")
    ).toBe("page")
  })

  it("is where the Agent overview's Requests waiting tile leads", async () => {
    renderShell()

    fireEvent.click(
      await screen.findByRole("button", { name: "View requests" })
    )

    expect(
      screen.getByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()
    await screen.findByText("Share the pipeline total.")
  })

  it("opens on a request when a note or a link names one, from wherever the member is", async () => {
    renderShell({ mode: "home" })
    await screen.findByRole("heading", { name: "Today", level: 1 })

    act(() => openRequest("irq_link"))

    expect(
      await screen.findByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()
    const opened = await screen.findByRole("region", { name: "Opened request" })
    await within(opened).findByText("A linked ask.")
    expect(
      screen.getByRole("navigation", { name: "Agents navigation" })
    ).toBeTruthy()
  })

  it("opens the request an address names, as the page opens", async () => {
    window.history.replaceState(
      {},
      "",
      `/?org=${ORG}&view=requests&request=irq_link`
    )

    renderShell({ mode: "home" })

    const opened = await screen.findByRole("region", { name: "Opened request" })
    await within(opened).findByText("A linked ask.")
    expect(
      screen.getByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()
  })

  it("puts a named request away once the member goes elsewhere", async () => {
    renderShell()
    await screen.findByRole("heading", { name: "Agent overview", level: 1 })
    act(() => openRequest("irq_link"))
    await screen.findByRole("region", { name: "Opened request" })

    const navigation = screen.getByRole("navigation", {
      name: "Agents navigation",
    })
    fireEvent.click(
      within(navigation).getByRole("button", { name: "Agent overview" })
    )
    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Agent overview", level: 1 })
      ).toBeTruthy()
    )
    fireEvent.click(
      within(navigation).getByRole("button", { name: "Requests" })
    )

    await screen.findByRole("tab", { name: /Inbox/ })
    expect(screen.queryByRole("region", { name: "Opened request" })).toBeNull()
  })

  it("opens Requests with no request when none is named", async () => {
    renderShell()
    await screen.findByRole("heading", { name: "Agent overview", level: 1 })

    act(() => openRequests())

    await screen.findByRole("tab", { name: /Inbox/ })
    expect(screen.queryByRole("region", { name: "Opened request" })).toBeNull()
  })

  it.each([
    { viewport: "desktop", mobile: false, linked: false },
    { viewport: "desktop", mobile: false, linked: true },
    { viewport: "mobile", mobile: true, linked: false },
    { viewport: "mobile", mobile: true, linked: true },
  ])(
    "leaves focused primary work when Requests opens on $viewport (linked: $linked)",
    async ({ mobile, linked }) => {
      vi.stubGlobal("matchMedia", () => ({
        matches: mobile,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }))
      renderShell()
      fireEvent.click(
        screen.getByRole("button", { name: "Expand agent panel" })
      )
      fireEvent.click(
        await screen.findByRole("button", { name: "Open Agent overview" })
      )
      await screen.findByRole("region", { name: "Agent overview work panel" })
      expect(
        screen.queryByRole("complementary", { name: "Workspace navigation" })
      ).toBeNull()

      act(() => (linked ? openRequest("irq_link") : openRequests()))

      expect(
        await screen.findByRole("heading", { name: "Requests", level: 1 })
      ).toBeTruthy()
      expect(
        screen.queryByRole("region", { name: "Agent overview work panel" })
      ).toBeNull()
      expect(
        screen.queryByRole("button", { name: "Back to Agent overview" })
      ).toBeNull()
      if (mobile) {
        fireEvent.click(
          screen.getByRole("button", { name: "Expand left sidebar" })
        )
      }
      const navigation = screen.getByRole("navigation", {
        name: "Agents navigation",
      })
      expect(
        within(navigation)
          .getByRole("button", { name: "Requests" })
          .getAttribute("aria-current")
      ).toBe("page")
      if (linked) {
        const opened = await screen.findByRole("region", {
          name: "Opened request",
        })
        await within(opened).findByText("A linked ask.")
      } else {
        await screen.findByRole("tab", { name: /Inbox/ })
        expect(
          screen.queryByRole("region", { name: "Opened request" })
        ).toBeNull()
        await screen.findByText("Share the pipeline total.")
      }
    }
  )

  it("says a signed-out preview has no requests", async () => {
    renderShell({ signedIn: false, section: "requests" })

    expect(
      await screen.findByText(
        "Your requests appear here once you're signed in."
      )
    ).toBeTruthy()
  })
})

describe("Slack's install coming back to the workspace", () => {
  it("opens Settings on Slack, for an admin", async () => {
    window.history.replaceState(
      {},
      "",
      `/?org=${ORG}&view=slack&slack=connected`
    )
    vi.spyOn(window.history, "replaceState").mockImplementation(() => {})

    renderShell({ mode: "home", organizationRole: "admin" })

    const settings = await screen.findByRole("dialog", { name: "Settings" })
    expect(
      within(settings).getByRole("heading", { level: 2, name: "Slack" })
    ).toBeTruthy()
    expect(
      await within(settings).findByText("Slack is connected.")
    ).toBeTruthy()
  })

  it("tells a member who opened it that admins manage Slack", async () => {
    window.history.replaceState({}, "", `/?org=${ORG}&view=slack`)

    renderShell({ mode: "home", organizationRole: "member" })

    const settings = await screen.findByRole("dialog", { name: "Settings" })
    expect(
      await within(settings).findByText(
        "Organisation owners and admins manage Slack."
      )
    ).toBeTruthy()
  })
})
