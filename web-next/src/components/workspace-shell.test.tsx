// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  act,
  cleanup,
  fireEvent,
  render as renderBase,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactElement } from "react"
import { WorkspaceShell } from "./workspace-shell"
import type { Session } from "@/lib/auth/client"
import * as primaryChat from "@/lib/threads/chat-choice"
import { json, mockApi, orgPath, viewerId } from "@/test/teamspaces"
import { browserStorage } from "@/test/browser-storage"
import { rememberSettingsPage } from "@/lib/settings/page-choice"

// A signed-in shell holds the primary agent's chat in its panel: its socket
// is a stand-in (src/test/room-socket.ts).
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)
vi.mock(
  "@cloudflare/ai-chat/react",
  async () => (await import("@/test/agent-chat")).aiChatReact
)

function render(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return renderBase(ui, {
    wrapper: ({ children }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    ),
  })
}

const session: Session = {
  user: { id: viewerId, email: "member@sim.oryxa", display_name: "Member" },
  organizations: [{ id: "org_sim", name: "Simulated Org", slug: "org-sim" }],
}

const leo = {
  id: "agt_leo",
  organization_id: "org_sim",
  owner_user_id: viewerId,
  kind: "primary",
  name: "Leo",
  description: "Your primary Oryxa agent.",
  instructions: "",
  status: "active",
  revision: 1,
  created_at: "2026-09-20T10:00:00Z",
  updated_at: "2026-09-20T10:00:00Z",
}

/** The agents, teams and files services, answering for `org_sim`'s member. */
function liveBackend() {
  return mockApi(({ url }) => {
    const path = url.pathname
    if (path === `${orgPath}/agents`) return json({ agents: [leo] })
    if (path === `${orgPath}/threads`) return json({ threads: [] })
    if (path.endsWith("/get-messages")) return json([])
    if (path === `${orgPath}/teams`) return json({ teams: [] })
    if (path === `${orgPath}/files`) return json({ files: [] })
    return undefined
  })
}

beforeEach(() => {
  vi.stubGlobal("localStorage", browserStorage())
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
})

