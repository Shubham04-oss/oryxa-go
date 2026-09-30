// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import {
  focusManager,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query"
import type { ComponentProps } from "react"
import { agentKeys } from "@/lib/agents/queries"
import { threadKeys } from "@/lib/threads/client"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import type { ActiveAgentLease } from "@/lib/home/client"
import { HomeOverview } from "./home-overview"
import {
  ASKER,
  OWNER,
  ORG,
  NOW,
  agentRequest,
  answer,
  members,
  slackRequest,
} from "./request-card-fixtures"

vi.mock("thinking-orbs", () => ({
  ThinkingOrb: ({ state }: { state: string }) => (
    <i aria-hidden="true" data-orb-state={state} />
  ),
}))
const path = `/v1/organizations/${ORG}`
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
const refused = () =>
  json({ error: { code: "forbidden", message: "private diagnostic" } }, 403)
const request = (overrides: Partial<AgentRequest> = {}) =>
  agentRequest({
    expires_at: new Date(NOW + 3600_000).toISOString(),
    ...overrides,
  })
const chat = (id: string, title = id, origin?: string) => ({
  id,
  title,
  origin,
  updated_at: new Date(NOW).toISOString(),
})
const activeLease = (
  id = "lease-1",
  overrides: Partial<ActiveAgentLease> = {}
): ActiveAgentLease => ({
  id,
  organization_id: ORG,
  owner_user_id: OWNER,
  asker_user_id: ASKER,
  channel_id: "channel",
  thread_ts: "123.4",
  created_at: new Date(NOW).toISOString(),
  expires_at: new Date(NOW + 3600_000).toISOString(),
  role: "owner",
  can_revoke: true,
  ...overrides,
})
const page = (leases: ActiveAgentLease[] = [], extra: object = {}) => ({
  leases,
  observed_at: new Date(NOW).toISOString(),
  has_more: false,
  ...extra,
})
const base = { id: "base", name: "Knowledge", document_count: 1 }
type Route =
  unknown | ((url: URL, init?: RequestInit) => Response | Promise<Response>)

function stubApi(overrides: Record<string, Route> = {}) {
  const routes: Record<string, Route> = {
    [`${path}/agent-requests/inbox`]: { requests: [] },
    [`${path}/agent-requests/sent`]: { requests: [] },
    [`${path}/agent-requests/activity`]: { activity: [] },
    [`${path}/agent-leases`]: page(),
    [`${path}/threads`]: { threads: [] },
    [`${path}/corpora`]: { corpora: [base] },
    [`${path}/apps/slack/status`]: { status: "active", can_manage: false },
    [`${path}/members`]: { members },
    "/v1/agent-grant": { granted: true },
    ...overrides,
  }
  const calls: { url: URL; method: string; body?: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      calls.push({
        url,
        method: init?.method ?? "GET",
        body:
          typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      })
      const value = routes[url.pathname]
      if (typeof value === "function") return value(url, init)
      if (value !== undefined) return json(value)
      if (url.pathname.startsWith(`${path}/threads/`))
        return json({
          ...chat(url.pathname.split("/").at(-1)!),
          items: [],
          pending_confirmations: [],
        })
      return json({ error: { code: "not_found", message: "No route" } }, 404)
    })
  )
  return { calls, routes }
}

