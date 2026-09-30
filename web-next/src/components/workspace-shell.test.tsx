// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  act,
  cleanup,
  fireEvent,
  render as renderBase,
  renderHook,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactElement } from "react"
import { WorkspaceShell } from "./workspace-shell"
import { primary, request } from "./agents-fake-backend"
import type { Session } from "@/lib/auth/client"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import {
  claimPrimaryChatRequest,
  rememberChoice,
  usePrimaryChatRequest,
} from "@/lib/threads/chat-choice"
import type { Item, Thread } from "@/lib/threads/client"
import { json, mockApi, orgPath, viewerId } from "@/test/teamspaces"
import { browserStorage } from "@/test/browser-storage"
import { agentChats } from "@/test/agent-chat"
import { roomSockets } from "@/test/room-socket"
import { rememberSettingsPage } from "@/lib/settings/page-choice"

// The shell is one sidebar and one central screen; the center hosts the
// primary agent's chat, whose socket is a stand-in (src/test/room-socket.ts,
// src/test/agent-chat.ts).
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)
vi.mock(
  "@cloudflare/ai-chat/react",
  async () => (await import("@/test/agent-chat")).aiChatReact
)
vi.mock("thinking-orbs", () => ({
  ThinkingOrb: ({ state }: { state: string }) => (
    <i aria-hidden="true" data-orb-state={state} />
  ),
}))

let client: QueryClient

function render(ui: ReactElement) {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return renderBase(ui, {
    wrapper: ({ children }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    ),
  })
}

const organization = { id: "org_sim", name: "Simulated Org", slug: "org-sim" }
const session: Session = {
  user: { id: viewerId, email: "member@sim.oryxa", display_name: "Member" },
  organizations: [organization],
}
const agentId = primary.id
const scope = { organizationId: organization.id, userId: viewerId }

function SignedIn(props: Partial<Parameters<typeof WorkspaceShell>[0]> = {}) {
  return (
    <WorkspaceShell
      session={session}
      selectedOrganization={organization}
      role="member"
      organizationRole="member"
      {...props}
    />
  )
}

/** A conversation last active `age` ago (a minute by default). */
function thread(id: string, title: string, age = 60_000): Thread {
  return {
    id,
    title,
    origin: "chat",
    updated_at: new Date(Date.now() - age).toISOString(),
  }
}

type Seed = {
  threads?: Thread[]
  items?: Record<string, Item[]>
  pending?: Record<string, string[]>
  inbox?: AgentRequest[]
  agentStatus?: "active" | "disabled"
}

/** Go as the shell reads it for `org_sim`'s member: their agent, threads, requests and the desk's reads. */
function backend(seed: Seed = {}) {
  let threads = seed.threads ?? []
  let made = 0
  return mockApi(({ url, method }) => {
    const path = url.pathname
    if (path === `${orgPath}/agents`)
      return json({
        agents: [
          {
            ...primary,
            organization_id: organization.id,
            owner_user_id: viewerId,
            status: seed.agentStatus ?? "active",
          },
        ],
      })
    if (path === `${orgPath}/threads`) {
      if (method === "POST") {
        made += 1
        const created = thread(made === 1 ? "th_new" : `th_new_${made}`, "", 0)
        threads = [created, ...threads]
        return json(created, 201)
      }
      return json({ threads })
    }
    const detail = new RegExp(`^${orgPath}/threads/([^/]+)$`).exec(path)
    if (detail) {
      const found = threads.find((item) => item.id === detail[1])
      return found
        ? json({
            ...found,
            items: seed.items?.[found.id] ?? [],
            pending_confirmations: seed.pending?.[found.id] ?? [],
          })
        : json({ error: { code: "not_found", message: "no" } }, 404)
    }
    if (path.endsWith("/get-messages")) return json([])
    if (path === `${orgPath}/agent-requests/inbox`)
      return json({ requests: seed.inbox ?? [] })
    if (path === `${orgPath}/agent-requests/sent`) return json({ requests: [] })
    if (path === `${orgPath}/agent-requests/activity`)
      return json({ activity: [] })
    if (path === `${orgPath}/agent-leases`)
      return json({
        leases: [],
        observed_at: new Date().toISOString(),
        has_more: false,
      })
    if (path === `${orgPath}/corpora`) return json({ corpora: [] })
    if (path === `${orgPath}/apps/slack/status`)
      return json({ status: "active", can_manage: false })
    if (path === "/v1/agent-grant") return json({ granted: true })
    if (path === `${orgPath}/members`) return json({ members: [] })
    if (path === `${orgPath}/teams`) return json({ teams: [] })
    return undefined
  })
}