afterEach(() => {
  cleanup()
  window.history.replaceState({}, "", "/")
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("WorkspaceShell navigation", () => {
  it.each(["billing", "slack"] as const)(
    "keeps the explicit %s return ahead of the remembered section",
    (page) => {
      liveBackend()
      rememberSettingsPage(
        {
          organizationId: session.organizations[0].id,
          userId: session.user.id,
        },
        "email"
      )
      window.history.replaceState({}, "", `/?view=${page}`)
      render(
        <WorkspaceShell
          session={session}
          selectedOrganization={session.organizations[0]}
          organizationRole="admin"
        />
      )
      expect(
        screen.getByRole("heading", {
          name: page === "billing" ? "Billing" : "Slack",
          level: 2,
        })
      ).toBeTruthy()
      fireEvent.click(screen.getByRole("button", { name: "Tool access" }))
      fireEvent.click(screen.getByRole("button", { name: "Open my tools →" }))
      expect(screen.queryByRole("dialog", { name: "Settings" })).toBeNull()
      fireEvent.click(screen.getByRole("button", { name: "Open user menu" }))
      fireEvent.click(screen.getByRole("menuitem", { name: "Settings" }))
      expect(
        screen.getByRole("heading", { name: "Tool access", level: 2 })
      ).toBeTruthy()
    }
  )

  it("restores Settings for the same member and workspace without carrying a section to another scope", () => {
    liveBackend()
    const props = {
      session,
      selectedOrganization: session.organizations[0],
      role: "member" as const,
      organizationRole: "member" as const,
    }
    const view = render(<WorkspaceShell {...props} />)
    function openSettings() {
      fireEvent.click(screen.getByRole("button", { name: "Open user menu" }))
      fireEvent.click(screen.getByRole("menuitem", { name: "Settings" }))
    }
    openSettings()
    fireEvent.click(screen.getByRole("button", { name: "Teams" }))
    fireEvent.click(screen.getByRole("button", { name: "Close settings" }))
    openSettings()
    expect(
      screen.getByRole("heading", { name: "Teams", level: 2 })
    ).toBeTruthy()

    view.rerender(
      <WorkspaceShell
        {...props}
        selectedOrganization={{ id: "org_other", name: "Other", slug: "other" }}
      />
    )
    expect(
      screen.getByRole("heading", { name: "Account", level: 2 })
    ).toBeTruthy()
    view.rerender(<WorkspaceShell {...props} />)
    expect(
      screen.getByRole("heading", { name: "Teams", level: 2 })
    ).toBeTruthy()

    view.rerender(
      <WorkspaceShell
        {...props}
        session={{
          ...session,
          user: { id: "user-other-settings", email: "other@example.com" },
        }}
      />
    )
    openSettings()
    expect(
      screen.getByRole("heading", { name: "Account", level: 2 })
    ).toBeTruthy()
  })

  it("preserves the same member's workspace state on refresh and resets the whole shell for another member", async () => {
    liveBackend()
    const props = {
      session,
      selectedOrganization: session.organizations[0],
      role: "member" as const,
      organizationRole: "member" as const,
    }
    const view = render(<WorkspaceShell {...props} />)
    fireEvent.change(
      await screen.findByRole("textbox", {
        name: "Start a new conversation",
      }),
      { target: { value: "Member's unfinished Home message" } }
    )
    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    fireEvent.click(screen.getByRole("button", { name: "Open user menu" }))
    fireEvent.click(screen.getByRole("menuitem", { name: "Settings" }))
    expect(screen.getByRole("dialog", { name: "Settings" })).toBeTruthy()

    view.rerender(
      <WorkspaceShell
        {...props}
        session={{
          ...session,
          user: { ...session.user, display_name: "Member refreshed" },
        }}
      />
    )
    expect(screen.getByRole("dialog", { name: "Settings" })).toBeTruthy()
    expect(
      view.container
        .querySelector(".workspace-shell")
        ?.getAttribute("data-left")
    ).toBe("expanded")
    expect(
      screen.getByDisplayValue("Member's unfinished Home message")
    ).toBeTruthy()

    view.rerender(
      <WorkspaceShell
        {...props}
        session={{
          ...session,
          user: {
            id: "usr_other_shell",
            email: "other@sim.oryxa",
            display_name: "Other member",
          },
        }}
      />
    )
    expect(screen.queryByRole("dialog", { name: "Settings" })).toBeNull()
    const shell = view.container.querySelector(".workspace-shell")
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(shell?.getAttribute("data-right")).toBe("collapsed")
    expect(
      screen.getByRole<HTMLTextAreaElement>("textbox", {
        name: "Start a new conversation",
      }).value
    ).toBe("")
  })

  it("clears Home's private composer draft when the member changes in the same workspace", async () => {
    liveBackend()
    const props = {
      session,
      selectedOrganization: session.organizations[0],
      role: "member" as const,
      organizationRole: "member" as const,
    }
    const view = render(<WorkspaceShell {...props} />)
    const composer = await screen.findByRole<HTMLTextAreaElement>("textbox", {
      name: "Start a new conversation",
    })
    fireEvent.change(composer, { target: { value: "Member's private draft" } })
    expect(composer.value).toBe("Member's private draft")

    view.rerender(
      <WorkspaceShell
        {...props}
        session={{
          ...session,
          user: {
            id: "usr_other_home",
            email: "other@sim.oryxa",
            display_name: "Other member",
          },
        }}
      />
    )
    expect(
      screen.getByRole<HTMLTextAreaElement>("textbox", {
        name: "Start a new conversation",
      }).value
    ).toBe("")
  })

  it("opens the panel and emits workspace-scoped Home targets through the public contract without claiming delivery", async () => {
    const request = vi
      .spyOn(primaryChat, "requestPrimaryChat")
      .mockImplementation(() => {})
    const api = mockApi(({ url }) => {
      if (url.pathname === `${orgPath}/threads`)
        return json({
          threads: [
            {
              id: "th_recent",
              title: "Specific recent conversation",
              origin: "chat",
              updated_at: new Date().toISOString(),
            },
          ],
        })
      if (url.pathname.endsWith("/threads/th_recent"))
        return json({
          id: "th_recent",
          title: "Specific recent conversation",
          items: [],
          pending_confirmations: [],
        })
      if (url.pathname.endsWith("/get-messages")) return json([])
      if (url.pathname.endsWith("/agents")) return json({ agents: [] })
      if (url.pathname.endsWith("/corpora")) return json({ corpora: [] })
      if (url.pathname.endsWith("/apps/connections"))
        return json({ connections: [] })
      if (url.pathname.endsWith("/agent-requests/inbox"))
        return json({ requests: [] })
      return undefined
    })
    const { container } = render(
      <WorkspaceShell
        session={session}
        selectedOrganization={session.organizations[0]}
        role="member"
        organizationRole="member"
      />
    )
    const shell = container.querySelector(".workspace-shell")
    fireEvent.click(
      await screen.findByRole("button", {
        name: /Specific recent conversation/,
      })
    )
    expect(request).toHaveBeenLastCalledWith(
      { organizationId: "org_sim", userId: viewerId },
      { kind: "thread", threadId: "th_recent" }
    )
    expect(shell?.getAttribute("data-right")).toBe("expanded")
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(
      screen.getByRole("heading", { name: "Today", level: 1 })
    ).toBeTruthy()
    expect(shell?.getAttribute("data-primary-work")).toBeNull()

    fireEvent.change(
      screen.getByRole("textbox", { name: "Start a new conversation" }),
      { target: { value: "  Start from Home  " } }
    )
    fireEvent.click(
      within(screen.getByRole("main", { name: "Today canvas" })).getByRole(
        "button",
        { name: "Send message" }
      )
    )
    expect(request).toHaveBeenLastCalledWith(
      { organizationId: "org_sim", userId: viewerId },
      { kind: "new", message: "Start from Home" }
    )
    fireEvent.click(
      within(screen.getByRole("region", { name: "Your agent" })).getByRole(
        "button",
        { name: "New chat" }
      )
    )
    expect(request).toHaveBeenLastCalledWith(
      { organizationId: "org_sim", userId: viewerId },
      { kind: "new", message: undefined }
    )
    expect(request).toHaveBeenCalledTimes(3)
    fireEvent.change(
      screen.getByRole("textbox", { name: "Start a new conversation" }),
      { target: { value: "Still composing on Home" } }
    )
    const all = within(
      screen.getByRole("region", { name: "Your agent" })
    ).getByRole("button", { name: "All conversations" })
    fireEvent.click(all)
    const history = await screen.findByRole("button", { name: "History" })
    await waitFor(() =>
      expect(history.getAttribute("aria-expanded")).toBe("true")
    )
    fireEvent.click(history)
    expect(history.getAttribute("aria-expanded")).toBe("false")
    fireEvent.click(
      screen.getByRole("button", { name: "Collapse agent panel" })
    )
    fireEvent.click(all)
    await waitFor(() =>
      expect(history.getAttribute("aria-expanded")).toBe("true")
    )
    expect(screen.getByDisplayValue("Still composing on Home")).toBeTruthy()
    expect(
      screen.getByRole("heading", { name: "Today", level: 1 })
    ).toBeTruthy()
    expect(shell?.getAttribute("data-primary-work")).toBeNull()
    expect(request).toHaveBeenCalledTimes(3)
    expect(api.calls.every((call) => call.method === "GET")).toBe(true)
  })

  it("starts Home on collapsed rails, preserves explicit toggle choices and has no fake saved views", () => {
    const { container } = render(<WorkspaceShell />)
    const shell = container.querySelector(".workspace-shell")
    expect(
      screen.getByRole("heading", { name: "Today", level: 1 })
    ).toBeTruthy()
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(shell?.getAttribute("data-right")).toBe("collapsed")
    for (const label of [
      "Saved views",
      "Launch plan",
      "At-risk accounts",
      "Q4 pipeline",
    ])
      expect(screen.queryByText(label)).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    fireEvent.click(screen.getByRole("button", { name: /^Agents$/ }))
    fireEvent.click(screen.getByRole("button", { name: /^Home$/ }))
    expect(shell?.getAttribute("data-left")).toBe("expanded")
    fireEvent.click(screen.getByRole("button", { name: "Expand agent panel" }))
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(shell?.getAttribute("data-right")).toBe("expanded")
    fireEvent.click(screen.getByRole("button", { name: "Knowledge bases" }))
    fireEvent.click(screen.getByRole("button", { name: "Today" }))
    expect(shell?.getAttribute("data-right")).toBe("expanded")
    fireEvent.click(
      screen.getByRole("button", { name: "Collapse agent panel" })
    )
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(shell?.getAttribute("data-right")).toBe("collapsed")
  })

  it("uses the header title without repeating page introductions", () => {
    render(<WorkspaceShell />)

    fireEvent.click(screen.getByRole("button", { name: "Knowledge bases" }))
    expect(
      screen.getByRole("heading", { name: "Knowledge bases", level: 1 })
    ).toBeTruthy()
    expect(screen.queryByText("Knowledge your team can build on.")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Tools" }))
    expect(
      screen.getByRole("heading", { name: "Tools", level: 1 })
    ).toBeTruthy()
    expect(screen.queryByText("Connected to your work.")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    fireEvent.click(screen.getByRole("button", { name: /^Agents$/ }))
    expect(
      screen.getByRole("heading", { name: "Agent overview", level: 1 })
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Requests" }))
    expect(
      screen.getByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()
  })

  it("keeps workspace invitations and people in Settings", () => {
    render(<WorkspaceShell preview />)

    // There is no Team area: people are reached from Settings alone.
    expect(screen.queryByRole("button", { name: /^Team$/ })).toBeNull()
    expect(screen.queryByRole("heading", { name: "People" })).toBeNull()
    expect(screen.queryByText("Preview")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Open user menu" }))
    fireEvent.click(screen.getByRole("menuitem", { name: "Settings" }))
    expect(screen.getByRole("dialog", { name: "Settings" })).toBeTruthy()
    expect(
      screen.getByRole("heading", { name: "Account", level: 2 })
    ).toBeTruthy()
    fireEvent.click(
      screen.getByRole("button", { name: "People & invitations" })
    )
    expect(
      screen.getByRole("heading", { name: "Make room for someone new." })
    ).toBeTruthy()
    expect(
      screen
        .getByRole("button", { name: "Send invite" })
        .hasAttribute("disabled")
    ).toBe(true)

    fireEvent.click(screen.getByRole("button", { name: "Account" }))
    expect(screen.getByRole("heading", { name: "Account" })).toBeTruthy()
    expect(
      screen
        .getByRole("button", { name: "Send reset link" })
        .hasAttribute("disabled")
    ).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "Organisation" }))
    expect(screen.getByRole("heading", { name: "Organisation" })).toBeTruthy()
    expect(
      screen.getByRole("textbox", { name: "Name" }).hasAttribute("disabled")
    ).toBe(true)
    expect(
      screen.getByRole("textbox", { name: "Handle" }).hasAttribute("disabled")
    ).toBe(true)
    for (const section of ["Teams", "Tool access", "Email", "Billing"]) {
      fireEvent.click(screen.getByRole("button", { name: section }))
      expect(
        screen.getByRole("heading", { name: section, level: 2 })
      ).toBeTruthy()
    }
    fireEvent.click(screen.getByRole("button", { name: "Close settings" }))
    expect(screen.queryByRole("dialog", { name: "Settings" })).toBeNull()
    expect(
      screen.getByRole("heading", { name: "Today", level: 1 })
    ).toBeTruthy()
  })

  it("offers Home and Agents as the main areas, and in each only what remains", () => {
    render(<WorkspaceShell />)
    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))

    const areas = screen.getByRole("group", { name: "Main areas" })
    expect(
      within(areas)
        .getAllByRole("button")
        .map((button) => button.getAttribute("aria-label"))
    ).toEqual(["Home", "Agents"])
    const labels = (name: string) =>
      within(screen.getByRole("navigation", { name }))
        .getAllByRole("button")
        .map((button) => button.textContent)
    expect(labels("Home navigation")).toEqual([
      "Today",
      "Knowledge bases",
      "Tools",
    ])

    fireEvent.click(within(areas).getByRole("button", { name: "Agents" }))
    expect(labels("Agents navigation")).toEqual(["Agent overview", "Requests"])

    // What was archived is reachable from nowhere.
    for (const archived of [
      "Team",
      "Teamspaces",
      "People",
      "Multi-agent teams",
      "Specialists",
      "Workflows",
    ]) {
      expect(screen.queryByRole("button", { name: archived })).toBeNull()
    }
    expect(screen.queryByText("Recent conversations")).toBeNull()
  })

  it("shows a different sidebar for each mode and remembers its selection", () => {
    render(<WorkspaceShell />)

    const homeNavigation = screen.getByRole("navigation", {
      name: "Home navigation",
    })
    expect(
      within(homeNavigation).getByRole("button", { name: "Today" })
    ).toBeTruthy()
    expect(
      within(homeNavigation).getByRole("button", { name: "Knowledge bases" })
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Agent overview" })).toBeNull()
    fireEvent.click(
      within(homeNavigation).getByRole("button", { name: "Knowledge bases" })
    )
    expect(
      screen.getByRole("heading", { name: "Knowledge bases", level: 1 })
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    fireEvent.click(screen.getByRole("button", { name: /^Agents$/ }))
    const agentsNavigation = screen.getByRole("navigation", {
      name: "Agents navigation",
    })
    expect(
      within(agentsNavigation).queryByRole("button", { name: "Today" })
    ).toBeNull()
    fireEvent.click(
      within(agentsNavigation).getByRole("button", { name: "Requests" })
    )
    expect(
      screen.getByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: /^Home$/ }))
    expect(
      screen.getByRole("heading", { name: "Knowledge bases", level: 1 })
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /^Agents$/ }))
    expect(
      screen.getByRole("heading", { name: "Requests", level: 1 })
    ).toBeTruthy()
  })

  it("keeps mobile panels closed until their header controls are used", () => {
    vi.stubGlobal("matchMedia", () => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
    const { container } = render(<WorkspaceShell />)
    const shell = container.querySelector(".workspace-shell")

    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(shell?.getAttribute("data-right")).toBe("collapsed")

    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    expect(shell?.getAttribute("data-left")).toBe("expanded")

    fireEvent.click(screen.getByRole("button", { name: "Expand agent panel" }))
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(shell?.getAttribute("data-right")).toBe("expanded")
    expect(
      screen.getByRole("region", { name: "Agent chat" }).hasAttribute("hidden")
    ).toBe(false)

    fireEvent.click(
      screen.getByRole("button", { name: "Collapse agent panel" })
    )
    expect(shell?.getAttribute("data-right")).toBe("collapsed")
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
  })

  it("replaces the activity feed with the primary agent's chat alone", () => {
    const { container } = render(<WorkspaceShell />)
    const shell = container.querySelector(".workspace-shell")
    const panel = screen.getByRole("complementary", { name: "Agent panel" })

    for (const text of ["Activity", "Team online", "5 online"])
      expect(within(panel).queryByText(text)).toBeNull()
    expect(within(panel).queryByText(/launch timeline/)).toBeNull()
    expect(within(panel).queryByRole("tablist")).toBeNull()

    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    fireEvent.click(screen.getByRole("button", { name: "Expand agent panel" }))
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(shell?.getAttribute("data-right")).toBe("expanded")

    // No tabs, no DMs, no Teams: the one agent's chat.
    expect(within(panel).queryByRole("tab")).toBeNull()
    for (const archived of ["DMs", "Teams", "Direct messages"])
      expect(within(panel).queryByText(archived)).toBeNull()
    const chat = within(panel).getByRole("region", { name: "Agent chat" })
    // One agent: nothing to switch to, so no switch row and no empty slots.
    expect(
      within(chat).queryByRole("group", { name: "Switch agent" })
    ).toBeNull()
    expect(
      within(chat).queryByRole("button", { name: "Empty agent slot" })
    ).toBeNull()
    expect(screen.queryByText("Primary agent")).toBeNull()
    expect(
      within(chat).getByText("Ask anything to start a conversation.")
    ).toBeTruthy()
    expect(
      within(chat)
        .getByRole("textbox", { name: "Message your agent" })
        .hasAttribute("disabled")
    ).toBe(true)
    expect(
      within(chat)
        .getByRole("button", { name: "Send message" })
        .hasAttribute("disabled")
    ).toBe(true)
    expect(within(chat).getByText("Chat isn't connected yet.")).toBeTruthy()

    fireEvent.click(
      screen.getByRole("button", { name: "Collapse agent panel" })
    )
    expect(shell?.getAttribute("data-right")).toBe("collapsed")
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
  })

  it("opens the agent panel from its rail", () => {
    const { container } = render(<WorkspaceShell />)
    const shell = container.querySelector(".workspace-shell")
    const panel = screen.getByRole("complementary", { name: "Agent panel" })

    // One button, for the one agent: not a button per archived area.
    expect(within(panel).getAllByRole("button")).toHaveLength(2)
    fireEvent.click(screen.getByRole("button", { name: "Open agent chat" }))

    expect(shell?.getAttribute("data-right")).toBe("expanded")
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Collapse agent panel" })
    )
    expect(
      within(screen.getByRole("region", { name: "Agent chat" })).getByText(
        "Ask anything to start a conversation."
      )
    ).toBeTruthy()

    fireEvent.click(
      screen.getByRole("button", { name: "Collapse agent panel" })
    )
    expect(
      screen.queryByRole("button", { name: "Open agent chat" })
    ).toBeTruthy()
  })

  it("opens a real knowledge base and names it in the header", async () => {
    const organization = { id: "org_1", name: "Acme", slug: "acme" }
    const handbook = {
      id: "cor_1",
      organization_id: "org_1",
      slug: "product-handbook",
      name: "Product handbook",
      scope: "member",
      member_id: "user-1",
      embedding: {
        provider: "vertex",
        model: "text-embedding-005",
        dimensions: 768,
        metric: "cosine",
      },
      backend: "pgvector",
      chunking: { strategy: "structure", max_tokens: 512, overlap_tokens: 76 },
      metadata: { description: "Principles, specs and decisions." },
      document_count: 1,
      chunk_count: 2,
      created_by: "user-1",
      created_at: "2026-09-20T10:00:00Z",
      updated_at: "2026-09-20T10:00:00Z",
    }
    const routes: Record<string, unknown> = {
      "/v1/capabilities": { answering: true, content_types: [] },
      "/v1/organizations/org_1/corpora": { corpora: [handbook] },
      "/v1/organizations/org_1/corpora/cor_1": handbook,
      "/v1/organizations/org_1/corpora/cor_1/documents": {
        documents: [
          {
            id: "doc_1",
            corpus_id: "cor_1",
            organization_id: "org_1",
            source: { kind: "inline" },
            title: "Design principles",
            content_type: "text/plain",
            content_sha256: "",
            byte_size: 512,
            status: "ready",
            chunk_count: 2,
            attempts: 1,
            created_by: "user-1",
            created_at: "2026-09-20T10:00:00Z",
            updated_at: "2026-09-20T10:00:00Z",
          },
        ],
      },
    }
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input) => {
        const path = new URL(String(input), "http://localhost").pathname
        return path in routes
          ? json(routes[path])
          : new Response(null, { status: 404 })
      })
    )
    render(
      <WorkspaceShell
        session={{
          user: { id: "user-1", email: "avery@example.com" },
          organizations: [organization],
        }}
        selectedOrganization={organization}
        organizationRole="member"
      />
    )
    fireEvent.click(screen.getByRole("button", { name: "Knowledge bases" }))

    fireEvent.click(
      await screen.findByRole("button", { name: "Open Product handbook" })
    )
    expect(
      screen.getByRole("heading", { name: "Product handbook", level: 1 })
    ).toBeTruthy()
    expect(screen.getByText("Knowledge base")).toBeTruthy()
    expect(
      screen.queryByRole("heading", { name: "Knowledge bases", level: 1 })
    ).toBeNull()
    expect(await screen.findByText("Design principles")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "All knowledge bases" }))
    expect(
      screen.getByRole("heading", { name: "Knowledge bases", level: 1 })
    ).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "Open Product handbook" })
    ).toBeTruthy()
  })

  it("shows no example knowledge bases without a workspace", () => {
    render(<WorkspaceShell />)
    fireEvent.click(screen.getByRole("button", { name: "Knowledge bases" }))
    expect(
      screen.getByText(/Sign in to a workspace to see its knowledge bases/)
    ).toBeTruthy()
    const canvas = screen.getByRole("main", { name: "Knowledge bases canvas" })
    expect(within(canvas).queryByRole("button", { name: /^Open / })).toBeNull()
    expect(
      within(canvas).queryByRole("button", { name: "New knowledge base" })
    ).toBeNull()
  })
})