const clients: QueryClient[] = []
function renderHome(
  overrides: Partial<ComponentProps<typeof HomeOverview>> = {}
) {
  const callbacks = {
    onNewChat: vi.fn(),
    onOpenRequests: vi.fn(),
    onOpenHistory: vi.fn(),
    onOpenConversation: vi.fn(),
    onOpenTools: vi.fn(),
    onOpenKnowledge: vi.fn(),
    onOpenSlack: vi.fn(),
    onOpenAccount: vi.fn(),
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const props = {
    organizationId: ORG,
    viewerId: OWNER,
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

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(NOW)
})
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  focusManager.setFocused(undefined)
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

it("names the member's agent over the composer, and starts a conversation from it without listing conversations of its own", async () => {
  stubApi({
    [`${path}/threads`]: {
      threads: [chat("one", " First "), chat("two", "Second")],
    },
  })
  const view = renderHome({
    agent: { id: "agt_krude", name: "Krude", status: "active" },
  })
  const agent = screen.getByRole("region", { name: "Your agent" })
  expect(within(agent).getByRole("heading", { name: "Krude" })).toBeTruthy()
  expect(agent.getAttribute("data-screen-context-kind")).toBe("primary_agent")
  expect(agent.getAttribute("data-screen-context-id")).toBe("agt_krude")
  await screen.findByText("Nothing needs you right now.")
  // The sidebar's Recent list has the conversations and New chat: the desk
  // lists none, and offers neither New chat nor All of its own.
  expect(screen.queryByText("Second")).toBeNull()
  expect(within(agent).queryByRole("list")).toBeNull()
  expect(within(agent).queryByRole("button", { name: "New chat" })).toBeNull()
  expect(
    within(agent).queryByRole("button", { name: "All conversations" })
  ).toBeNull()
  // Nothing is paused: no word of it.
  expect(within(agent).queryByText(/paused/)).toBeNull()

  fireEvent.change(
    within(agent).getByRole<HTMLTextAreaElement>("textbox", {
      name: "Start a new conversation",
    }),
    { target: { value: "  Check the launch plan  " } }
  )
  fireEvent.click(
    within(agent).getByRole<HTMLButtonElement>("button", {
      name: "Send message",
    })
  )
  expect(view.onNewChat).toHaveBeenCalledWith("Check the launch plan")
  expect(within(agent).getByRole<HTMLTextAreaElement>("textbox").value).toBe("")
})

it("calls the agent 'Your agent' until the member's agents have loaded", () => {
  stubApi()
  renderHome()
  expect(
    within(screen.getByRole("region", { name: "Your agent" })).getByRole(
      "heading",
      { name: "Your agent" }
    )
  ).toBeTruthy()
})

it("is the primary chat's blank state when given one: its draft, its send, its unsent messages and why it left a conversation", async () => {
  stubApi()
  const blank = {
    draft: "Typed in a conversation",
    onDraftChange: vi.fn(),
    onSubmit: vi.fn(),
    onComposingChange: vi.fn(),
    notSent: <p data-status="not-sent">Lost words</p>,
    notice: "That conversation no longer exists.",
  }
  const view = renderHome({ chat: blank, onNewChat: undefined })
  const box = screen.getByRole<HTMLTextAreaElement>("textbox", {
    name: "Start a new conversation",
  })
  // One draft with the conversations' composer: the chat keeps it.
  expect(box.value).toBe("Typed in a conversation")
  fireEvent.change(box, { target: { value: "Something else" } })
  expect(blank.onDraftChange).toHaveBeenCalledWith("Something else")
  fireEvent.focus(box)
  expect(blank.onComposingChange).toHaveBeenLastCalledWith(true)
  fireEvent.blur(box)
  expect(blank.onComposingChange).toHaveBeenLastCalledWith(false)
  fireEvent.click(screen.getByRole("button", { name: "Send message" }))
  expect(blank.onSubmit).toHaveBeenCalledExactlyOnceWith(
    "Typed in a conversation"
  )
  expect(view.onNewChat).not.toHaveBeenCalled()
  expect(
    within(screen.getByRole("log", { name: "Messages not sent" })).getByText(
      "Lost words"
    )
  ).toBeTruthy()
  expect(screen.getByRole("alert").textContent).toBe(
    "That conversation no longer exists."
  )
})

it("says when the member's agent is paused, and resumes it", async () => {
  const krude = {
    id: "agt_krude",
    organization_id: ORG,
    owner_user_id: OWNER,
    kind: "primary",
    name: "Krude",
    description: "",
    instructions: "",
    status: "active",
    revision: 2,
    created_at: new Date(NOW).toISOString(),
    updated_at: new Date(NOW).toISOString(),
  }
  const go = stubApi({ [`${path}/agents/agt_krude`]: () => json(krude) })
  renderHome({ agent: { id: "agt_krude", name: "Krude", status: "disabled" } })
  const agent = screen.getByRole("region", { name: "Your agent" })
  expect(within(agent).getByText("Krude is paused.")).toBeTruthy()
  fireEvent.click(within(agent).getByRole("button", { name: "Resume Krude" }))
  await waitFor(() =>
    expect(
      go.calls.find(
        (call) =>
          call.method === "PATCH" &&
          call.url.pathname === `${path}/agents/agt_krude`
      )?.body
    ).toEqual({ status: "active" })
  )
})

it("uses real inline consent and held answer cards, followed by access, sent/shared answers and unfinished setup", async () => {
  const consent = slackRequest({
    expires_at: new Date(NOW + 3600_000).toISOString(),
  })
  const held = request({
    id: "held",
    status: "review",
    task: "Review this answer",
    held: {
      result: answer("Held answer with personal data"),
      masked_result: answer("Masked answer"),
    },
    guard: { state: "held", kinds: ["EMAIL"] },
  })
  stubApi({
    [`${path}/agent-requests/inbox`]: {
      requests: [
        consent,
        held,
        request({
          id: "shared",
          status: "completed",
          task: "What you shared",
          result: answer("The agent shared this answer"),
        }),
      ],
    },
    [`${path}/agent-requests/sent`]: {
      requests: [
        request({
          id: "sent",
          requester: consent.target,
          target: consent.requester,
          status: "completed",
          task: "What you asked",
          result: answer("An answer to your request"),
        }),
      ],
    },
    [`${path}/agent-leases`]: page([activeLease()]),
    [`${path}/apps/slack/status`]: {
      status: "not_installed",
      can_manage: true,
    },
    [`${path}/corpora`]: { corpora: [] },
    "/v1/agent-grant": { granted: false },
  })
  const view = renderHome()
  const needs = await screen.findByRole("region", { name: "Needs you" })
  // The same three words as the Requests card and Slack's notice.
  expect(
    within(needs).getByRole<HTMLButtonElement>("button", {
      name: "Share answer to Raya's request",
    }).textContent
  ).toContain("Share answer")
  expect(
    within(needs).getByRole<HTMLButtonElement>("button", {
      name: "Allow for 1 hour for Raya's request",
    }).textContent
  ).toContain("Allow for 1 hour")
  expect(
    within(needs).getByRole<HTMLButtonElement>("button", {
      name: "Deny Raya's request",
    }).textContent
  ).toContain("Deny")
  expect(needs.textContent).not.toMatch(/Summary only|Approve|Decline/)
  expect(within(needs).getByText("Held answer with personal data")).toBeTruthy()
  expect(
    within(needs).getByRole<HTMLButtonElement>("button", {
      name: /Send .*masked/,
    })
  ).toBeTruthy()
  await screen.findByText("An answer to your request")
  const sections = [...view.container.querySelectorAll(".home-card")].map(
    (node) => node.getAttribute("aria-label")
  )
  expect(sections).toEqual([
    "Your agent",
    "Needs you",
    "Open access",
    "Your requests and shared answers",
    "Finish setup",
  ])
  expect(screen.getByText("The agent shared this answer")).toBeTruthy()
  fireEvent.click(
    screen.getByRole<HTMLButtonElement>("button", { name: "Connect" })
  )
  expect(view.onOpenSlack).toHaveBeenCalledOnce()
  fireEvent.click(
    screen.getByRole<HTMLButtonElement>("button", { name: "Add" })
  )
  expect(view.onOpenKnowledge).toHaveBeenCalledOnce()
  fireEvent.click(
    screen.getByRole<HTMLButtonElement>("button", { name: "Open Account" })
  )
  expect(view.onOpenAccount).toHaveBeenCalledOnce()
})

it("posts a Home consent choice through the existing guarded request action", async () => {
  const pending = slackRequest({
    expires_at: new Date(NOW + 3600_000).toISOString(),
  })
  let current = pending
  const go = stubApi({
    [`${path}/agent-requests/inbox`]: () => json({ requests: [current] }),
    [`${path}/agent-requests/${pending.id}/consent`]: () => {
      current = { ...pending, status: "rejected", revision: 2 }
      return json({ request: current })
    },
  })
  renderHome()
  fireEvent.click(await screen.findByRole("button", { name: /Deny/ }))
  await waitFor(() =>
    expect(screen.queryByRole("region", { name: "Needs you" })).toBeNull()
  )
  expect(go.calls.find((call) => call.method === "POST")?.body).toEqual({
    choice: "deny",
  })
})

it("offers a request an agent filed in Oryxa two choices, Share answer and Deny, and no approval without tools", async () => {
  const filed = request({ id: "irq_filed" })
  let current = filed
  const go = stubApi({
    [`${path}/agent-requests/inbox`]: () => json({ requests: [current] }),
    [`${path}/agent-requests/${filed.id}/decision`]: () => {
      current = { ...filed, status: "approved", revision: 2 }
      return json(current)
    },
  })
  renderHome()

  const needs = await screen.findByRole("region", { name: "Needs you" })
  const choices = await within(needs).findAllByRole("button", {
    name: /Share answer|Deny|without tools|Approve|Decline/,
  })
  expect(choices.map((choice) => choice.textContent)).toEqual([
    "Deny",
    "Share answer",
  ])
  expect(needs.textContent).not.toMatch(/without tools|can’t look anything up/)
  fireEvent.click(
    within(needs).getByRole("button", {
      name: "Share answer to Raya's request",
    })
  )
  await waitFor(() =>
    expect(screen.queryByRole("region", { name: "Needs you" })).toBeNull()
  )
  expect(go.calls.find((call) => call.method === "POST")?.body).toEqual({
    decision: "approved",
    message: "Approved in Oryxa.",
    allowed_tools: ["knowledge"],
  })
})

it("keeps only this member's unexpired owner decisions and held reviews; bounds cards with All requests", async () => {
  stubApi({
    [`${path}/agent-requests/inbox`]: {
      requests: [
        request({
          id: "foreign",
          target: {
            user_id: "other",
            agent_id: "other-agent",
            agent_name: "Other",
          },
          task: "Foreign owner",
        }),
        request({ id: "agent", approval: "agent", task: "Agent handles this" }),
        request({
          id: "expired",
          expires_at: new Date(NOW - 1).toISOString(),
          task: "Expired request",
        }),
        ...[1, 2, 3, 4].map((id) =>
          request({ id: `own-${id}`, task: `Owner request ${id}` })
        ),
      ],
    },
  })
  const view = renderHome()
  const needs = await screen.findByRole("region", { name: "Needs you" })
  expect(within(needs).getAllByRole("article")).toHaveLength(3)
  expect(screen.queryByText("Foreign owner")).toBeNull()
  expect(screen.queryByText("Agent handles this")).toBeNull()
  expect(screen.queryByText("Expired request")).toBeNull()
  expect(
    screen.getByText("1 more request needs you. Open All requests to decide.")
  ).toBeTruthy()
  fireEvent.click(
    within(needs).getByRole<HTMLButtonElement>("button", {
      name: "All requests",
    })
  )
  expect(view.onOpenRequests).toHaveBeenCalledWith()
})

it("keeps a card's decision note and composer draft mounted during a background refresh", async () => {
  let finish: ((response: Response) => void) | undefined
  let refreshing = false
  stubApi({
    [`${path}/agent-requests/inbox`]: () =>
      refreshing
        ? new Promise<Response>((resolve) => {
            finish = resolve
          })
        : json({ requests: [request()] }),
  })
  const view = renderHome()
  fireEvent.click(await screen.findByRole("button", { name: "Add a note" }))
  const note = screen.getByRole<HTMLInputElement>("textbox", {
    name: /A note for Raya/,
  })
  fireEvent.change(note, {
    target: { value: "Please use the public launch notes" },
  })
  const composer = screen.getByRole<HTMLTextAreaElement>("textbox", {
    name: "Start a new conversation",
  })
  fireEvent.change(composer, { target: { value: "Unsent question" } })
  refreshing = true
  let refetch: Promise<void> | undefined
  act(() => {
    refetch = view.client.invalidateQueries({
      queryKey: agentKeys.requestBox(ORG, "inbox"),
    })
  })
  await waitFor(() => expect(finish).toBeTypeOf("function"))
  expect(
    screen.getByRole<HTMLTextAreaElement>("textbox", {
      name: /A note for Raya/,
    })
  ).toBe(note)
  expect(note.value).toBe("Please use the public launch notes")
  await act(async () => {
    finish!(json({ requests: [request()] }))
    await refetch
  })
  expect(
    screen.getByRole<HTMLTextAreaElement>("textbox", {
      name: /A note for Raya/,
    })
  ).toBe(note)
  expect(composer.value).toBe("Unsent question")
})

it("checks only newest five primary chats plus the distinct open chat and opens exact confirmations", async () => {
  const go = stubApi({
    [`${path}/threads`]: {
      threads: [
        chat("wake", "Wake", "wakeup"),
        ...Array.from({ length: 12 }, (_, i) => chat(`chat-${i}`)),
      ],
    },
    [`${path}/threads/chat-1`]: {
      ...chat("chat-1", "Needs a tool choice"),
      items: [],
      pending_confirmations: ["a", "b"],
    },
    [`${path}/threads/chat-10`]: {
      ...chat("chat-10", "Open older chat"),
      items: [],
      pending_confirmations: ["c"],
    },
  })
  const view = renderHome({
    primaryChatActivity: { threadId: "chat-10", state: "idle" },
  })
  await screen.findByText("2 tool confirmations · Open chat to decide")
  expect(
    screen.getByText(/limited to your five newest chats and the open chat/)
  ).toBeTruthy()
  const details = go.calls
    .filter((call) => call.url.pathname.startsWith(`${path}/threads/`))
    .map((call) => call.url.pathname.split("/").at(-1))
  expect(details.sort()).toEqual(
    ["chat-0", "chat-1", "chat-2", "chat-3", "chat-4", "chat-10"].sort()
  )
  expect(screen.queryByText("Nothing needs you right now.")).toBeNull()
  fireEvent.click(
    screen.getByRole<HTMLButtonElement>("button", { name: /Open older chat/ })
  )
  expect(view.onOpenConversation).toHaveBeenCalledWith("chat-10")
})

it("polls bounded chat details every thirty seconds while lists stay quick and mount and focus refresh immediately", async () => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] })
  const go = stubApi({
    [`${path}/threads`]: {
      threads: Array.from({ length: 6 }, (_, i) => chat(`chat-${i}`)),
    },
  })
  const activity = { threadId: "chat-5", state: "idle" as const }
  const view = renderHome({ primaryChatActivity: activity })
  const detailReads = () =>
    go.calls.filter((call) => call.url.pathname.startsWith(`${path}/threads/`))
  const listReads = () =>
    go.calls.filter((call) => call.url.pathname === `${path}/threads`)
  await screen.findByText("Nothing needs you right now.")
  expect(detailReads()).toHaveLength(6)
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10_000)
  })
  expect(listReads()).toHaveLength(2)
  expect(detailReads()).toHaveLength(6)
  await act(async () => {
    await vi.advanceTimersByTimeAsync(19_999)
  })
  expect(detailReads()).toHaveLength(6)
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1)
  })
  expect(detailReads()).toHaveLength(12)

  view.unmount()
  render(
    <QueryClientProvider client={view.client}>
      <HomeOverview
        organizationId={ORG}
        viewerId={OWNER}
        primaryChatActivity={activity}
        onOpenRequests={vi.fn()}
        onOpenHistory={vi.fn()}
        onOpenTools={vi.fn()}
        onOpenKnowledge={vi.fn()}
      />
    </QueryClientProvider>
  )
  await screen.findByText("Nothing needs you right now.")
  expect(detailReads()).toHaveLength(18)
  await act(async () => {
    focusManager.setFocused(false)
    focusManager.setFocused(true)
  })
  await waitFor(() => expect(detailReads()).toHaveLength(24))
})