const sidebar = () =>
  within(screen.getByRole("complementary", { name: "Workspace navigation" }))
const main = () => within(screen.getByRole("navigation", { name: "Main" }))
const shellOf = (container: HTMLElement) =>
  container.querySelector<HTMLElement>(".workspace-shell")
const heading = () => screen.getByRole("heading", { level: 1 }).textContent
const objectName = (id: string) => `${organization.id}~${id}`

async function socketOn(id: string) {
  await waitFor(() =>
    expect(roomSockets.named(objectName(id)).length).toBeGreaterThan(0)
  )
  return roomSockets.named(objectName(id)).at(-1)!
}

function phone(matches = true) {
  vi.stubGlobal("matchMedia", () => ({
    matches,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
}

beforeEach(() => {
  const { result, unmount } = renderHook(() => usePrimaryChatRequest())
  while (result.current) {
    const { id } = result.current
    act(() => {
      claimPrimaryChatRequest(id)
    })
  }
  unmount()
  roomSockets.reset()
  agentChats.reset()
  vi.stubGlobal("localStorage", browserStorage())
  phone(false)
})

afterEach(() => {
  cleanup()
  window.history.replaceState({}, "", "/")
  window.sessionStorage.clear()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("the workspace: one sidebar, one central screen", () => {
  it("navigates from the sidebar alone: the workspace, New chat, Requests, Knowledge, Tools, Recent and the profile menu", async () => {
    backend({ threads: [thread("th_1", "Plan the launch", 3 * 3_600_000)] })
    render(<SignedIn />)

    expect(sidebar().getByText("Simulated Org")).toBeTruthy()
    expect(sidebar().getByRole("button", { name: "New chat" })).toBeTruthy()
    expect(
      main()
        .getAllByRole("button")
        .map((button) => button.textContent)
    ).toEqual(["Requests", "Knowledge", "Tools"])
    expect(sidebar().getByRole("region", { name: "Recent" })).toBeTruthy()
    expect(
      await sidebar().findByRole("button", { name: /^Plan the launch/ })
    ).toBeTruthy()
    expect(
      sidebar().getByRole("button", { name: "Open user menu" })
    ).toBeTruthy()

    // No modes and no mode-specific navigation; no right panel or its rail.
    for (const gone of [
      /^Home$/,
      /^Agents$/,
      "Agent overview",
      "Today",
      "Expand agent panel",
      "Open agent chat",
    ])
      expect(screen.queryByRole("button", { name: gone })).toBeNull()
    expect(
      screen.queryByRole("complementary", { name: "Agent panel" })
    ).toBeNull()
    expect(screen.queryByRole("group", { name: "Main areas" })).toBeNull()
    expect(screen.queryByRole("tablist")).toBeNull()
    // What was archived is reachable from nowhere.
    for (const archived of [
      "Team",
      "Teamspaces",
      "People",
      "Multi-agent teams",
      "Specialists",
      "Workflows",
    ])
      expect(screen.queryByRole("button", { name: archived })).toBeNull()

    // With no conversation to reopen, the center is the desk.
    expect(
      await screen.findByRole("heading", { name: "Today", level: 1 })
    ).toBeTruthy()
    expect(screen.getByRole("main", { name: "Today canvas" })).toBeTruthy()
  })

  it("shows exactly one screen in the center, named in the header, and marks where the member is", async () => {
    backend()
    render(<SignedIn />)
    await screen.findByRole("heading", { name: "Today", level: 1 })

    for (const [item, title] of [
      ["Requests", "Requests"],
      ["Knowledge", "Knowledge bases"],
      ["Tools", "Tools"],
    ] as const) {
      fireEvent.click(main().getByRole("button", { name: item }))
      expect(heading()).toBe(title)
      expect(screen.getAllByRole("main")).toHaveLength(1)
      expect(
        main().getByRole("button", { name: item }).getAttribute("aria-current")
      ).toBe("page")
      expect(
        main()
          .getAllByRole("button")
          .filter((button) => button.getAttribute("aria-current"))
      ).toHaveLength(1)
    }
    // The desk and the chat are hidden, not gone, while another screen shows.
    expect(screen.queryByRole("region", { name: "Your agent" })).toBeNull()

    fireEvent.click(sidebar().getByRole("button", { name: "New chat" }))
    expect(
      await screen.findByRole("heading", { name: "Today", level: 1 })
    ).toBeTruthy()
    expect(
      main()
        .getAllByRole("button")
        .some((button) => button.getAttribute("aria-current"))
    ).toBe(false)
    expect(screen.getByRole("region", { name: "Your agent" })).toBeTruthy()
  })

  it("counts on Requests the Inbox requests waiting on the member", async () => {
    backend({
      inbox: [
        request({ id: "req_open", status: "pending" }),
        request({ id: "req_review", status: "review" }),
        request({ id: "req_done", status: "completed" }),
        request({
          id: "req_lapsed",
          status: "pending",
          expires_at: "2000-01-01T00:00:00Z",
        }),
      ],
    })
    render(<SignedIn />)
    const requests = await main().findByRole("button", {
      name: "Requests, 2 waiting",
    })
    expect(requests.textContent).toBe("Requests2")
  })

  it("keeps workspace invitations and people in Settings, from the profile menu", () => {
    render(<WorkspaceShell preview />)

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
    for (const section of ["Teams", "Tool access", "Email", "Billing"]) {
      fireEvent.click(screen.getByRole("button", { name: section }))
      expect(
        screen.getByRole("heading", { name: section, level: 2 })
      ).toBeTruthy()
    }
    fireEvent.click(screen.getByRole("button", { name: "Close settings" }))
    expect(screen.queryByRole("dialog", { name: "Settings" })).toBeNull()
    expect(heading()).toBe("Today")
  })

  it("shows no example knowledge bases without a workspace", () => {
    render(<WorkspaceShell />)
    fireEvent.click(main().getByRole("button", { name: "Knowledge" }))
    expect(
      screen.getByText(/Sign in to a workspace to see its knowledge bases/)
    ).toBeTruthy()
    const canvas = screen.getByRole("main", { name: "Knowledge bases canvas" })
    expect(within(canvas).queryByRole("button", { name: /^Open / })).toBeNull()
    expect(
      within(canvas).queryByRole("button", { name: "New knowledge base" })
    ).toBeNull()
  })

  it("opens a real knowledge base and names it in the header", async () => {
    const acme = { id: "org_1", name: "Acme", slug: "acme" }
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
      "/v1/organizations/org_1/threads": { threads: [] },
      "/v1/organizations/org_1/agents": { agents: [] },
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
          organizations: [acme],
        }}
        selectedOrganization={acme}
        organizationRole="member"
      />
    )
    fireEvent.click(main().getByRole("button", { name: "Knowledge" }))

    fireEvent.click(
      await screen.findByRole("button", { name: "Open Product handbook" })
    )
    expect(
      screen.getByRole("heading", { name: "Product handbook", level: 1 })
    ).toBeTruthy()
    expect(screen.getByText("Knowledge base")).toBeTruthy()
    expect(await screen.findByText("Design principles")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "All knowledge bases" }))
    expect(heading()).toBe("Knowledge bases")
    expect(
      screen.getByRole("button", { name: "Open Product handbook" })
    ).toBeTruthy()
  })
})

