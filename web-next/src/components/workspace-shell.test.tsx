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
import { json, mockApi, orgPath, teamspace, viewerId } from "@/test/teamspaces"
import { browserStorage } from "@/test/browser-storage"
import { rememberSettingsPage } from "@/lib/settings/page-choice"

// A signed-in shell holds the member's DM inbox open, and a teamspace its
// room: their sockets are stand-ins (src/test/room-socket.ts).
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

const launch = teamspace()
const design = teamspace({
  id: "tsp_design",
  team_id: "team_design",
  team_name: "Design",
  name: "Design reviews",
  status: "disabled",
  can_manage: false,
})

/** The agents, teams and files services, answering for `org_sim`'s member. */
function liveBackend() {
  const launchPath = `${orgPath}/teamspaces/tsp_launch`
  return mockApi(({ method, url }) => {
    const path = url.pathname
    if (path === `${orgPath}/teamspaces`) {
      return json({ teamspaces: [launch, design] })
    }
    if (path === `${orgPath}/teams`) return json({ teams: [] })
    if (path === launchPath) return json(launch)
    if (path === `${launchPath}/catchup`) {
      return json({ brief: { through_seq: 0 }, messages: [] })
    }
    if (path === `${launchPath}/messages` && method === "GET") {
      return json({
        messages: [],
        seats: [
          {
            handle: "member",
            kind: "person",
            user_id: viewerId,
            display_name: "Member",
            present: true,
          },
        ],
      })
    }
    if (path === `${launchPath}/entries`) return json({ entries: [] })
    if (path === `${orgPath}/files`) return json({ files: [] })
    return undefined
  })
}