it("deduplicates an open chat already among five and never reads side-conversation histories", async () => {
  const go = stubApi({
    [`${path}/threads`]: {
      threads: [
        chat("side", "Side", "side_conversation"),
        ...[1, 2, 3, 4, 5].map((i) => chat(String(i))),
      ],
    },
  })
  renderHome({ primaryChatActivity: { state: "idle", threadId: "2" } })
  await screen.findByText("Nothing needs you right now.")
  expect(
    go.calls.filter((call) => call.url.pathname.startsWith(`${path}/threads/`))
  ).toHaveLength(5)
})

it("keeps known live confirmation reachable after a refused detail refresh while cached confirmations disappear", async () => {
  let deny = false
  stubApi({
    [`${path}/threads`]: {
      threads: [
        chat("stored", "Stored confirmation"),
        chat("live", "Open chat"),
      ],
    },
    [`${path}/threads/stored`]: () =>
      deny
        ? refused()
        : json({
            ...chat("stored", "Stored confirmation"),
            items: [],
            pending_confirmations: ["a"],
          }),
    [`${path}/threads/live`]: () =>
      deny
        ? refused()
        : json({
            ...chat("live", "Open chat"),
            items: [],
            pending_confirmations: ["b"],
          }),
  })
  const view = renderHome({
    primaryChatActivity: { state: "waiting", threadId: "live" },
  })
  await screen.findByRole("list", { name: "Tool confirmations" })
  deny = true
  await act(async () => {
    await view.client.invalidateQueries({ queryKey: threadKeys.all(ORG) })
  })
  const needs = screen.getByRole("region", { name: "Needs you" })
  expect(within(needs).queryByText("Stored confirmation")).toBeNull()
  fireEvent.click(
    within(needs).getByRole<HTMLButtonElement>("button", { name: /Open chat/ })
  )
  expect(view.onOpenConversation).toHaveBeenCalledWith("live")
  expect(screen.queryByText("Nothing needs you right now.")).toBeNull()
  expect(screen.queryByText("private diagnostic")).toBeNull()
})