describe("the sidebar on a desktop", () => {
  it("collapses to an icon rail and back, and remembers the choice in this browser", async () => {
    backend({ threads: [thread("th_1", "Plan the launch", 3 * 3_600_000)] })
    const view = render(<SignedIn />)
    const shell = shellOf(view.container)
    await sidebar().findByRole("button", { name: /^Plan the launch/ })
    expect(shell?.getAttribute("data-sidebar")).toBe("expanded")

    fireEvent.click(sidebar().getByRole("button", { name: "Collapse sidebar" }))
    expect(shell?.getAttribute("data-sidebar")).toBe("collapsed")
    // Icons only, still named; no Recent list and no workspace name.
    expect(
      main()
        .getAllByRole("button")
        .map((button) => button.getAttribute("aria-label"))
    ).toEqual(["Requests", "Knowledge", "Tools"])
    expect(sidebar().getByRole("button", { name: "New chat" })).toBeTruthy()
    expect(sidebar().queryByRole("region", { name: "Recent" })).toBeNull()
    expect(sidebar().queryByText("Simulated Org")).toBeNull()
    expect(localStorage.getItem("oryxa.sidebar")).toBe("collapsed")

    // The rail still navigates.
    fireEvent.click(main().getByRole("button", { name: "Requests" }))
    expect(heading()).toBe("Requests")

    // A reload keeps it collapsed.
    view.unmount()
    const again = render(<SignedIn />)
    expect(shellOf(again.container)?.getAttribute("data-sidebar")).toBe(
      "collapsed"
    )
    fireEvent.click(sidebar().getByRole("button", { name: "Expand sidebar" }))
    expect(shellOf(again.container)?.getAttribute("data-sidebar")).toBe(
      "expanded"
    )
    expect(localStorage.getItem("oryxa.sidebar")).toBeNull()
    expect(
      await sidebar().findByRole("button", { name: /^Plan the launch/ })
    ).toBeTruthy()
  })
})