function SignedInShell() {
  return (
    <WorkspaceShell
      session={session}
      role="member"
      organizationRole="member"
      selectedOrganization={session.organizations[0]}
      initialMode="team"
    />
  )
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
      if (url.pathname.endsWith("/teamspaces")) return json({ teamspaces: [] })
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
      screen.getByRole("heading", { name: "My workspace", level: 1 })
    ).toBeTruthy()
    expect(shell?.getAttribute("data-primary-work")).toBeNull()

    fireEvent.change(
      screen.getByRole("textbox", { name: "Start a new conversation" }),
      { target: { value: "  Start from Home  " } }
    )
    fireEvent.click(
      within(
        screen.getByRole("main", { name: "My workspace canvas" })
      ).getByRole("button", { name: "Send message" })
    )
    expect(request).toHaveBeenLastCalledWith(
      { organizationId: "org_sim", userId: viewerId },
      { kind: "new", message: "Start from Home" }
    )
    fireEvent.click(
      within(screen.getByRole("region", { name: "Conversations" })).getByRole(
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
      screen.getByRole("region", { name: "Conversations" })
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
      screen.getByRole("heading", { name: "My workspace", level: 1 })
    ).toBeTruthy()
    expect(shell?.getAttribute("data-primary-work")).toBeNull()
    expect(request).toHaveBeenCalledTimes(3)
    expect(api.calls.every((call) => call.method === "GET")).toBe(true)
  })

  it("starts Home on collapsed rails, preserves explicit toggle choices and has no fake saved views", () => {
    const { container } = render(<WorkspaceShell />)
    const shell = container.querySelector(".workspace-shell")
    expect(
      screen.getByRole("heading", { name: "My workspace", level: 1 })
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
    fireEvent.click(screen.getByRole("button", { name: "Teamspaces" }))
    fireEvent.click(screen.getByRole("button", { name: "My workspace" }))
    expect(shell?.getAttribute("data-left")).toBe("expanded")
    fireEvent.click(screen.getByRole("button", { name: "Expand agent panel" }))
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(shell?.getAttribute("data-right")).toBe("expanded")
    fireEvent.click(screen.getByRole("button", { name: "Library" }))
    fireEvent.click(screen.getByRole("button", { name: "My workspace" }))
    expect(shell?.getAttribute("data-right")).toBe("expanded")
    fireEvent.click(
      screen.getByRole("button", { name: "Collapse agent panel" })
    )
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(shell?.getAttribute("data-right")).toBe("collapsed")
  })

  it("uses the header title without repeating page introductions", () => {
    render(<WorkspaceShell />)

    fireEvent.click(screen.getByRole("button", { name: "Library" }))
    expect(
      screen.getByRole("heading", { name: "Library", level: 1 })
    ).toBeTruthy()
    expect(screen.queryByText("Knowledge your team can build on.")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    fireEvent.click(screen.getByRole("button", { name: "Teamspaces" }))
    expect(
      screen.getByRole("heading", { name: "Teamspaces", level: 1 })
    ).toBeTruthy()
    expect(screen.queryByText("Spaces where your team works.")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Tools" }))
    expect(
      screen.getByRole("heading", { name: "Tools", level: 1 })
    ).toBeTruthy()
    expect(screen.queryByText("Connected to your work.")).toBeNull()
  })

  it("keeps personal messages in Team and workspace invitations in Settings", () => {
    render(
      <WorkspaceShell preview initialMode="team" initialSection="people" />
    )

    expect(screen.getByRole("heading", { name: "People" })).toBeTruthy()
    expect(screen.queryByText("Work is better together.")).toBeNull()
    expect(screen.queryByText("Your people, together in one place.")).toBeNull()
    expect(screen.queryByText("Preview")).toBeNull()
    // Without a workspace there is nobody to message, and never examples.
    expect(
      screen.getByText("Sign in to a workspace to message its people.")
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: /^Message / })).toBeNull()
    expect(
      screen.queryByRole("heading", { name: "Make room for someone new." })
    ).toBeNull()

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
      screen.getByRole("heading", { name: "People", level: 1 })
    ).toBeTruthy()
  })

  it("lists every page in one sidebar, grouped as the board's tiles", () => {
    render(<WorkspaceShell />)

    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    const nav = screen.getByRole("navigation", { name: "Workspace" })
    for (const name of [
      "My workspace",
      "Requests",
      "Chats with your agent",
      "People",
      "Specialists",
      "Agent teams",
      "Workflows",
      "Knowledge",
      "Tools",
    ])
      expect(within(nav).getByRole("button", { name })).toBeTruthy()
    for (const group of ["Teamspaces", "Messages", "Agents", "Library"])
      expect(within(nav).getByRole("group", { name: group })).toBeTruthy()
    expect(
      screen.queryByRole("navigation", { name: "Home navigation" })
    ).toBeNull()

    const messages = within(nav).getByRole("group", { name: "Messages" })
    fireEvent.click(within(messages).getByRole("button", { name: "People" }))
    expect(screen.getByRole("heading", { name: "People" })).toBeTruthy()
    expect(
      within(messages)
        .getByRole("button", { name: "People" })
        .getAttribute("aria-current")
    ).toBe("page")

    fireEvent.click(within(nav).getByRole("button", { name: "Specialists" }))
    expect(screen.getByRole("heading", { name: "Specialists" })).toBeTruthy()
    expect(
      within(messages)
        .getByRole("button", { name: "People" })
        .getAttribute("aria-current")
    ).toBeNull()

    fireEvent.click(within(nav).getByRole("button", { name: "Agent teams" }))
    expect(
      screen.getByRole("heading", { name: "Multi-agent teams", level: 1 })
    ).toBeTruthy()

    fireEvent.click(within(nav).getByRole("button", { name: "My workspace" }))
    expect(screen.getByRole("heading", { name: "My workspace" })).toBeTruthy()
  })

  it("collapses to the board's rail: own pages, teamspaces, agent and Library, Settings", async () => {
    liveBackend()
    render(<SignedInShell />)

    fireEvent.click(
      screen.getByRole("button", { name: "Collapse left sidebar" })
    )
    const nav = screen.getByRole("navigation", { name: "Workspace" })
    const spaces = await within(nav).findByRole("group", {
      name: "Teamspaces",
    })
    await waitFor(() =>
      expect(
        within(spaces)
          .getAllByRole("button")
          .map((button) => button.textContent)
      ).toEqual(["Launch room", "Design reviews"])
    )
    for (const name of ["My workspace", "Chats with your agent", "Library"])
      expect(within(nav).getByRole("button", { name })).toBeTruthy()
    // No unread DMs: the rail shows no messages tile.
    expect(
      within(nav).queryByRole("group", { name: "Unread messages" })
    ).toBeNull()
    expect(screen.getByRole("button", { name: "Settings" })).toBeTruthy()

    fireEvent.click(within(spaces).getByRole("button", { name: "Launch room" }))
    expect(
      await screen.findByRole("heading", { name: "Launch room", level: 1 })
    ).toBeTruthy()
  })

  it("opens the member's chats with their agent in the panel's history", async () => {
    render(<WorkspaceShell />)

    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    fireEvent.click(
      screen.getByRole("button", { name: "Chats with your agent" })
    )
    expect(
      screen.getByRole("button", { name: "Collapse agent panel" })
    ).toBeTruthy()
    // Expanding the right panel collapses the left (DESIGN.md §2).
    expect(
      screen.getByRole("button", { name: "Expand left sidebar" })
    ).toBeTruthy()
  })

  // Agents mode's focused work is real data now: agents-mode.test.tsx.
  it("opens focused team work without the left sidebar and returns to the right section", async () => {
    liveBackend()
    render(<SignedInShell />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Manage Launch room" })
    )
    fireEvent.click(screen.getByRole("button", { name: "Open teamspace" }))
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    expect(
      screen.getByRole("heading", { name: "Launch room", level: 1 })
    ).toBeTruthy()
    expect(screen.getByText("Teamspace · Active")).toBeTruthy()
    const workPanel = screen.getByRole("region", {
      name: "Launch room work panel",
    })
    const workViews = screen.getByRole("group", { name: "Work views" })
    expect(workPanel.contains(workViews)).toBe(false)
    fireEvent.click(within(workViews).getByRole("button", { name: "Files" }))
    expect(
      within(workPanel).getByRole("heading", { name: "Team files" })
    ).toBeTruthy()
    expect(
      await within(workPanel).findByText(
        "No files have been shared with Launch yet."
      )
    ).toBeTruthy()
    expect(
      screen.getByRole("textbox", { name: "Message Launch room" })
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Back to teamspaces" }))
    expect(
      screen.getByRole("complementary", { name: "Workspace navigation" })
    ).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "Manage Launch room" })
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
    expect(screen.getByRole("tab", { name: "Agents" })).toBeTruthy()

    fireEvent.click(
      screen.getByRole("button", { name: "Collapse agent panel" })
    )
    expect(shell?.getAttribute("data-right")).toBe("collapsed")
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
  })

  it("keeps today's widgets a tab away, without example data", () => {
    render(<WorkspaceShell />)
    const panel = screen.getByRole("complementary", { name: "Agent panel" })
    fireEvent.click(screen.getByRole("button", { name: "Expand agent panel" }))

    const glanceTab = within(panel).getByRole("tab", { name: "At a glance" })
    fireEvent.click(glanceTab)
    expect(glanceTab.getAttribute("aria-selected")).toBe("true")
    const glanceView = within(panel).getByRole("tabpanel", {
      name: "At a glance",
    })
    // No workspace to read: one line saying what the tab holds, nothing more.
    expect(
      within(glanceView).getByText(
        "Your apps, teammates and conversations will appear here."
      )
    ).toBeTruthy()
    expect(within(glanceView).queryByRole("list")).toBeNull()
  })

  it("replaces the activity feed with agent and team tabs", () => {
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

    expect(
      within(panel)
        .getAllByRole("tab")
        .map((item) => item.getAttribute("aria-label"))
    ).toEqual(["Agents", "DMs", "Teams", "At a glance"])
    const agentsTab = within(panel).getByRole("tab", { name: "Agents" })
    expect(agentsTab.getAttribute("aria-selected")).toBe("true")
    const agentsView = within(panel).getByRole("tabpanel", {
      name: "Agents",
    })
    // One agent: nothing to switch to, so no switch row and no empty slots.
    expect(
      within(agentsView).queryByRole("group", { name: "Switch agent" })
    ).toBeNull()
    expect(
      within(agentsView).queryByRole("button", { name: "Empty agent slot" })
    ).toBeNull()
    expect(screen.queryByText("Primary agent")).toBeNull()
    expect(
      within(agentsView).getByText("Ask anything to start a conversation.")
    ).toBeTruthy()
    expect(
      within(agentsView)
        .getByRole("textbox", { name: "Message your agent" })
        .hasAttribute("disabled")
    ).toBe(true)
    expect(
      within(agentsView)
        .getByRole("button", { name: "Send message" })
        .hasAttribute("disabled")
    ).toBe(true)
    expect(
      within(agentsView).getByText("Chat isn't connected yet.")
    ).toBeTruthy()

    const expectSelected = (name: string, text: string) => {
      const selected = within(panel).getByRole("tab", { name })
      expect(selected.getAttribute("aria-selected")).toBe("true")
      expect(document.activeElement).toBe(selected)
      expect(within(panel).getAllByRole("tabpanel")).toHaveLength(1)
      expect(
        within(within(panel).getByRole("tabpanel", { name })).getByText(text)
      ).toBeTruthy()
      return selected
    }
    let tab = agentsTab
    for (const [key, name, text] of [
      ["ArrowRight", "DMs", "Direct messages will appear here."],
      ["ArrowRight", "Teams", "Team rooms will appear here."],
      [
        "ArrowRight",
        "At a glance",
        "Your apps, teammates and conversations will appear here.",
      ],
      ["ArrowRight", "Agents", "Ask anything to start a conversation."],
      [
        "ArrowLeft",
        "At a glance",
        "Your apps, teammates and conversations will appear here.",
      ],
      ["Home", "Agents", "Ask anything to start a conversation."],
      [
        "End",
        "At a glance",
        "Your apps, teammates and conversations will appear here.",
      ],
    ]) {
      fireEvent.keyDown(tab, { key })
      tab = expectSelected(name, text)
    }

    fireEvent.click(
      screen.getByRole("button", { name: "Collapse agent panel" })
    )
    expect(shell?.getAttribute("data-right")).toBe("collapsed")
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
  })

  it("opens the agent panel on the tab chosen from its rail", () => {
    const { container } = render(<WorkspaceShell />)
    const shell = container.querySelector(".workspace-shell")

    for (const [name, text] of [
      ["DMs", "Direct messages will appear here."],
      ["Teams", "Team rooms will appear here."],
      ["Agents", "Ask anything to start a conversation."],
    ]) {
      fireEvent.click(screen.getByRole("button", { name: `Open ${name}` }))
      expect(shell?.getAttribute("data-right")).toBe("expanded")
      expect(shell?.getAttribute("data-left")).toBe("collapsed")
      const tab = screen.getByRole("tab", { name })
      expect(tab.getAttribute("aria-selected")).toBe("true")
      expect(document.activeElement).toBe(tab)
      expect(
        within(screen.getByRole("tabpanel", { name })).getByText(text)
      ).toBeTruthy()

      fireEvent.click(
        screen.getByRole("button", { name: "Collapse agent panel" })
      )
      expect(screen.queryByRole("tab", { name })).toBeNull()
    }
  })

  it("names the selected teamspace in the header and counts the real ones", async () => {
    liveBackend()
    render(<SignedInShell />)

    // Team starts with the sidebar open.
    const listed = screen.getByRole("group", { name: "Teamspaces" })
    await waitFor(() =>
      expect(
        within(listed)
          .getAllByRole("listitem")
          .map((item) => item.textContent)
      ).toEqual(["Launch room", "Design reviews"])
    )
    fireEvent.click(within(listed).getByRole("button", { name: "Teamspaces" }))

    fireEvent.click(
      await screen.findByRole("button", { name: "Manage Design reviews" })
    )
    expect(
      screen.getByRole("heading", { name: "Design reviews", level: 1 })
    ).toBeTruthy()
    expect(screen.getByText("Teamspace · Paused")).toBeTruthy()
    expect(
      screen.queryByRole("heading", { name: "Teamspaces", level: 1 })
    ).toBeNull()
    expect(screen.queryByRole("button", { name: "Open teamspace" })).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "All teamspaces" }))
    expect(
      screen.getByRole("heading", { name: "Teamspaces", level: 1 })
    ).toBeTruthy()
  })

  it("shows no example teamspaces without a workspace to read", () => {
    const api = mockApi(() => undefined)
    render(<WorkspaceShell />)
    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    fireEvent.click(screen.getByRole("button", { name: "Teamspaces" }))

    expect(screen.getByText("No workspace to load")).toBeTruthy()
    for (const example of ["Product sync", "Launch planning", "Design reviews"])
      expect(screen.queryByText(example)).toBeNull()
    expect(api.calls).toEqual([])
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
    fireEvent.click(screen.getByRole("button", { name: "Library" }))

    fireEvent.click(
      await screen.findByRole("button", { name: "Open Product handbook" })
    )
    expect(
      screen.getByRole("heading", { name: "Product handbook", level: 1 })
    ).toBeTruthy()
    expect(screen.getByText("Knowledge base")).toBeTruthy()
    expect(
      screen.queryByRole("heading", { name: "Library", level: 1 })
    ).toBeNull()
    expect(await screen.findByText("Design principles")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "All knowledge bases" }))
    expect(
      screen.getByRole("heading", { name: "Library", level: 1 })
    ).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "Open Product handbook" })
    ).toBeTruthy()
  })

  it("shows no example knowledge bases without a workspace", () => {
    render(<WorkspaceShell />)
    fireEvent.click(screen.getByRole("button", { name: "Library" }))
    expect(
      screen.getByText(/Sign in to a workspace to see its knowledge bases/)
    ).toBeTruthy()
    const canvas = screen.getByRole("main", { name: "Library canvas" })
    expect(within(canvas).queryByRole("button", { name: /^Open / })).toBeNull()
    expect(
      within(canvas).queryByRole("button", { name: "New knowledge base" })
    ).toBeNull()
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

    fireEvent.click(
      await screen.findByRole("button", { name: "Manage Launch room" })
    )
    fireEvent.click(screen.getByRole("button", { name: "Open teamspace" }))

    const focus = container.querySelector<HTMLElement>(".workspace-focus")
    const input = screen.getByRole("textbox", { name: "Message Launch room" })
    fireEvent.focus(input)
    expect(focus?.getAttribute("data-composing")).toBe("true")
    expect(focus?.style.height).toBe("420px")

    act(() => {
      viewport.height = 300
      viewport.dispatchEvent(new Event("resize"))
    })
    expect(focus?.style.height).toBe("300px")

    fireEvent.blur(input)
    expect(focus?.getAttribute("data-composing")).toBe("false")
    expect(focus?.style.height).toBe("")
  })
})
