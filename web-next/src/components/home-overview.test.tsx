// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ComponentProps } from "react"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import { agentKeys } from "@/lib/agents/queries"
import { threadKeys } from "@/lib/threads/client"
import { connectionsQueryOptions } from "@/lib/apps/client"
import { ragKeys } from "@/lib/rag/client"
import type { Connection } from "@/lib/apps/client"
import type { Corpus, CorpusDocument } from "@/lib/rag/client"
import { HomeOverview } from "./home-overview"

vi.mock("thinking-orbs", () => ({
  ThinkingOrb: ({ state }: { state: string }) => (
    <i aria-hidden="true" data-orb-state={state} />
  ),
}))

const path = "/v1/organizations/org_1"
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

function request(overrides: Partial<AgentRequest> = {}): AgentRequest {
  return {
    id: "req_1",
    organization_id: "org_1",
    approval: "owner",
    requester: { user_id: "peer", agent_id: "agt_peer", agent_name: "Nia" },
    target: { user_id: "member", agent_id: "agt_member", agent_name: "Leo" },
    purpose: "Review the launch plan",
    task: "Draft a launch plan",
    context: [],
    requested_tools: [],
    requested_permissions: [],
    status: "pending",
    revision: 1,
    expires_at: new Date(Date.now() + 60_000).toISOString(),
    created_at: "",
    updated_at: "",
    ...overrides,
  }
}

function connection(id: string, status: Connection["status"]): Connection {
  return {
    id,
    toolkit: id,
    label: id,
    status,
    member_id: "member",
    auto_subscribe: false,
    created_at: "",
    updated_at: "",
  }
}

function corpus(id: string, documentCount = 1): Corpus {
  return {
    id,
    organization_id: "org_1",
    slug: id,
    name: id,
    scope: "member",
    member_id: "member",
    embedding: {
      provider: "vertex",
      model: "text-embedding-005",
      dimensions: 768,
      metric: "cosine",
    },
    backend: "pgvector",
    chunking: { strategy: "structure", max_tokens: 512, overlap_tokens: 76 },
    metadata: {},
    document_count: documentCount,
    chunk_count: 0,
    created_by: "member",
    created_at: "",
    updated_at: "",
  }
}

function failedDocument(id: string): CorpusDocument {
  return {
    id,
    corpus_id: "base_1",
    organization_id: "org_1",
    source: { kind: "inline" },
    title: id,
    content_type: "text/plain",
    content_sha256: "",
    byte_size: 0,
    status: "failed",
    chunk_count: 0,
    attempts: 1,
    created_by: "member",
    created_at: "",
    updated_at: "",
  }
}

function stubApi(overrides: Record<string, unknown> = {}) {
  const routes: Record<string, unknown> = {
    [`${path}/agent-requests/inbox`]: { requests: [] },
    [`${path}/threads`]: { threads: [] },
    [`${path}/apps/connections`]: { connections: [] },
    [`${path}/corpora`]: { corpora: [] },
    ...overrides,
  }
  const calls: URL[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost")
      calls.push(url)
      const value = routes[url.pathname]
      if (typeof value === "function")
        return (value as (url: URL) => Response | Promise<Response>)(url)
      return value === undefined
        ? json({ error: { code: "not_found", message: "No route" } }, 404)
        : json(value)
    })
  )
  return calls
}