describe("the sidebar on a phone", () => {
  it("is an overlay the header's menu opens, that closes after a selection, on Escape, from its close button or outside it", async () => {
    phone()
    backend({ threads: [thread("th_1", "Plan the launch", 3 * 3_600_000)] })
    const view = render(<SignedIn />)
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    await screen.findByRole("heading", { name: "Today", level: 1 })
    const menu = screen.getByRole("button", { name: "Open navigation" })
    expect(menu.getAttribute("aria-expanded")).toBe("false")

    // A selection closes it, and the center takes the whole width.
    fireEvent.click(menu)
    expect(menu.getAttribute("aria-expanded")).toBe("true")
    expect(document.activeElement).toBe(
      sidebar().getByRole("button", { name: "Close navigation" })
    )
    fireEvent.click(main().getByRole("button", { name: "Requests" }))
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    expect(heading()).toBe("Requests")

    // A conversation from Recent, too.
    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }))
    fireEvent.click(
      await sidebar().findByRole("button", { name: /^Plan the launch/ })
    )
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    await waitFor(() => expect(heading()).toBe("Plan the launch"))

    // Escape, the close button and a tap outside close it as well.
    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }))
    fireEvent.keyDown(window, { key: "Escape" })
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }))
    fireEvent.click(sidebar().getByRole("button", { name: "Close navigation" }))
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }))
    fireEvent.click(view.container.querySelector(".workspace-scrim")!)
    expect(
      screen.queryByRole("complementary", { name: "Workspace navigation" })
    ).toBeNull()
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Open navigation" })
    )
  })

  it("fits the page to the visible height while a phone's keyboard is up", async () => {
    phone()
    const viewport = Object.assign(new EventTarget(), { height: 420 })
    vi.stubGlobal("visualViewport", viewport)
    backend()
    const view = render(<SignedIn />)
    const shell = shellOf(view.container)
    const box = await screen.findByRole("textbox", {
      name: "Start a new conversation",
    })
    fireEvent.focus(box)
    expect(shell?.getAttribute("data-composing")).toBe("true")
    expect(shell?.style.getPropertyValue("--visible-height")).toBe("420px")

    act(() => {
      viewport.height = 300
      viewport.dispatchEvent(new Event("resize"))
    })
    expect(shell?.style.getPropertyValue("--visible-height")).toBe("300px")

    fireEvent.blur(box)
    expect(shell?.getAttribute("data-composing")).toBeNull()
    expect(shell?.style.getPropertyValue("--visible-height")).toBe("")
  })
})

