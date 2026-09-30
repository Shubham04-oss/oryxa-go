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
import type { WorkspaceArea } from "./workspace-shell"
import { openRequest, openRequests } from "./requests-view-open"
import {
  ORG,
  VIEWER,
  fakeAgentsBackend,
  primary,
  request,
} from "./agents-fake-backend"
import type { Role, Session } from "@/lib/auth/client"

// The shell hosts the primary agent's chat in its center; the Agents SDK
// stands in (src/test/room-socket.ts, src/test/agent-chat.ts).
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

/** A conversation of the member's, to leave for Requests. */
const conversation = {
  id: "th_plan",
  title: "Plan the launch",
  origin: "chat",
  updated_at: new Date(Date.now() - 3 * 60 * 60_000).toISOString(),
}

function renderShell(
  initial: {
    area?: WorkspaceArea
    signedIn?: boolean
    organizationRole?: Role
  } = {}
) {
  const backend = fakeAgentsBackend({
    agents: [primary],
    inbox: [waiting],
  })
  const answer = (body: unknown) =>
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })
  // The one request a link names, the member's conversation and the members
  // list, beside the backend.
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const path = new URL(input, "http://localhost").pathname
      if (path === `/v1/organizations/${ORG}/agent-requests/activity`) {
        return answer({
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
        })
      }
      if (path === `/v1/organizations/${ORG}/agent-requests/${shared.id}`) {
        return answer(shared)
      }
      if (path === `/v1/organizations/${ORG}/agent-requests/irq_shell`) {
        return answer(waiting)
      }
      if (path === `/v1/organizations/${ORG}/agent-requests/irq_link`) {
        return answer(
          request({ id: "irq_link", status: "pending", task: "A linked ask." })
        )
      }
      if (path === `/v1/organizations/${ORG}/threads`) {
        return answer({ threads: [conversation] })
      }
      if (path === `/v1/organizations/${ORG}/threads/${conversation.id}`) {
        return answer({ ...conversation, items: [], pending_confirmations: [] })
      }
      if (path.endsWith("/get-messages")) return answer([])
      if (path === `/v1/organizations/${ORG}/apps/slack/install`) {
        return answer({
          status: "not_installed",
          add_to_slack_url: "https://slack.com/oauth/v2/authorize?state=abc",
          add_to_slack_expires_at: "2999-01-01T00:00:00Z",
        })
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
        initialArea={initial.area}
      />
    </QueryClientProvider>
  )
}

const main = () => within(screen.getByRole("navigation", { name: "Main" }))

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
  window.sessionStorage.clear()
  vi.unstubAllGlobals()
})

describe("Requests in the workspace shell", () => {
  it.each(["All", "row"])(
    "opens the desk's %s shortcut in Requests, selecting only a named shared-activity row",
    async (target) => {
      renderShell()
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

  it("is the sidebar's first destination, with the Inbox count, and opens the page", async () => {
    renderShell()

    expect(
      main()
        .getAllByRole("button")
        .map((button) => button.textContent.replace(/\d+$/, ""))
    ).toEqual(["Requests", "Knowledge", "Tools"])
    const requests = await main().findByRole("button", {
      name: "Requests, 1 waiting",
    })
    fireEvent.click(requests)

    expect(
      screen.getByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()
    expect(
      await screen.findByRole("tab", { name: "Inbox, 1 waiting" })
    ).toBeTruthy()
    await screen.findByText("Share the pipeline total.")
    expect(requests.getAttribute("aria-current")).toBe("page")
  })

  it("opens on a request when a note or a link names one, from wherever the member is", async () => {
    renderShell()
    await screen.findByRole("heading", { name: "Today", level: 1 })

    act(() => openRequest("irq_link"))

    expect(
      await screen.findByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()
    const opened = await screen.findByRole("region", { name: "Opened request" })
    await within(opened).findByText("A linked ask.")
    expect(
      main()
        .getByRole("button", { name: /^Requests/ })
        .getAttribute("aria-current")
    ).toBe("page")
    // The address names the request, so a reload opens it again.
    expect(new URLSearchParams(window.location.search).get("request")).toBe(
      "irq_link"
    )
  })

  it("opens the request an address names, as the page opens", async () => {
    window.history.replaceState(
      {},
      "",
      `/?org=${ORG}&view=requests&request=irq_link`
    )

    renderShell()

    const opened = await screen.findByRole("region", { name: "Opened request" })
    await within(opened).findByText("A linked ask.")
    expect(
      screen.getByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()
    expect(window.location.search).toBe(
      `?org=${ORG}&view=requests&request=irq_link`
    )

    // Back to all requests: the address forgets the request.
    fireEvent.click(
      within(opened).getByRole("button", { name: "Back to all requests" })
    )
    expect(window.location.search).toBe(`?org=${ORG}&view=requests`)
  })

  it("puts a named request away once the member goes elsewhere", async () => {
    renderShell({ area: "requests" })
    await screen.findByRole("tab", { name: /Inbox/ })
    act(() => openRequest("irq_link"))
    await screen.findByRole("region", { name: "Opened request" })

    fireEvent.click(main().getByRole("button", { name: "Knowledge" }))
    expect(
      screen.getByRole("heading", { name: "Knowledge bases", level: 1 })
    ).toBeTruthy()
    fireEvent.click(main().getByRole("button", { name: /^Requests/ }))

    await screen.findByRole("tab", { name: /Inbox/ })
    expect(screen.queryByRole("region", { name: "Opened request" })).toBeNull()
  })

  it("opens Requests with no request when none is named", async () => {
    renderShell()
    await screen.findByRole("heading", { name: "Today", level: 1 })

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
    "leaves an open conversation for Requests when one is opened on $viewport (linked: $linked)",
    async ({ mobile, linked }) => {
      vi.stubGlobal("matchMedia", () => ({
        matches: mobile,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }))
      renderShell()
      await screen.findByRole("heading", { name: "Today", level: 1 })
      if (mobile)
        fireEvent.click(screen.getByRole("button", { name: "Open navigation" }))
      fireEvent.click(
        await screen.findByRole("button", { name: /^Plan the launch/ })
      )
      await waitFor(() =>
        expect(screen.getByRole("heading", { level: 1 }).textContent).toBe(
          "Plan the launch"
        )
      )
      expect(screen.getByRole("region", { name: "Agent chat" })).toBeTruthy()

      act(() => (linked ? openRequest("irq_link") : openRequests()))

      expect(
        await screen.findByRole("heading", { name: "Requests", level: 1 })
      ).toBeTruthy()
      expect(screen.queryByRole("region", { name: "Agent chat" })).toBeNull()
      if (mobile) {
        expect(
          screen.queryByRole("complementary", { name: "Workspace navigation" })
        ).toBeNull()
        fireEvent.click(screen.getByRole("button", { name: "Open navigation" }))
      }
      expect(
        main()
          .getByRole("button", { name: /^Requests/ })
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
    renderShell({ signedIn: false, area: "requests" })

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

    renderShell({ organizationRole: "admin" })

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

    renderShell({ organizationRole: "member" })

    const settings = await screen.findByRole("dialog", { name: "Settings" })
    expect(
      await within(settings).findByText(
        "Organisation owners and admins manage Slack."
      )
    ).toBeTruthy()
  })
})