function renderHome(
  overrides: Partial<ComponentProps<typeof HomeOverview>> = {}
) {
  const callbacks = {
    onNewChat: vi.fn(),
    onOpenRequests: vi.fn(),
    onOpenAgent: vi.fn(),
    onOpenConversation: vi.fn(),
    onOpenTools: vi.fn(),
    onOpenKnowledge: vi.fn(),
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const props = {
    organizationId: "org_1",
    viewerId: "member",
    ...callbacks,
    ...overrides,
  }
  const view = render(
    <QueryClientProvider client={client}>
      <HomeOverview {...props} />
    </QueryClientProvider>
  )
  return {
    ...view,
    ...callbacks,
    client,
    rerenderHome: (next: Partial<ComponentProps<typeof HomeOverview>>) =>
      view.rerender(
        <QueryClientProvider client={client}>
          <HomeOverview {...props} {...next} />
        </QueryClientProvider>
      ),
  }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("HomeOverview", () => {
  it("counts only this member's unexpired owner approvals and held answers, with an honest limited count", async () => {
    const waiting = [
      request(),
      request({
        id: "review",
        status: "review",
        purpose: "Review Nia’s answer",
      }),
      request({
        id: "legacy",
        approval: undefined,
        purpose: "Legacy owner approval",
      }),
      request({ id: "fourth", purpose: "Fourth approval" }),
    ]
    const ignored = [
      request({
        id: "automatic",
        approval: "agent",
        purpose: "Automatic work",
      }),
      request({
        id: "approved",
        status: "approved",
        purpose: "Already approved",
      }),
      request({
        id: "expired",
        expires_at: new Date(Date.now() - 1000).toISOString(),
        purpose: "Expired work",
      }),
      request({
        id: "other",
        target: {
          user_id: "other",
          agent_id: "agt_other",
          agent_name: "Other",
        },
        purpose: "Other member work",
      }),
      ...Array.from({ length: 92 }, (_, i) =>
        request({ id: `done_${i}`, status: "completed" })
      ),
    ]
    const calls = stubApi({
      [`${path}/agent-requests/inbox`]: { requests: [...waiting, ...ignored] },
    })
    const callbacks = renderHome()
    const card = await screen.findByRole("region", { name: "Waiting on you" })
    expect(
      within(card).getByRole("heading", { name: "Waiting on you · 4+" })
    ).toBeTruthy()
    expect(within(card).getAllByRole("listitem")).toHaveLength(3)
    expect(within(card).getByText("Answer to review · Nia")).toBeTruthy()
    for (const title of [
      "Fourth approval",
      "Automatic work",
      "Already approved",
      "Expired work",
      "Other member work",
    ])
      expect(screen.queryByText(title)).toBeNull()
    fireEvent.click(within(card).getByRole("button", { name: "All requests" }))
    fireEvent.click(
      within(card).getByRole("button", { name: /Legacy owner approval/ })
    )
    expect(callbacks.onOpenRequests).toHaveBeenCalledTimes(2)
    expect(callbacks.onOpenRequests).toHaveBeenNthCalledWith(1)
    expect(callbacks.onOpenRequests).toHaveBeenNthCalledWith(2, "legacy")
    expect(
      calls
        .find((url) => url.pathname.endsWith("/inbox"))
        ?.searchParams.get("limit")
    ).toBe("100")
    expect(
      within(card).getByText("Only the latest 100 requests were checked.")
    ).toBeTruthy()
  })

  it("puts member decisions before recent conversations and failures below the composer", async () => {
    stubApi({
      [`${path}/agent-requests/inbox`]: { requests: [request()] },
      [`${path}/threads`]: {
        threads: [
          {
            id: "recent",
            title: "Recent conversation",
            origin: "chat",
            updated_at: "2026-09-23T10:00:00Z",
          },
        ],
      },
      [`${path}/apps/connections`]: {
        connections: [connection("Broken tool", "failed")],
      },
    })
    const { container } = renderHome()
    await screen.findByRole("region", { name: "Waiting on you" })
    await screen.findByRole("region", { name: "Your agent" })
    await screen.findByRole("region", { name: "Needs attention" })
    expect(
      Array.from(
        container.querySelectorAll("textarea[aria-label], section[aria-label]")
      ).map((element) => element.getAttribute("aria-label"))
    ).toEqual([
      "Start a new conversation",
      "Waiting on you",
      "Your agent",
      "Needs attention",
    ])
  })

  it("keeps three real recent conversations below the composer even when no decision or failure needs the member", async () => {
    stubApi({
      [`${path}/threads`]: {
        threads: [
          {
            id: "app_update",
            title: "Private app update",
            origin: "app_update",
            updated_at: "2026-09-23T10:00:00Z",
          },
          {
            id: "side",
            title: "Side conversation",
            origin: "side",
            updated_at: "2026-09-23T10:00:00Z",
          },
          ...Array.from({ length: 4 }, (_, i) => ({
            id: `old_${i}`,
            title: `Completed plan ${i}`,
            origin: i === 1 ? "chat" : undefined,
            updated_at: "2026-09-23T10:00:00Z",
          })),
        ],
      },
    })
    const callbacks = renderHome()
    expect(
      await screen.findByText("Nothing waiting in the work checked.")
    ).toBeTruthy()
    const recent = screen.getByRole("region", { name: "Your agent" })
    expect(within(recent).getAllByRole("listitem")).toHaveLength(3)
    expect(screen.queryByText("Completed plan 3")).toBeNull()
    expect(screen.queryByText("Private app update")).toBeNull()
    expect(screen.queryByText("Side conversation")).toBeNull()
    fireEvent.click(
      within(recent).getByRole("button", { name: /Completed plan 1/ })
    )
    expect(callbacks.onOpenConversation).toHaveBeenCalledExactlyOnceWith(
      "old_1"
    )
    expect(screen.getAllByRole("status")).toHaveLength(1)
    fireEvent.change(
      screen.getByRole("textbox", { name: "Start a new conversation" }),
      { target: { value: "  Start fresh  " } }
    )
    fireEvent.click(screen.getByRole("button", { name: "Send message" }))
    expect(callbacks.onNewChat).toHaveBeenCalledExactlyOnceWith("Start fresh")
  })

  it("pins selected live work outside the first three recents and deduplicates its exact conversation", async () => {
    stubApi({
      [`${path}/threads`]: {
        threads: Array.from({ length: 5 }, (_, i) => ({
          id: `th_${i}`,
          title: `Conversation ${i}`,
          updated_at: "2026-09-23T10:00:00Z",
        })),
      },
    })
    const view = renderHome({
      primaryChatActivity: { threadId: "th_4", state: "running" },
    })
    const card = await screen.findByRole("region", { name: "Your agent" })
    await within(card).findByText("Conversation 4")
    expect(
      within(card)
        .getAllByRole("listitem")[0]
        .querySelector('[data-orb-state="working"]')
    ).toBeTruthy()
    expect(
      within(card)
        .getAllByRole("listitem")
        .map((row) => row.textContent)
    ).toEqual([
      "Conversation 4 Working",
      expect.stringContaining("Conversation 0"),
      expect.stringContaining("Conversation 1"),
    ])
    fireEvent.click(
      within(card).getByRole("button", { name: /Conversation 4/ })
    )
    expect(view.onOpenConversation).toHaveBeenCalledExactlyOnceWith("th_4")
    expect(view.onNewChat).not.toHaveBeenCalled()

    view.rerenderHome({
      primaryChatActivity: { threadId: "th_1", state: "running" },
    })
    expect(
      within(card)
        .getAllByRole("listitem")
        .map((row) => row.textContent)
    ).toEqual([
      "Conversation 1 Working",
      expect.stringContaining("Conversation 0"),
      expect.stringContaining("Conversation 2"),
    ])
    view.rerenderHome({
      primaryChatActivity: { threadId: "th_1", state: "idle" },
    })
    expect(
      within(card)
        .getAllByRole("listitem")
        .map((row) => row.textContent)
    ).toEqual([
      expect.stringContaining("Conversation 0"),
      expect.stringContaining("Conversation 1"),
      expect.stringContaining("Conversation 2"),
    ])
    expect(within(card).queryByText("Working")).toBeNull()
    expect(card.querySelector("[data-orb-state]")).toBeNull()
    view.rerenderHome({ primaryChatActivity: null })
    expect(within(card).getAllByRole("listitem")).toHaveLength(3)
    expect(within(card).getAllByText("Sep 23")).toHaveLength(3)
    expect(card.querySelector("[data-orb-state]")).toBeNull()
  })

  it("shows selected tool confirmation separately without inflating Inbox decisions or claiming quiet", async () => {
    stubApi({ [`${path}/agent-requests/inbox`]: { requests: [request()] } })
    const view = renderHome({
      primaryChatActivity: { threadId: "th_held", state: "waiting" },
    })
    await screen.findByRole("heading", { name: "Waiting on you · 2" })
    const card = screen.getByRole("region", { name: "Waiting on you" })
    expect(within(card).getAllByRole("listitem")).toHaveLength(2)
    fireEvent.click(
      within(card).getByRole("button", {
        name: "Current conversation Needs confirmation",
      })
    )
    expect(view.onOpenConversation).toHaveBeenCalledExactlyOnceWith("th_held")
    expect(view.onOpenRequests).not.toHaveBeenCalled()
    fireEvent.click(
      within(card).getByRole("button", { name: "All confirmations" })
    )
    expect(view.onOpenConversation).toHaveBeenLastCalledWith("th_held")
    expect(view.onOpenConversation).toHaveBeenCalledTimes(2)
    fireEvent.click(within(card).getByRole("button", { name: "All requests" }))
    expect(view.onOpenRequests).toHaveBeenCalledExactlyOnceWith()
    expect(screen.queryByText(/Nothing waiting/)).toBeNull()

    // A confirmation alone is actionable even when the inbox is empty.
    await act(async () => {
      view.client.setQueryData(
        [...agentKeys.requestBox("org_1", "inbox", 100), "home", "member"],
        []
      )
    })
    await waitFor(() =>
      expect(
        within(card).getByRole("heading", { name: "Waiting on you · 1" })
      ).toBeTruthy()
    )
    expect(screen.getByText("Needs confirmation")).toBeTruthy()
    expect(
      within(card).queryByRole("button", { name: "All requests" })
    ).toBeNull()
    expect(
      within(card).getByRole("button", { name: "All confirmations" })
        .textContent
    ).toBe("All")
    expect(screen.queryByText(/Nothing waiting/)).toBeNull()
  })

  it("keeps a known confirmation reachable after an Inbox refusal without showing its cached private request or claiming a complete count", async () => {
    let refused = false
    stubApi({
      [`${path}/agent-requests/inbox`]: () =>
        refused
          ? json(
              { error: { code: "forbidden", message: "Private diagnostic" } },
              403
            )
          : json({ requests: [request({ purpose: "Private old approval" })] }),
    })
    const view = renderHome({
      primaryChatActivity: { threadId: "th_held", state: "waiting" },
    })
    await screen.findByRole("heading", { name: "Waiting on you · 2" })
    refused = true
    await act(async () => {
      await view.client.refetchQueries({
        queryKey: agentKeys.requestBox("org_1", "inbox", 100),
      })
    })
    await screen.findByText("Couldn’t load requests.")
    const waiting = screen.getByRole("region", { name: "Waiting on you" })
    expect(
      within(waiting).getByRole("heading", { name: "Waiting on you · 1+" })
    ).toBeTruthy()
    expect(within(waiting).getAllByRole("listitem")).toHaveLength(1)
    expect(screen.queryByText("Private old approval")).toBeNull()
    expect(screen.queryByText("Private diagnostic")).toBeNull()
    expect(
      within(waiting).queryByRole("button", { name: "All requests" })
    ).toBeNull()
    fireEvent.click(
      within(waiting).getByRole("button", { name: "All confirmations" })
    )
    expect(view.onOpenConversation).toHaveBeenCalledExactlyOnceWith("th_held")
    expect(view.onOpenRequests).not.toHaveBeenCalled()
    expect(screen.queryByText(/Nothing waiting/)).toBeNull()
  })

  it("prioritizes one confirmation conversation in the bounded mixed waiting list and keeps other recent chats reachable", async () => {
    stubApi({
      [`${path}/agent-requests/inbox`]: {
        requests: Array.from({ length: 100 }, (_, i) =>
          request({ id: `req_${i}`, purpose: `Approval ${i}` })
        ),
      },
      [`${path}/threads`]: {
        threads: [
          {
            id: "th_held",
            title: "Held conversation",
            updated_at: "2026-09-23T10:00:00Z",
          },
          ...Array.from({ length: 4 }, (_, i) => ({
            id: `recent_${i}`,
            title: `Recent ${i}`,
            updated_at: "2026-09-23T10:00:00Z",
          })),
        ],
      },
    })
    const view = renderHome({
      primaryChatActivity: { threadId: "th_held", state: "waiting" },
    })
    const waiting = await screen.findByRole("region", {
      name: "Waiting on you",
    })
    await within(waiting).findByText("Held conversation")
    expect(
      within(waiting).getByRole("heading", { name: "Waiting on you · 101+" })
    ).toBeTruthy()
    expect(
      within(waiting)
        .getAllByRole("listitem")
        .map((row) => row.textContent)
    ).toEqual([
      "Held conversation Needs confirmation",
      "Approval 0 Request to approve · Nia",
      "Approval 1 Request to approve · Nia",
    ])
    const agent = screen.getByRole("region", { name: "Your agent" })
    expect(within(agent).getAllByRole("listitem")).toHaveLength(3)
    expect(within(agent).queryByText("Held conversation")).toBeNull()
    expect(screen.queryByText("Recent 3")).toBeNull()
    expect(screen.queryByText("Approval 2")).toBeNull()
    fireEvent.click(
      within(waiting).getByRole("button", { name: "All confirmations" })
    )
    fireEvent.click(
      within(waiting).getByRole("button", { name: "All requests" })
    )
    fireEvent.click(within(agent).getByRole("button", { name: /Recent 2/ }))
    expect(view.onOpenConversation).toHaveBeenNthCalledWith(1, "th_held")
    expect(view.onOpenConversation).toHaveBeenNthCalledWith(2, "recent_2")
    expect(view.onOpenRequests).toHaveBeenCalledExactlyOnceWith()
    expect(view.onNewChat).not.toHaveBeenCalled()
    expect(screen.queryByText(/Nothing waiting/)).toBeNull()
  })

  it("counts a pending new thread as one noninteractive work row until its exact ID arrives", async () => {
    stubApi({
      [`${path}/threads`]: {
        threads: Array.from({ length: 3 }, (_, i) => ({
          id: `th_${i}`,
          title: `Recent ${i}`,
          updated_at: "2026-09-23T10:00:00Z",
        })),
      },
    })
    const view = renderHome({
      primaryChatActivity: { threadId: null, state: "running" },
    })
    const card = await screen.findByRole("region", { name: "Your agent" })
    await within(card).findByText("Recent 0")
    expect(within(card).getAllByRole("listitem")).toHaveLength(3)
    expect(
      within(card).getAllByRole("listitem")[0].querySelector("button")
    ).toBeNull()
    expect(within(card).getByRole("status").textContent).toBe(
      "Starting conversation Working"
    )
    expect(
      within(card)
        .getByRole("status")
        .querySelector('[data-orb-state="working"]')
    ).toBeTruthy()
    expect(within(card).queryByText("Recent 2")).toBeNull()
    expect(view.onNewChat).not.toHaveBeenCalled()
    view.rerenderHome({
      primaryChatActivity: { threadId: "th_created", state: "running" },
    })
    fireEvent.click(
      within(card).getByRole("button", { name: "Current conversation Working" })
    )
    expect(view.onOpenConversation).toHaveBeenCalledExactlyOnceWith(
      "th_created"
    )
    expect(view.onNewChat).not.toHaveBeenCalled()
  })

  it("checks failed documents in at most three nonempty bases and preserves partial coverage and cursors", async () => {
    const bases = Array.from({ length: 8 }, (_, i) =>
      corpus(`base_${i + 1}`, i === 7 ? 0 : 6)
    )
    const calls = stubApi({
      [`${path}/corpora`]: { corpora: bases },
      [`${path}/corpora/base_1/documents`]: {
        documents: [
          failedDocument("a"),
          failedDocument("b"),
          failedDocument("c"),
        ],
        next_cursor: "more-failures",
      },
      [`${path}/corpora/base_2/documents`]: { documents: [] },
      [`${path}/corpora/base_3/documents`]: { documents: [] },
      [`${path}/apps/connections`]: {
        connections: [
          connection("Broken tool", "failed"),
          connection("Expired tool", "expired"),
          connection("Connecting tool", "pending"),
          connection("Healthy tool", "active"),
        ],
      },
      [`${path}/threads`]: {
        threads: Array.from({ length: 4 }, (_, i) => ({
          id: `th_${i}`,
          title: `Recent ${i}`,
          updated_at: "2026-09-23T10:00:00Z",
        })),
      },
    })
    const callbacks = renderHome({
      primaryChatActivity: { threadId: "th_0", state: "running" },
    })
    expect(
      await screen.findByText("Checked 4 of 8 knowledge bases.")
    ).toBeTruthy()
    const card = screen.getByRole("region", { name: "Needs attention" })
    expect(
      within(card).getByRole("heading", { name: "Needs attention · 3+" })
    ).toBeTruthy()
    expect(within(card).getByText("3+ failed documents")).toBeTruthy()
    expect(within(card).getAllByRole("listitem")).toHaveLength(3)
    expect(screen.queryByText("Connecting tool")).toBeNull()
    expect(screen.queryByText("Healthy tool")).toBeNull()
    const agent = screen.getByRole("region", { name: "Your agent" })
    expect(within(agent).getAllByRole("listitem")).toHaveLength(3)
    fireEvent.click(within(agent).getByRole("button", { name: "New chat" }))
    expect(callbacks.onNewChat).toHaveBeenCalledExactlyOnceWith()
    fireEvent.click(within(card).getByRole("button", { name: /Broken tool/ }))
    fireEvent.click(within(card).getByRole("button", { name: /base_1/ }))
    expect(callbacks.onOpenTools).toHaveBeenCalledOnce()
    expect(callbacks.onOpenKnowledge).toHaveBeenCalledOnce()
    const scans = calls.filter((url) => url.pathname.endsWith("/documents"))
    expect(scans).toHaveLength(3)
    for (const scan of scans) {
      expect(scan.searchParams.get("status")).toBe("failed")
      expect(scan.searchParams.get("limit")).toBe("3")
    }
    expect(screen.queryByText(/Nothing waiting/)).toBeNull()
  })

  it("does not claim all clear when a checked base fails or when requests are still loading", async () => {
    let answer!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => {
      answer = resolve
    })
    stubApi({
      [`${path}/agent-requests/inbox`]: () => pending,
      [`${path}/corpora`]: { corpora: [corpus("Private base")] },
      [`${path}/corpora/Private%20base/documents`]: () =>
        json({ error: { code: "forbidden", message: "No access" } }, 403),
    })
    renderHome()
    expect(
      await screen.findByText("Couldn’t load Private base failures.")
    ).toBeTruthy()
    expect(screen.getByText("Loading requests…")).toBeTruthy()
    expect(screen.queryByText(/Nothing waiting/)).toBeNull()
    answer(json({ requests: [] }))
    await waitFor(() =>
      expect(screen.queryByText("Loading requests…")).toBeNull()
    )
    expect(screen.queryByText(/Nothing waiting/)).toBeNull()
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy()
  })

  it("keeps optional unavailable services distinct from denied reads and preserves independent cards", async () => {
    stubApi({
      [`${path}/agent-requests/inbox`]: { requests: [request()] },
      [`${path}/apps/connections`]: () =>
        json({ error: { code: "not_found", message: "Absent" } }, 404),
      [`${path}/corpora`]: () =>
        json({ error: { code: "forbidden", message: "Denied" } }, 403),
    })
    renderHome()
    expect(
      await screen.findByRole("region", { name: "Waiting on you" })
    ).toBeTruthy()
    expect(await screen.findByText("tools is not available here.")).toBeTruthy()
    expect(
      await screen.findByText("Couldn’t load knowledge bases.")
    ).toBeTruthy()
    expect(screen.queryByText(/Nothing waiting/)).toBeNull()
  })

  it("shows one qualified quiet line for a full response and incomplete knowledge coverage", async () => {
    stubApi({
      [`${path}/agent-requests/inbox`]: {
        requests: Array.from({ length: 100 }, (_, i) =>
          request({ id: `done_${i}`, status: "completed" })
        ),
      },
      [`${path}/corpora`]: {
        corpora: Array.from({ length: 5 }, (_, i) => corpus(`base_${i}`)),
      },
      ...Object.fromEntries(
        [0, 1, 2].map((i) => [
          `${path}/corpora/base_${i}/documents`,
          { documents: [] },
        ])
      ),
    })
    renderHome()
    expect(
      await screen.findByText(
        "Nothing waiting in the work checked. Checked 3 of 5 knowledge bases. Only the latest 100 requests were checked."
      )
    ).toBeTruthy()
    expect(screen.getAllByRole("status")).toHaveLength(1)
    expect(screen.queryAllByRole("region")).toHaveLength(0)
  })

  it("keeps preview read-only and fetches no member data", async () => {
    const calls = stubApi()
    renderHome({ organizationId: undefined, viewerId: undefined })
    expect(
      screen
        .getByRole("button", { name: "Send message" })
        .hasAttribute("disabled")
    ).toBe(true)
    expect(screen.getByText("Sign in to start a conversation.")).toBeTruthy()
    expect(calls).toHaveLength(0)
    expect(screen.queryAllByRole("region")).toHaveLength(0)
  })

  it("does not send or imply exact-thread navigation before its supported chat callbacks are wired", async () => {
    stubApi({
      [`${path}/threads`]: {
        threads: [
          {
            id: "old",
            title: "Existing work",
            updated_at: "2026-09-23T10:00:00Z",
          },
        ],
      },
    })
    const callbacks = renderHome({
      onNewChat: undefined,
      onOpenConversation: undefined,
    })
    expect(
      await screen.findByRole("button", { name: /Existing work/ })
    ).toBeTruthy()
    expect(
      screen
        .getByRole("button", { name: /Existing work/ })
        .hasAttribute("disabled")
    ).toBe(true)
    expect(
      screen.getByRole("button", { name: "New chat" }).hasAttribute("disabled")
    ).toBe(true)
    fireEvent.change(
      screen.getByRole("textbox", { name: "Start a new conversation" }),
      { target: { value: "New work" } }
    )
    fireEvent.keyDown(
      screen.getByRole("textbox", { name: "Start a new conversation" }),
      { key: "Enter" }
    )
    expect(
      screen
        .getByRole("button", { name: "Send message" })
        .hasAttribute("disabled")
    ).toBe(true)
    expect(
      screen.getByText("Conversation shortcuts are unavailable.")
    ).toBeTruthy()
    expect(callbacks.onNewChat).not.toHaveBeenCalled()
    expect(callbacks.onOpenConversation).not.toHaveBeenCalled()
  })

  it("drops the old workspace's private rows immediately when the workspace changes", async () => {
    stubApi({
      [`${path}/agent-requests/inbox`]: {
        requests: [request({ purpose: "Private old-workspace decision" })],
      },
      "/v1/organizations/org_2/agent-requests/inbox": { requests: [] },
      "/v1/organizations/org_2/threads": { threads: [] },
      "/v1/organizations/org_2/apps/connections": { connections: [] },
      "/v1/organizations/org_2/corpora": { corpora: [] },
    })
    const view = renderHome()
    expect(
      await screen.findByText("Private old-workspace decision")
    ).toBeTruthy()
    view.rerenderHome({ organizationId: "org_2" })
    expect(screen.queryByText("Private old-workspace decision")).toBeNull()
    expect(
      await screen.findByText("Nothing waiting in the work checked.")
    ).toBeTruthy()
    expect(screen.queryByRole("region", { name: "Waiting on you" })).toBeNull()
  })

  it("fresh-reads every private source after a same-workspace member change, including failed documents", async () => {
    let nextMember = false
    let releaseLists!: () => void
    let releaseDocuments!: () => void
    const listsReady = new Promise<void>((resolve) => {
      releaseLists = resolve
    })
    const documentsReady = new Promise<void>((resolve) => {
      releaseDocuments = resolve
    })
    const oldRows = {
      [`${path}/agent-requests/inbox`]: {
        requests: [request({ purpose: "Private old-member approval" })],
      },
      [`${path}/threads`]: {
        threads: [
          {
            id: "old_thread",
            title: "Private old-member chat",
            updated_at: "",
          },
        ],
      },
      [`${path}/apps/connections`]: {
        connections: [connection("Private old-member tool", "failed")],
      },
      [`${path}/corpora`]: {
        corpora: [{ ...corpus("same_base"), name: "Private old-member base" }],
      },
    }
    const nextRows: Record<string, unknown> = {
      [`${path}/agent-requests/inbox`]: {
        requests: [
          request({
            purpose: "New-member approval",
            target: {
              user_id: "next_member",
              agent_id: "next_agent",
              agent_name: "Next",
            },
          }),
        ],
      },
      [`${path}/threads`]: { threads: [] },
      [`${path}/apps/connections`]: { connections: [] },
      [`${path}/corpora`]: {
        corpora: [{ ...corpus("same_base"), name: "New-member base" }],
      },
    }
    const calls = stubApi({
      ...Object.fromEntries(
        Object.entries(oldRows).map(([url, rows]) => [
          url,
          () =>
            nextMember
              ? listsReady.then(() => json(nextRows[url]))
              : json(rows),
        ])
      ),
      [`${path}/corpora/same_base/documents`]: () =>
        nextMember
          ? documentsReady.then(() => json({ documents: [] }))
          : json({ documents: [failedDocument("old_failure")] }),
    })
    const view = renderHome()
    await screen.findByText("Checked 1 of 1 knowledge bases.")
    expect(screen.getByText("Private old-member chat")).toBeTruthy()
    nextMember = true
    view.rerenderHome({ viewerId: "next_member" })
    expect(screen.queryByText(/Private old-member/)).toBeNull()
    expect(screen.queryByText(/Checked/)).toBeNull()
    expect(screen.queryByText(/Nothing waiting/)).toBeNull()
    await waitFor(() => {
      for (const url of Object.keys(oldRows))
        expect(calls.filter((call) => call.pathname === url)).toHaveLength(2)
    })
    await act(async () => releaseLists())
    await screen.findByText("Loading New-member base failures…")
    expect(screen.queryByText("1 failed document")).toBeNull()
    expect(screen.getByText("Checked 0 of 1 knowledge bases.")).toBeTruthy()
    expect(screen.queryByRole("region", { name: "Needs attention" })).toBeNull()
    await act(async () => releaseDocuments())
    await screen.findByText("Checked 1 of 1 knowledge bases.")
    expect(
      calls.filter((call) => call.pathname.endsWith("/documents"))
    ).toHaveLength(2)
    expect(screen.queryByText(/Private old-member/)).toBeNull()
  })

  it.each([
    "requests",
    "agent work",
    "tools",
    "knowledge bases",
    "Base failures",
  ])(
    "removes cached %s choices and counts during a refresh and after a refused read",
    async (source) => {
      const sources = {
        requests: {
          route: `${path}/agent-requests/inbox`,
          key: agentKeys.requestBox("org_1", "inbox", 100),
          label: "Private approval",
        },
        "agent work": {
          route: `${path}/threads`,
          key: threadKeys.list("org_1"),
          label: "Private chat",
        },
        tools: {
          route: `${path}/apps/connections`,
          key: connectionsQueryOptions("org_1").queryKey,
          label: "Private tool",
        },
        "knowledge bases": {
          route: `${path}/corpora`,
          key: ragKeys.corpora("org_1"),
          label: "Base",
        },
        "Base failures": {
          route: `${path}/corpora/base/documents`,
          key: ragKeys.documents("org_1", "base"),
          label: "Base",
        },
      }
      const selected = sources[source as keyof typeof sources]
      let refresh = false
      let release!: () => void
      const ready = new Promise<void>((resolve) => {
        release = resolve
      })
      const rows: Record<string, unknown> = {
        [`${path}/agent-requests/inbox`]: {
          requests: [request({ purpose: "Private approval" })],
        },
        [`${path}/threads`]: {
          threads: [
            { id: "private_chat", title: "Private chat", updated_at: "" },
          ],
        },
        [`${path}/apps/connections`]: {
          connections: [connection("Private tool", "failed")],
        },
        [`${path}/corpora`]: { corpora: [{ ...corpus("base"), name: "Base" }] },
        [`${path}/corpora/base/documents`]: {
          documents: [failedDocument("private_failure")],
        },
      }
      stubApi(
        Object.fromEntries(
          Object.entries(rows).map(([url, data]) => [
            url,
            () =>
              refresh && url === selected.route
                ? ready.then(() =>
                    json(
                      {
                        error: {
                          code: "forbidden",
                          message: "Private diagnostic",
                        },
                      },
                      403
                    )
                  )
                : json(data),
          ])
        )
      )
      const view = renderHome()
      await screen.findByText("Checked 1 of 1 knowledge bases.")
      expect(
        screen.getByRole("button", { name: new RegExp(selected.label) })
      ).toBeTruthy()
      refresh = true
      let refetch!: Promise<void>
      await act(async () => {
        refetch = view.client.refetchQueries({ queryKey: selected.key })
      })
      await screen.findByText(`Loading ${source}…`)
      expect(
        screen.queryByRole("button", { name: new RegExp(selected.label) })
      ).toBeNull()
      expect(screen.queryByText(/Nothing waiting/)).toBeNull()
      if (source === "knowledge bases")
        expect(screen.queryByText(/Checked/)).toBeNull()
      if (source === "Base failures") {
        expect(screen.queryByText("1 failed document")).toBeNull()
        expect(screen.getByText("Checked 0 of 1 knowledge bases.")).toBeTruthy()
      }
      await act(async () => {
        release()
        await refetch
      })
      await screen.findByText(`Couldn’t load ${source}.`)
      expect(
        screen.queryByRole("button", { name: new RegExp(selected.label) })
      ).toBeNull()
      expect(screen.queryByText(/Nothing waiting/)).toBeNull()
      expect(screen.queryByText("Private diagnostic")).toBeNull()
      if (source === "knowledge bases")
        expect(screen.queryByText(/Checked/)).toBeNull()
      if (source === "Base failures") {
        expect(screen.queryByText("1 failed document")).toBeNull()
        expect(screen.getByText("Checked 0 of 1 knowledge bases.")).toBeTruthy()
      }
    }
  )

  it("removes a previously readable tool after a refresh refuses access", async () => {
    let refused = false
    stubApi({
      [`${path}/apps/connections`]: () =>
        refused
          ? json({ error: { code: "forbidden" } }, 403)
          : json({
              connections: [connection("Private cached tool", "failed")],
            }),
    })
    const view = renderHome()
    await screen.findByRole("button", { name: /Private cached tool/ })
    refused = true
    await act(async () => {
      await view.client.refetchQueries({
        queryKey: connectionsQueryOptions("org_1").queryKey,
      })
    })
    await screen.findByText("Couldn’t load tools.")
    expect(
      screen.queryByRole("button", { name: /Private cached tool/ })
    ).toBeNull()
    expect(screen.queryByText(/Nothing waiting/)).toBeNull()
  })
})