it("never claims all clear when history coverage is partial, loading, or refused", async () => {
  stubApi({
    [`${path}/threads`]: {
      threads: Array.from({ length: 8 }, (_, i) => chat(String(i))),
    },
    [`${path}/threads/1`]: refused,
  })
  renderHome()
  await screen.findByText("Couldn’t load tool confirmations.")
  expect(screen.queryByText("Nothing needs you right now.")).toBeNull()
  expect(screen.getByText(/Older chats may still need you/)).toBeTruthy()
})

it("gives quiet complete empty Home exactly once, with only unfinished setup", async () => {
  stubApi({
    [`${path}/corpora`]: { corpora: [{ ...base, document_count: 0 }] },
    [`${path}/apps/slack/status`]: { status: "ended", can_manage: false },
    "/v1/agent-grant": { granted: false },
  })
  renderHome()
  expect(
    await screen.findAllByText("Nothing needs you right now.")
  ).toHaveLength(1)
  const setup = screen.getByRole("region", { name: "Set up your workspace" })
  expect(
    within(setup).getByText("Ask a workspace admin to connect Slack.")
  ).toBeTruthy()
  expect(within(setup).queryByRole("button", { name: "Connect" })).toBeNull()
  expect(within(setup).getByText("Add knowledge")).toBeTruthy()
  expect(within(setup).getByText("Enable background access")).toBeTruthy()
  expect(screen.queryByRole("region", { name: "Needs you" })).toBeNull()
  expect(screen.queryByRole("region", { name: "Open access" })).toBeNull()
})