describe("the member's agent in the center", () => {
  it("opens a conversation from Recent in the center, marked current, with its title and New chat, which returns to the desk", async () => {
    backend({
      threads: [
        thread("th_1", "Plan the launch", 3 * 3_600_000),
        thread("th_2", "Hiring pipeline", 4 * 3_600_000),
      ],
    })
    render(<SignedIn />)
    await screen.findByRole("heading", { name: "Today", level: 1 })
    const row = await sidebar().findByRole("button", {
      name: /^Plan the launch/,
    })
    expect(row.getAttribute("aria-current")).toBeNull()

    fireEvent.click(row)
    ;(await socketOn("th_1")).open()
    expect(heading()).toBe("Plan the launch")
    expect(screen.getByRole("region", { name: "Agent chat" })).toBeTruthy()
    expect(
      screen.getByRole("main", { name: "Conversation canvas" })
    ).toBeTruthy()
    expect(
      await screen.findByRole("textbox", { name: "Message Leo" })
    ).toBeTruthy()
    expect(screen.queryByRole("region", { name: "Your agent" })).toBeNull()
    expect(
      sidebar()
        .getByRole("button", { name: /^Plan the launch/ })
        .getAttribute("aria-current")
    ).toBe("true")

    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(heading()).toBe("Today")
    expect(screen.getByRole("region", { name: "Your agent" })).toBeTruthy()
    expect(
      sidebar()
        .getByRole("button", { name: /^Plan the launch/ })
        .getAttribute("aria-current")
    ).toBeNull()
  })

  it("keeps the conversation, its socket and its draft, while another screen is in the center", async () => {
    backend({ threads: [thread("th_1", "Plan the launch", 3 * 3_600_000)] })
    render(<SignedIn />)
    fireEvent.click(
      await sidebar().findByRole("button", { name: /^Plan the launch/ })
    )
    ;(await socketOn("th_1")).open()
    fireEvent.change(
      await screen.findByRole("textbox", { name: "Message Leo" }),
      { target: { value: "Half a thought" } }
    )

    fireEvent.click(main().getByRole("button", { name: "Requests" }))
    expect(heading()).toBe("Requests")
    expect(screen.queryByRole("region", { name: "Agent chat" })).toBeNull()
    expect(roomSockets.named(objectName("th_1"))).toHaveLength(1)
    // Not current while another screen shows.
    expect(
      sidebar()
        .getByRole("button", { name: /^Plan the launch/ })
        .getAttribute("aria-current")
    ).toBeNull()

    fireEvent.click(sidebar().getByRole("button", { name: /^Plan the launch/ }))
    expect(heading()).toBe("Plan the launch")
    expect(roomSockets.named(objectName("th_1"))).toHaveLength(1)
    expect(screen.getByDisplayValue("Half a thought")).toBeTruthy()
  })

  it("starts a conversation from the desk and shows it in the center, never a side panel", async () => {
    const api = backend()
    const view = render(<SignedIn />)
    const box = await screen.findByRole("textbox", {
      name: "Start a new conversation",
    })
    fireEvent.change(box, { target: { value: "  What changed today?  " } })
    fireEvent.click(
      within(screen.getByRole("region", { name: "Your agent" })).getByRole(
        "button",
        { name: "Send message" }
      )
    )
    // The center is the conversation at once, the message in it.
    expect(heading()).toBe("New conversation")
    expect(
      screen.getByRole("log").querySelector('[data-from="member"]')?.textContent
    ).toBe("What changed today?")
    ;(await socketOn("th_new")).open()
    expect(
      agentChats.named(objectName("th_new")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith(
      { text: "What changed today?" },
      {
        body: {
          screen_context: expect.objectContaining({ view: "home/overview" }),
        },
      }
    )
    expect(
      api.calls.filter(
        (call) => call.method === "POST" && call.path === `${orgPath}/threads`
      )
    ).toHaveLength(1)
    expect(view.container.querySelector(".workspace-right")).toBeNull()
    expect(
      screen.queryByRole("complementary", { name: "Agent panel" })
    ).toBeNull()
  })

  it("reopens, as the app opens, the conversation the member left here less than five minutes ago", async () => {
    backend({
      threads: [
        thread("th_new_one", "Newest", 60_000),
        thread("th_left", "The one they left", 2 * 3_600_000),
      ],
    })
    rememberChoice(scope, agentId, "th_left", Date.now() - 60_000)
    render(<SignedIn />)
    await socketOn("th_left")
    expect(heading()).toBe("The one they left")
    expect(roomSockets.named(objectName("th_new_one"))).toHaveLength(0)
  })

  it("stays on the desk, as the app opens, after an explicit New chat less than five minutes ago", async () => {
    backend({
      threads: [thread("th_1", "Waiting", 60_000)],
      pending: { th_1: ["approval"] },
    })
    rememberChoice(scope, agentId, null, Date.now() - 60_000)
    render(<SignedIn />)
    expect(
      await screen.findByRole("heading", { name: "Today", level: 1 })
    ).toBeTruthy()
    await act(async () => {})
    expect(roomSockets.all).toHaveLength(0)
  })

  it("opens, as the app opens, the newest conversation waiting on the member's approval", async () => {
    backend({
      threads: [
        thread("th_quiet", "Nothing waits", 60_000),
        thread("th_waiting", "Send the email?", 3_600_000),
      ],
      pending: { th_waiting: ["approval"] },
    })
    render(<SignedIn />)
    await socketOn("th_waiting")
    expect(heading()).toBe("Send the email?")
    expect(
      sidebar().getByRole("button", {
        name: /^Send the email\?.*Needs approval/,
      })
    ).toBeTruthy()
  })

  it("opens the conversation's files inline from its header, and a file in place of the conversation", async () => {
    const items: Item[] = [
      { type: "message", id: "ev_1", author: "member", text: "Write it up" },
      { type: "artifact", id: "ev_2", artifact: "report.txt", version: 1 },
    ]
    backend({
      threads: [thread("th_1", "Write it up", 3 * 3_600_000)],
      items: { th_1: items },
    })
    render(<SignedIn />)
    fireEvent.click(
      await sidebar().findByRole("button", { name: /^Write it up/ })
    )
    ;(await socketOn("th_1")).open()

    const files = await screen.findByRole("button", {
      name: "Files, 1 in this conversation",
    })
    expect(files.getAttribute("aria-expanded")).toBe("false")
    fireEvent.click(files)
    expect(files.getAttribute("aria-expanded")).toBe("true")
    const listed = screen.getByRole("region", {
      name: "Files in this conversation",
    })
    fireEvent.click(within(listed).getByRole("button", { name: "report.txt" }))

    // The viewer takes the conversation's place; the chat stays mounted.
    expect(screen.getByRole("region", { name: "File viewer" })).toBeTruthy()
    expect(screen.queryByRole("region", { name: "Agent chat" })).toBeNull()
    expect(roomSockets.named(objectName("th_1"))).toHaveLength(1)
    fireEvent.click(
      screen.getByRole("button", { name: "Back to conversation" })
    )
    expect(screen.queryByRole("region", { name: "File viewer" })).toBeNull()
    expect(screen.getByRole("region", { name: "Agent chat" })).toBeTruthy()

    // New chat leaves the conversation, and its files with it.
    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(screen.queryByRole("button", { name: /^Files/ })).toBeNull()
  })

  it("says, on the desk and in a conversation, that the member's agent is paused, with Resume", async () => {
    backend({
      threads: [thread("th_1", "Plan the launch", 3 * 3_600_000)],
      agentStatus: "disabled",
    })
    render(<SignedIn />)
    const agent = await screen.findByRole("region", { name: "Your agent" })
    expect(await within(agent).findByText("Leo is paused.")).toBeTruthy()
    fireEvent.click(
      await sidebar().findByRole("button", { name: /^Plan the launch/ })
    )
    ;(await socketOn("th_1")).open()
    expect(screen.getByText("Leo is paused.")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Resume Leo" })).toBeTruthy()
  })
})

describe("links into the workspace", () => {
  it("opens the conversation `?chat=` names, and leaves the address, so a reload follows the opening rules", async () => {
    window.history.replaceState({}, "", "/?org=org_sim&chat=th_2")
    backend({
      threads: [
        thread("th_1", "Newest", 60_000),
        thread("th_2", "Linked conversation", 5 * 3_600_000),
      ],
    })
    render(<SignedIn />)
    await socketOn("th_2")
    await waitFor(() => expect(heading()).toBe("Linked conversation"))
    expect(window.location.search).toBe("?org=org_sim")
  })

  it.each([
    ["knowledge", "Knowledge bases"],
    ["tools", "Tools"],
    ["requests", "Requests"],
  ])(
    "opens `?view=%s` on its screen, and the address follows the screen after",
    async (view, title) => {
      window.history.replaceState({}, "", `/?org=org_sim&view=${view}`)
      backend()
      render(<SignedIn />)
      expect(heading()).toBe(title)

      fireEvent.click(
        main().getByRole("button", {
          name: view === "requests" ? "Knowledge" : "Requests",
        })
      )
      expect(new URLSearchParams(window.location.search).get("view")).toBe(
        view === "requests" ? "knowledge" : "requests"
      )
      // The member's agent has no view: a reload there follows the opening rules.
      fireEvent.click(sidebar().getByRole("button", { name: "New chat" }))
      expect(window.location.search).toBe("?org=org_sim")
    }
  )

  it.each(["billing", "slack"] as const)(
    "keeps the explicit %s return ahead of the remembered section",
    (page) => {
      backend()
      rememberSettingsPage(scope, "email")
      window.history.replaceState({}, "", `/?view=${page}`)
      render(<SignedIn organizationRole="admin" />)
      expect(
        screen.getByRole("heading", {
          name: page === "billing" ? "Billing" : "Slack",
          level: 2,
        })
      ).toBeTruthy()
      fireEvent.click(screen.getByRole("button", { name: "Tool access" }))
      fireEvent.click(screen.getByRole("button", { name: "Open my tools →" }))
      expect(screen.queryByRole("dialog", { name: "Settings" })).toBeNull()
      expect(heading()).toBe("Tools")
      fireEvent.click(screen.getByRole("button", { name: "Open user menu" }))
      fireEvent.click(screen.getByRole("menuitem", { name: "Settings" }))
      expect(
        screen.getByRole("heading", { name: "Tool access", level: 2 })
      ).toBeTruthy()
    }
  )
})

describe("whose workspace it is", () => {
  it("restores Settings for the same member and workspace without carrying a section to another scope", () => {
    backend()
    const props = {
      session,
      selectedOrganization: organization,
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

  it("keeps the same member's draft, screen and Settings through a session refresh, and nothing for another member", async () => {
    backend()
    const view = render(<SignedIn />)
    fireEvent.change(
      await screen.findByRole("textbox", { name: "Start a new conversation" }),
      { target: { value: "Member's unfinished message" } }
    )
    fireEvent.click(screen.getByRole("button", { name: "Open user menu" }))
    fireEvent.click(screen.getByRole("menuitem", { name: "Settings" }))
    expect(screen.getByRole("dialog", { name: "Settings" })).toBeTruthy()

    view.rerender(
      <SignedIn
        session={{
          ...session,
          user: { ...session.user, display_name: "Member refreshed" },
        }}
      />
    )
    expect(screen.getByRole("dialog", { name: "Settings" })).toBeTruthy()
    expect(screen.getByDisplayValue("Member's unfinished message")).toBeTruthy()

    view.rerender(
      <SignedIn
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
    expect(screen.queryByDisplayValue("Member's unfinished message")).toBeNull()
  })
})
