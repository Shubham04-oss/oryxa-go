// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  act,
  cleanup,
  fireEvent,
  render as renderBase,
  screen,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { useState } from "react"
import type { ReactElement } from "react"
import { WorkspaceShell } from "./workspace-shell"
import {
  initialTeamspaces,
  TeamspacesManagement,
} from "./teamspaces-management"

function render(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return renderBase(
    <QueryClientProvider client={client}>{ui}</QueryClientProvider>
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
  vi.unstubAllGlobals()
})

describe("WorkspaceShell navigation", () => {
  it("uses the header title without repeating page introductions", () => {
    render(<WorkspaceShell />)

    fireEvent.click(screen.getByRole("button", { name: "Knowledge bases" }))
    expect(
      screen.getByRole("heading", { name: "Knowledge bases", level: 1 })
    ).toBeTruthy()
    expect(screen.queryByText("Knowledge your team can build on.")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: /^Team$/ }))
    expect(
      screen.getByRole("heading", { name: "Teamspaces", level: 1 })
    ).toBeTruthy()
    expect(screen.queryByText("Spaces where your team works.")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: /^Home$/ }))
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
    expect(
      screen.getByRole("button", { name: "Message Jordan Lee" })
    ).toBeTruthy()
    expect(
      screen.queryByRole("heading", { name: "Make room for someone new." })
    ).toBeNull()
    expect(
      screen.queryByRole("button", {
        name: "Open conversation with Jordan Lee",
      })
    ).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Message Jordan Lee" }))
    expect(
      screen.getByRole("heading", { name: "Jordan Lee", level: 1 })
    ).toBeTruthy()
    expect(
      screen.queryByRole("heading", { name: "People", level: 1 })
    ).toBeNull()
    expect(
      screen.getByRole("button", { name: "Open conversation with Jordan Lee" })
    ).toBeTruthy()
    fireEvent.change(
      screen.getByRole("textbox", { name: "Message Jordan Lee" }),
      {
        target: { value: "Hello Jordan" },
      }
    )
    fireEvent.click(screen.getByRole("button", { name: "Send message" }))
    expect(
      within(screen.getByLabelText("Conversation with Jordan Lee")).getByText(
        "Hello Jordan"
      )
    ).toBeTruthy()
    expect(
      within(screen.getByLabelText("Conversations")).getByText("Hello Jordan")
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Back to people" }))
    expect(
      screen.getByRole("heading", { name: "People", level: 1 })
    ).toBeTruthy()
    expect(screen.getByRole("button", { name: "Message Sam Kim" })).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Message Sam Kim" }))
    expect(
      screen.getByRole("button", { name: "Open conversation with Sam Kim" })
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /^Teamspaces/ }))
    fireEvent.click(
      screen.getByRole("button", { name: "Open conversation with Jordan Lee" })
    )
    expect(
      within(screen.getByLabelText("Conversation with Jordan Lee")).getByText(
        "Hello Jordan"
      )
    ).toBeTruthy()
    fireEvent.click(
      screen.getByRole("button", { name: "Collapse left sidebar" })
    )
    expect(
      screen.getByRole("button", { name: "Open conversation with Sam Kim" })
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))

    fireEvent.click(screen.getByRole("button", { name: "Open user menu" }))
    fireEvent.click(screen.getByRole("menuitem", { name: "Settings" }))
    expect(screen.getByRole("dialog", { name: "Settings" })).toBeTruthy()
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
      screen.getByRole("heading", { name: "Jordan Lee", level: 1 })
    ).toBeTruthy()
  })

  it("shows a different sidebar for each mode and remembers its selection", () => {
    render(<WorkspaceShell />)

    const homeNavigation = screen.getByRole("navigation", {
      name: "Home navigation",
    })
    expect(
      within(homeNavigation).getByRole("button", { name: "Overview" })
    ).toBeTruthy()
    expect(
      within(homeNavigation).getByRole("button", { name: "Knowledge bases" })
    ).toBeTruthy()
    expect(
      screen.queryByRole("button", { name: "Multi-agent teams" })
    ).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: /^Team$/ }))
    const teamNavigation = screen.getByRole("navigation", {
      name: "Team navigation",
    })
    expect(
      within(teamNavigation).getByRole("button", { name: "People" })
    ).toBeTruthy()
    expect(
      within(teamNavigation).queryByRole("button", { name: "Overview" })
    ).toBeNull()
    fireEvent.click(
      within(teamNavigation).getByRole("button", { name: "People" })
    )
    expect(screen.getByRole("heading", { name: "People" })).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: /^Agents$/ }))
    const agentsNavigation = screen.getByRole("navigation", {
      name: "Agents navigation",
    })
    expect(
      within(agentsNavigation).getByRole("button", {
        name: "Multi-agent teams",
      })
    ).toBeTruthy()
    fireEvent.click(
      within(agentsNavigation).getByRole("button", { name: "Specialists" })
    )
    expect(screen.getByRole("heading", { name: "Specialists" })).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: /^Team$/ }))
    expect(screen.getByRole("heading", { name: "People" })).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /^Agents$/ }))
    expect(screen.getByRole("heading", { name: "Specialists" })).toBeTruthy()
  })

  it("opens focused team and agent work without the left sidebar and returns to the right section", () => {
    render(<WorkspaceShell />)

    fireEvent.click(screen.getByRole("button", { name: /^Team$/ }))
    fireEvent.click(screen.getByRole("button", { name: "Manage Product sync" }))
    fireEvent.click(screen.getByRole("button", { name: "Open teamspace" }))
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    expect(screen.getByRole("heading", { name: "Product sync" })).toBeTruthy()
    const workPanel = screen.getByRole("region", {
      name: "Product sync work panel",
    })
    const workViews = screen.getByLabelText("Work views")
    expect(workPanel.contains(workViews)).toBe(false)
    fireEvent.click(within(workViews).getByRole("button", { name: "Files" }))
    expect(
      within(workPanel).getByRole("heading", { name: "Files and context" })
    ).toBeTruthy()
    expect(within(workPanel).getByText("Handoff notes")).toBeTruthy()

    fireEvent.change(
      screen.getByRole("textbox", { name: "Message Product sync" }),
      {
        target: { value: "The checklist is ready." },
      }
    )
    expect(screen.getByRole("button", { name: "Send message" })).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Send message" }))
    expect(screen.getByText("The checklist is ready.")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Back to workspace" }))
    expect(
      screen.getByRole("navigation", { name: "Team navigation" })
    ).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "Manage Product sync" })
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: /^Agents$/ }))
    fireEvent.click(screen.getByRole("button", { name: "Open Launch crew" }))
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    expect(
      screen.getByRole("heading", { name: "Launch crew", level: 1 })
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Back to workspace" }))
    expect(
      screen.getByRole("navigation", { name: "Agents navigation" })
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

  it("replaces the activity feed with agent and team tabs", () => {
    const { container } = render(<WorkspaceShell />)
    const shell = container.querySelector(".workspace-shell")
    const panel = screen.getByRole("complementary", { name: "Agent panel" })

    for (const text of ["Activity", "Team online", "5 online"])
      expect(within(panel).queryByText(text)).toBeNull()
    expect(within(panel).queryByText(/launch timeline/)).toBeNull()
    expect(within(panel).queryByRole("tablist")).toBeNull()

    expect(shell?.getAttribute("data-left")).toBe("expanded")
    fireEvent.click(screen.getByRole("button", { name: "Expand agent panel" }))
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(shell?.getAttribute("data-right")).toBe("expanded")

    expect(
      within(panel)
        .getAllByRole("tab")
        .map((item) => item.getAttribute("aria-label"))
    ).toEqual(["Agents", "DMs", "Teams"])
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
      ["ArrowRight", "Agents", "Ask anything to start a conversation."],
      ["ArrowLeft", "Teams", "Team rooms will appear here."],
      ["Home", "Agents", "Ask anything to start a conversation."],
      ["End", "Teams", "Team rooms will appear here."],
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

  it("closes the mobile sidebar after selecting a conversation", () => {
    vi.stubGlobal("matchMedia", () => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
    const { container } = render(
      <WorkspaceShell preview initialMode="team" initialSection="people" />
    )
    const shell = container.querySelector(".workspace-shell")

    fireEvent.click(screen.getByRole("button", { name: "Message Jordan Lee" }))
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    expect(shell?.getAttribute("data-left")).toBe("expanded")
    fireEvent.click(
      screen.getByRole("button", { name: "Open conversation with Jordan Lee" })
    )
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    expect(
      screen.getByRole("textbox", { name: "Message Jordan Lee" })
    ).toBeTruthy()
  })

  it("lets an admin filter, resume, and create teamspaces", () => {
    render(<WorkspaceShell />)
    fireEvent.click(screen.getByRole("button", { name: /^Team$/ }))

    fireEvent.click(screen.getByRole("button", { name: "Paused" }))
    expect(
      screen.getByRole("button", { name: "Manage Design reviews" })
    ).toBeTruthy()
    expect(
      screen.queryByRole("button", { name: "Manage Product sync" })
    ).toBeNull()

    fireEvent.click(
      screen.getByRole("button", { name: "Manage Design reviews" })
    )
    expect(
      screen.getByRole("heading", { name: "Design reviews", level: 1 })
    ).toBeTruthy()
    expect(
      screen.queryByRole("heading", { name: "Teamspaces", level: 1 })
    ).toBeNull()
    expect(screen.queryByRole("button", { name: "Open teamspace" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Resume teamspace" }))
    expect(screen.getByRole("button", { name: "Open teamspace" })).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "All teamspaces" }))

    fireEvent.click(screen.getByRole("button", { name: "New teamspace" }))
    fireEvent.click(screen.getByRole("button", { name: "Create teamspace" }))
    expect(screen.getByText("Use at least 2 characters.")).toBeTruthy()
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "Customer success" },
    })
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "Customer handoffs and plans." },
    })
    fireEvent.click(screen.getByRole("button", { name: "Create teamspace" }))
    expect(
      screen.getByRole("heading", { name: "Customer success" })
    ).toBeTruthy()
    expect(
      within(
        screen.getByRole("region", { name: "Teamspace members" })
      ).getByText("Avery Morgan")
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Open teamspace" }))
    expect(
      screen.getByText("No messages yet. Start the conversation here.")
    ).toBeTruthy()
  })

  it("hides management actions from a teamspace member", () => {
    function MemberTeamspaces() {
      const [selectedId, setSelectedId] = useState<string | null>(null)
      return (
        <TeamspacesManagement
          role="member"
          spaces={initialTeamspaces}
          selectedId={selectedId}
          onSelect={setSelectedId}
          onCreate={vi.fn()}
          onStatusChange={vi.fn()}
          onOpen={vi.fn()}
        />
      )
    }
    render(<MemberTeamspaces />)

    expect(screen.queryByRole("button", { name: "New teamspace" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Manage Product sync" }))
    expect(screen.queryByRole("button", { name: "Pause teamspace" })).toBeNull()
    expect(screen.getByRole("button", { name: "Open teamspace" })).toBeTruthy()
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
    const json = (body: unknown) =>
      new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      })
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

  it("uses the visible mobile viewport for chat while composing", () => {
    vi.stubGlobal("matchMedia", () => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
    const viewport = Object.assign(new EventTarget(), { height: 420 })
    vi.stubGlobal("visualViewport", viewport)
    const { container } = render(<WorkspaceShell />)

    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    fireEvent.click(screen.getByRole("button", { name: /^Team$/ }))
    fireEvent.click(screen.getByRole("button", { name: "Manage Product sync" }))
    fireEvent.click(screen.getByRole("button", { name: "Open teamspace" }))

    const focus = container.querySelector<HTMLElement>(".workspace-focus")
    const input = screen.getByRole("textbox", { name: "Message Product sync" })
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