it("hides completed setup and does not create Slack authorization URLs", async () => {
  const go = stubApi()
  renderHome()
  await screen.findByText("Nothing needs you right now.")
  expect(screen.queryByText("Set up your workspace")).toBeNull()
  expect(
    go.calls.some(
      (call) =>
        call.url.pathname.includes("slack/install") ||
        call.url.pathname.endsWith("authorize")
    )
  ).toBe(false)
})

it("exposes End access only on owned revocable active windows and settles the server winning lease", async () => {
  const owned = activeLease()
  let revoked = false
  const go = stubApi({
    [`${path}/agent-leases`]: () =>
      json(
        page(
          revoked
            ? [
                activeLease("received", {
                  role: "asker",
                  can_revoke: false,
                  owner_user_id: ASKER,
                  asker_user_id: OWNER,
                }),
              ]
            : [
                owned,
                activeLease("received", {
                  role: "asker",
                  can_revoke: false,
                  owner_user_id: ASKER,
                  asker_user_id: OWNER,
                }),
                activeLease("expired", {
                  expires_at: new Date(NOW - 1).toISOString(),
                }),
                activeLease("foreign", {
                  owner_user_id: "stranger",
                  asker_user_id: "someone",
                }),
              ]
        )
      ),
    [`${path}/agent-leases/${owned.id}`]: () => {
      revoked = true
      return json({
        lease: { ...owned, revoked_at: new Date(NOW).toISOString() },
      })
    },
  })
  renderHome()
  const section = await screen.findByRole("region", { name: "Open access" })
  await within(section).findByText("Raya Patel can ask your agent")
  expect(
    within(section).getAllByRole("button", { name: "End access" })
  ).toHaveLength(1)
  expect(within(section).getAllByRole("listitem")).toHaveLength(2)
  fireEvent.click(
    within(section).getByRole<HTMLButtonElement>("button", {
      name: "End access",
    })
  )
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "End access" })).toBeNull()
  )
  expect(go.calls.find((call) => call.method === "DELETE")?.url.pathname).toBe(
    `${path}/agent-leases/${owned.id}`
  )
})