describe("the focused Agent overview", () => {
  function SignedInShell() {
    return (
      <WorkspaceShell
        session={session}
        role="member"
        organizationRole="member"
        selectedOrganization={session.organizations[0]}
      />
    )
  }

  async function openOverview() {
    fireEvent.click(screen.getByRole("button", { name: "Expand agent panel" }))
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Agent overview" })
    )
    return screen.findByRole("region", { name: "Agent overview work panel" })
  }

  it("takes the left sidebar away, keeps the same chat beside the overview, and goes back", async () => {
    liveBackend()
    const { container } = render(<SignedInShell />)
    const shell = container.querySelector(".workspace-shell")

    const workPanel = await openOverview()
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    expect(shell?.getAttribute("data-view")).toBe("focused")
    expect(shell?.getAttribute("data-primary-work")).toBe("true")
    expect(
      screen.getByRole("heading", { name: "Agent overview", level: 1 })
    ).toBeTruthy()
    expect(
      await within(workPanel).findByRole("region", { name: "Primary agent" })
    ).toBeTruthy()

    // The panel is the conversation column now, and offers nothing else: the
    // toggle and the rail are for the collapsed panel only.
    const panel = screen.getByRole("complementary", { name: "Agent panel" })
    expect(
      within(panel)
        .getByRole("region", { name: "Agent chat" })
        .getAttribute("data-primary-chat")
    ).toBe("true")
    expect(
      within(panel).queryByRole("button", { name: /agent panel$/ })
    ).toBeNull()
    expect(
      within(panel).queryByRole("button", { name: "Open agent chat" })
    ).toBeNull()
    expect(within(panel).queryByRole("tab")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Back to workspace" }))
    expect(
      screen.getByRole("complementary", { name: "Workspace navigation" })
    ).toBeTruthy()
    expect(shell?.getAttribute("data-view")).toBe("browse")
    expect(shell?.getAttribute("data-primary-work")).toBeNull()
    // Back is the overview in Agents, where it was opened from.
    expect(
      screen.getByRole("heading", { name: "Agent overview", level: 1 })
    ).toBeTruthy()
    expect(
      screen.getByRole("navigation", { name: "Agents navigation" })
    ).toBeTruthy()
  })

  it("uses the visible mobile viewport for chat while composing", async () => {
    vi.stubGlobal("matchMedia", () => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
    const viewport = Object.assign(new EventTarget(), { height: 420 })
    vi.stubGlobal("visualViewport", viewport)
    liveBackend()
    const { container } = render(<SignedInShell />)
    const shell = container.querySelector<HTMLElement>(".workspace-shell")

    await openOverview()
    const input = await screen.findByRole("textbox", { name: "Message Leo" })
    fireEvent.focus(input)
    expect(shell?.getAttribute("data-primary-composing")).toBe("true")
    expect(shell?.style.getPropertyValue("--primary-visible-height")).toBe(
      "420px"
    )

    act(() => {
      viewport.height = 300
      viewport.dispatchEvent(new Event("resize"))
    })
    expect(shell?.style.getPropertyValue("--primary-visible-height")).toBe(
      "300px"
    )

    fireEvent.blur(input)
    expect(shell?.getAttribute("data-primary-composing")).toBeNull()
    expect(shell?.style.getPropertyValue("--primary-visible-height")).toBe("")
  })
})