it("makes every live access page reachable without pretending a first page is complete", async () => {
  const go = stubApi({
    [`${path}/agent-leases`]: (url: URL) =>
      json(
        url.searchParams.has("cursor")
          ? page([activeLease("lease-5")])
          : page(
              [1, 2, 3, 4].map((i) => activeLease(`lease-${i}`)),
              { has_more: true, next_cursor: "opaque+cursor/" }
            )
      ),
  })
  renderHome()
  const section = await screen.findByRole("region", { name: "Open access" })
  expect(within(section).getAllByRole("listitem")).toHaveLength(3)
  fireEvent.click(
    within(section).getByRole<HTMLButtonElement>("button", {
      name: "All access",
    })
  )
  expect(within(section).getAllByRole("listitem")).toHaveLength(4)
  fireEvent.click(
    within(section).getByRole<HTMLButtonElement>("button", {
      name: "Load more access",
    })
  )
  await waitFor(() =>
    expect(within(section).getAllByRole("listitem")).toHaveLength(5)
  )
  expect(
    go.calls.some(
      (call) => call.url.searchParams.get("cursor") === "opaque+cursor/"
    )
  ).toBe(true)
  expect(
    within(section).queryByRole("button", { name: "Load more access" })
  ).toBeNull()
})

it("uses server observation to keep live windows despite a member clock ahead", async () => {
  vi.setSystemTime(NOW + 86400_000)
  stubApi({ [`${path}/agent-leases`]: page([activeLease()]) })
  renderHome()
  await screen.findByRole("button", { name: "End access" })
})

it("removes access when server-relative time expires while the page remains open", async () => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] })
  stubApi({
    [`${path}/agent-leases`]: page([
      activeLease("short", { expires_at: new Date(NOW + 2000).toISOString() }),
    ]),
  })
  renderHome()
  await screen.findByRole("button", { name: "End access" })
  act(() => {
    vi.advanceTimersByTime(3000)
  })
  expect(screen.queryByRole("region", { name: "Open access" })).toBeNull()
})

it("includes actual shared Activity without duplicating shown answers, withholding held work from shared rows", async () => {
  stubApi({
    [`${path}/agent-requests/inbox`]: {
      requests: [
        request({
          id: "shared",
          status: "completed",
          result: answer("Shared answer"),
        }),
      ],
    },
    [`${path}/agent-requests/activity`]: {
      activity: [
        {
          kind: "answered",
          role: "owner",
          request_id: "shared",
          status: "completed",
          with_user_id: ASKER,
          at: new Date(NOW).toISOString(),
        },
        {
          kind: "answered",
          role: "owner",
          request_id: "older",
          status: "completed",
          with_user_id: ASKER,
          at: new Date(NOW).toISOString(),
        },
        {
          kind: "answered",
          role: "owner",
          request_id: "held",
          status: "review",
          with_user_id: ASKER,
          at: new Date(NOW).toISOString(),
        },
      ],
    },
  })
  const view = renderHome()
  await screen.findByText("Shared answer")
  const section = screen.getByRole("region", {
    name: "Your requests and shared answers",
  })
  const activity = await within(section).findByRole("button", {
    name: /Your agent answered Raya/,
  })
  expect(
    within(section).getAllByRole("button", { name: /Open request/ })
  ).toHaveLength(1)
  fireEvent.click(activity)
  expect(view.onOpenRequests).toHaveBeenCalledWith("older")
})

it("does not claim quiet when the request inbox reaches its server limit", async () => {
  stubApi({
    [`${path}/agent-requests/inbox`]: {
      requests: Array.from({ length: 100 }, (_, i) =>
        request({ id: `old-${i}`, status: "rejected" })
      ),
    },
  })
  renderHome()
  await screen.findByText("Only the latest 100 requests were checked.")
  expect(screen.queryByText("Nothing needs you right now.")).toBeNull()
})

it("keeps preview read-only and does not fetch private APIs", async () => {
  const go = stubApi()
  renderHome({ organizationId: undefined, viewerId: undefined })
  expect(screen.getByText("Sign in to start a conversation.")).toBeTruthy()
  expect(
    screen.getByRole<HTMLButtonElement>("button", { name: "Send message" })
      .disabled
  ).toBe(true)
  await act(async () => {})
  expect(go.calls).toHaveLength(0)
  expect(screen.queryByText("Nothing needs you right now.")).toBeNull()
})

it("leaves unsupported conversation callbacks visibly disabled and drafts editable", async () => {
  stubApi({
    [`${path}/threads`]: { threads: [chat("one", "Waiting chat")] },
    [`${path}/threads/one`]: {
      ...chat("one", "Waiting chat"),
      items: [],
      pending_confirmations: ["a"],
    },
  })
  renderHome({ onOpenConversation: undefined, onNewChat: undefined })
  await screen.findByRole("button", { name: /Waiting chat/ })
  expect(
    screen.getByRole<HTMLButtonElement>("button", { name: /Waiting chat/ })
      .disabled
  ).toBe(true)
  expect(
    screen.getByText("Conversation shortcuts are unavailable.")
  ).toBeTruthy()
  fireEvent.change(screen.getByRole<HTMLTextAreaElement>("textbox"), {
    target: { value: "An unsent question" },
  })
  expect(screen.getByRole<HTMLTextAreaElement>("textbox").value).toBe(
    "An unsent question"
  )
})

it.each([
  {
    name: "requests",
    route: `${path}/agent-requests/inbox`,
    seed: { requests: [request({ task: "Private pending task" })] },
    privateText: "Private pending task",
    key: agentKeys.requestBox(ORG, "inbox"),
  },
  {
    name: "your requests",
    route: `${path}/agent-requests/sent`,
    seed: {
      requests: [
        request({
          requester: { user_id: OWNER, agent_id: "own", agent_name: "Krude" },
          target: { user_id: ASKER, agent_id: "peer", agent_name: "Nia" },
          task: "Private outgoing task",
        }),
      ],
    },
    privateText: "Private outgoing task",
    key: agentKeys.requestBox(ORG, "sent"),
  },
  {
    // The desk names a conversation only where it waits on the member.
    name: "conversations",
    route: `${path}/threads`,
    seed: { threads: [chat("private", "Private recent title")] },
    extra: {
      [`${path}/threads/private`]: {
        ...chat("private", "Private recent title"),
        items: [],
        pending_confirmations: ["a"],
      },
    },
    privateText: "Private recent title",
    key: threadKeys.list(ORG),
  },
  {
    name: "open access",
    route: `${path}/agent-leases`,
    seed: page([activeLease()]),
    privateText: "Raya Patel can ask your agent",
    key: [...agentKeys.requests(ORG), "active-leases"],
  },
  {
    name: "Slack status",
    route: `${path}/apps/slack/status`,
    seed: { status: "not_installed", can_manage: true },
    privateText: "Connect Slack",
    key: ["apps", ORG],
  },
  {
    name: "background access",
    route: "/v1/agent-grant",
    seed: { granted: false },
    privateText: "Enable background access",
    key: ["auth", "agent-grant"],
  },
])(
  "removes private $name after a refused refresh, preserving the unsent composer",
  async ({ name, route, seed, privateText, key, ...rest }) => {
    let deny = false
    const extra = "extra" in rest ? rest.extra : {}
    stubApi({ ...extra, [route]: () => (deny ? refused() : json(seed)) })
    const view = renderHome()
    await screen.findByText(privateText)
    const field = screen.getByRole<HTMLTextAreaElement>("textbox", {
      name: "Start a new conversation",
    })
    fireEvent.change(field, { target: { value: "Keep this draft" } })
    deny = true
    await act(async () => {
      await view.client.invalidateQueries({ queryKey: key })
    })
    await screen.findByText(`Couldn’t load ${name}.`)
    expect(screen.queryByText(privateText)).toBeNull()
    expect(field.value).toBe("Keep this draft")
    expect(screen.queryByText("Nothing needs you right now.")).toBeNull()
    expect(screen.queryByText("private diagnostic")).toBeNull()
  }
)

it("drops prior viewer work and drafts immediately and freshly authorizes all private caches for the next viewer", async () => {
  let nextViewer = false
  let finish: ((response: Response) => void) | undefined
  const go = stubApi({
    [`${path}/agent-requests/inbox`]: () =>
      nextViewer
        ? new Promise<Response>((resolve) => {
            finish = resolve
          })
        : json({ requests: [request({ task: "Old viewer private task" })] }),
  })
  const view = renderHome()
  await screen.findByText("Old viewer private task")
  fireEvent.change(
    screen.getByRole<HTMLTextAreaElement>("textbox", {
      name: "Start a new conversation",
    }),
    { target: { value: "Old viewer draft" } }
  )
  const firstCalls = go.calls.length
  nextViewer = true
  view.rerenderHome({ viewerId: "new-member" })
  expect(screen.queryByText("Old viewer private task")).toBeNull()
  expect(
    screen.getByRole<HTMLTextAreaElement>("textbox", {
      name: "Start a new conversation",
    }).value
  ).toBe("")
  await waitFor(() => expect(go.calls.length).toBeGreaterThan(firstCalls + 7))
  await act(async () => {
    finish!(json({ requests: [] }))
  })
  await screen.findByText("Nothing needs you right now.")
  const keys = view.client
    .getQueryCache()
    .getAll()
    .map((query) => query.queryKey)
  expect(
    keys.filter((key) => key.includes("new-member")).length
  ).toBeGreaterThanOrEqual(9)
  expect(keys.every((key) => key.includes(ORG))).toBe(true)
})
