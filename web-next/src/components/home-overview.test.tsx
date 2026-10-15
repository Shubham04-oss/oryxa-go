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
import { appsQueryKey } from "@/lib/apps/client"
import { ragKeys } from "@/lib/rag/client"
import { threadKeys } from "@/lib/threads/client"
import type { Item } from "@/lib/threads/client"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import type { AgentWorkflow } from "@/lib/agents/agent-workflows"
import type { ActiveAgentLease } from "@/lib/home/client"
import { HomeOverview, greeting } from "./home-overview"
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
const base = {
  id: "base",
  name: "Knowledge",
  document_count: 1,
  updated_at: new Date(NOW).toISOString(),
}
const connection = (toolkit: string, status = "active", id = toolkit) => ({
  id,
  toolkit,
  status,
  member_id: OWNER,
})
const workflow = (
  id: string,
  overrides: Partial<AgentWorkflow> = {}
): AgentWorkflow => ({
  id,
  organization_id: ORG,
  owner_user_id: OWNER,
  name: id,
  team_id: "team",
  status: "active",
  assignments: [],
  revision: 1,
  created_at: new Date(NOW).toISOString(),
  updated_at: new Date(NOW).toISOString(),
  ...overrides,
})
const minutesAgo = (minutes: number) =>
  new Date(NOW - minutes * 60_000).toISOString()
/** A wake-up thread's detail: each batch from the apps, then the agent's brief. */
const wakeDetail = (items: Item[]) => ({
  ...chat("wake", "Updates", "wakeup"),
  items,
  pending_confirmations: [],
})
const appsBatch = (id: string, at: string, ...toolkits: string[]): Item => ({
  type: "message",
  id,
  at,
  author: "apps",
  text: toolkits
    .map((toolkit, i) => `${i + 1}. ${toolkit} · event · 12:00`)
    .join("\n"),
})
const said = (
  id: string,
  at: string,
  text: string,
  author = "Krude"
): Item => ({ type: "message", id, at, author, text })
type Route =
  unknown | ((url: URL, init?: RequestInit) => Response | Promise<Response>)

function stubApi(overrides: Record<string, Route> = {}) {
  const routes: Record<string, Route> = {
    [`${path}/agent-requests/inbox`]: { requests: [] },
    [`${path}/agent-leases`]: page(),
    [`${path}/apps/connections`]: { connections: [] },
    [`${path}/agent-workflows`]: { workflows: [] },
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
    onOpenAgent: vi.fn(),
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

it("shows the same chats at a glance in the agent panel, without its own composer", async () => {
  stubApi({
    [`${path}/threads`]: {
      threads: [chat("one", "First"), chat("two", "Second")],
    },
  })
  const view = renderHome({ variant: "glance" })
  expect(view.container.querySelector(".home-glance-cards")).toBeTruthy()
  expect(screen.queryByRole("region", { name: "Your agent" })).toBeNull()
  expect(screen.queryByRole("textbox")).toBeNull()
  fireEvent.click(await screen.findByRole("button", { name: /First/ }))
  expect(view.onOpenConversation).toHaveBeenCalledWith("one")
})

it("lays At a glance out as the board's cards, from real reads only", async () => {
  stubApi({
    [`${path}/threads`]: {
      threads: [chat("wake", "Updates", "wakeup"), chat("one", "First")],
    },
    [`${path}/threads/wake`]: wakeDetail([
      appsBatch("b1", minutesAgo(12), "slack"),
      said("s1", minutesAgo(11), "Raya mentioned you in #eu-launch."),
    ]),
    [`${path}/threads/one`]: {
      ...chat("one", "First"),
      items: [
        {
          type: "artifact",
          id: "a1",
          at: minutesAgo(5),
          artifact: "EU launch plan.docx",
          version: 1,
        },
      ],
      pending_confirmations: [],
    },
    [`${path}/apps/connections`]: {
      connections: [connection("slack"), connection("github")],
    },
    [`${path}/agent-workflows`]: {
      workflows: [
        workflow("Weekday digest"),
        workflow("Monday metrics"),
        workflow("Someone else's", { owner_user_id: ASKER }),
      ],
    },
    // Needs you is the panel's chip now, not a card here.
    [`${path}/agent-requests/inbox`]: { requests: [request()] },
  })
  const onOpenPeople = vi.fn()
  const onMessage = vi.fn()
  const onOpenWorkflows = vi.fn()
  const view = renderHome({
    variant: "glance",
    agentName: "Krude",
    onOpenPeople,
    onMessage,
    onOpenWorkflows,
    teammates: {
      status: "ready",
      people: [
        {
          id: "user-kenji",
          name: "Kenji Sato",
          email: "kenji@example.com",
          role: "member",
          spaces: [],
          agents: [],
        },
        {
          id: ASKER,
          name: "Raya Patel",
          email: "raya@example.com",
          role: "member",
          spaces: ["Launch"],
          agents: [],
        },
      ],
      find: () => undefined,
      nameOf: () => undefined,
      peer: () => undefined,
      refetch: vi.fn(),
    },
  })

  const apps = screen.getByRole("region", { name: "From your apps" })
  expect(
    await within(apps).findByRole("list", { name: "Your connected apps" })
  ).toBeTruthy()
  expect(
    await within(apps).findByText("Raya mentioned you in #eu-launch.")
  ).toBeTruthy()
  fireEvent.click(within(apps).getByRole("button", { name: /Manage/ }))
  expect(view.onOpenTools).toHaveBeenCalled()

  // Teammates who share a teamspace come first; a tap messages them.
  const mates = screen.getByRole("region", { name: "Teammates" })
  const buttons = within(mates).getAllByRole("button", { name: /^Message/ })
  expect(buttons.map((button) => button.getAttribute("aria-label"))).toEqual([
    "Message Raya Patel",
    "Message Kenji Sato",
  ])
  fireEvent.click(buttons[0])
  expect(onMessage).toHaveBeenCalledWith(ASKER)
  fireEvent.click(within(mates).getByRole("button", { name: /All/ }))
  expect(onOpenPeople).toHaveBeenCalled()

  const knowledge = screen.getByRole("region", { name: "Knowledge" })
  expect(
    within(knowledge).getByRole("button", { name: "Knowledge, 1 document" })
  ).toBeTruthy()
  fireEvent.click(within(knowledge).getByRole("button", { name: /Library/ }))
  expect(view.onOpenKnowledge).toHaveBeenCalled()

  const files = screen.getByRole("region", { name: "Files Krude made" })
  expect(
    await within(files).findByRole("button", {
      name: "EU launch plan.docx, from First",
    })
  ).toBeTruthy()

  const recurring = screen.getByRole("region", { name: "Recurring work" })
  await within(recurring).findByText("Weekday digest")
  expect(within(recurring).queryByText("Someone else's")).toBeNull()
  fireEvent.click(
    within(recurring).getByRole("button", {
      name: "All 2 recurring workflows",
    })
  )
  expect(onOpenWorkflows).toHaveBeenCalled()

  expect(screen.queryByText(/Waiting for your OK/)).toBeNull()
  expect(screen.queryByRole("list", { name: "Needs you" })).toBeNull()
})

it("keeps three titled recent chats under Conversations at a glance, opening exact chats and history", async () => {
  stubApi({
    [`${path}/threads`]: {
      threads: [
        chat("blank", "  "),
        chat("wake", "Wake", "wakeup"),
        chat("one", " First "),
        chat("two", "Second"),
        chat("three", "Third"),
        chat("four", "Fourth"),
      ],
    },
  })
  const view = renderHome({ variant: "glance" })
  const conversations = screen.getByRole("region", { name: "Conversations" })
  await within(conversations).findByRole("button", { name: /First/ })
  expect(
    within(conversations).getByRole("list", { name: "Recent conversations" })
      .children
  ).toHaveLength(3)
  expect(screen.queryByText("Fourth")).toBeNull()
  // The wake-up thread is not a conversation.
  expect(within(conversations).queryByText("Wake")).toBeNull()
  fireEvent.click(
    within(conversations).getByRole<HTMLButtonElement>("button", {
      name: /Second/,
    })
  )
  expect(view.onOpenConversation).toHaveBeenCalledWith("two")
  // All conversations is on the page's Pick up header, not at a glance.
  fireEvent.click(
    within(conversations).getByRole<HTMLButtonElement>("button", {
      name: "New chat",
    })
  )
  expect(view.onNewChat).toHaveBeenLastCalledWith()
})

it("asks Krude from the large composer in the middle of the page, starting a new chat", async () => {
  stubApi()
  const view = renderHome()
  const agent = screen.getByRole("region", { name: "Your agent" })
  const field = within(agent).getByRole<HTMLTextAreaElement>("textbox", {
    name: "Start a new conversation",
  })
  expect(field.placeholder).toBe("Ask Krude anything")
  fireEvent.change(field, { target: { value: "  Check the launch plan  " } })
  fireEvent.click(
    within(agent).getByRole<HTMLButtonElement>("button", {
      name: "Send message",
    })
  )
  expect(view.onNewChat).toHaveBeenLastCalledWith("Check the launch plan")
  expect(field.value).toBe("")
  // A suggestion asks at once, in a new chat.
  fireEvent.click(
    within(agent).getByRole("button", { name: "What needs me today?" })
  )
  expect(view.onNewChat).toHaveBeenLastCalledWith("What needs me today?")
})

it("suggests catching up only on an app the member really connected", async () => {
  stubApi()
  const first = renderHome()
  await screen.findByText("Nothing needs you right now.")
  expect(screen.queryByRole("button", { name: /Catch me up/ })).toBeNull()
  // Without a connected tool, While you were away offers to connect one.
  fireEvent.click(
    await screen.findByRole("button", { name: "Connect your tools" })
  )
  expect(first.onOpenTools).toHaveBeenCalledOnce()
  first.unmount()

  stubApi({
    [`${path}/apps/connections`]: {
      connections: [connection("linear", "expired"), connection("github")],
    },
  })
  const view = renderHome()
  fireEvent.click(
    await screen.findByRole("button", { name: "Catch me up on GitHub" })
  )
  expect(view.onNewChat).toHaveBeenLastCalledWith("Catch me up on GitHub")
  expect(screen.queryByRole("button", { name: /Linear/ })).toBeNull()
})

it("pins a running open chat once and keeps starting chat truthful until an ID exists", async () => {
  stubApi({
    [`${path}/threads`]: {
      threads: [
        chat("one"),
        chat("two"),
        chat("three"),
        chat("open", "Running chat"),
      ],
    },
  })
  const view = renderHome({
    variant: "glance",
    primaryChatActivity: { state: "running", threadId: "open" },
  })
  const recents = await screen.findByRole("list", {
    name: "Recent conversations",
  })
  await waitFor(() => expect(recents.children).toHaveLength(3))
  expect(within(recents).getAllByText("Running chat")).toHaveLength(1)
  expect(
    within(recents)
      .getByText("Working")
      .closest("button")
      ?.querySelector("[data-orb-state=working]")
  ).toBeTruthy()
  view.rerenderHome({
    primaryChatActivity: { state: "running", threadId: null },
  })
  await screen.findByText("Starting conversation")
  // Until it has an ID there is nothing to open.
  expect(
    screen
      .getByText("Starting conversation")
      .closest("button")
      ?.hasAttribute("disabled")
  ).toBe(true)
})

it("greets by the member's clock and first name", () => {
  const at = (hour: number) => new Date(2026, 9, 14, hour, 30).getTime()
  expect(greeting(at(9), "Ada Brennan")).toBe("Good morning, Ada")
  expect(greeting(at(14), "Ada")).toBe("Good afternoon, Ada")
  expect(greeting(at(21), " ")).toBe("Good evening")
  expect(greeting(at(2))).toBe("Good evening")
  expect(greeting(at(9), "ada@northstar.io")).toBe("Good morning")
})

it("lays My workspace out on one axis: greeting, composer, Needs you beside While you were away, then Pick up", async () => {
  stubApi()
  const view = renderHome({ viewerName: "Ada Brennan" })
  await screen.findByText("Nothing needs you right now.")
  expect(
    screen.getAllByRole("heading", { level: 2 }).map((node) => node.textContent)
  ).toEqual([
    expect.stringMatching(/^Good (morning|afternoon|evening), Ada$/),
    "Needs you",
    "While you were away",
    "Pick up where you left off",
  ])
  const order = [
    screen.getByRole("region", { name: "Your agent" }),
    screen.getByRole("region", { name: "Needs you" }),
    screen.getByRole("region", { name: "While you were away" }),
    screen.getByRole("region", { name: "Pick up where you left off" }),
  ]
  for (let i = 1; i < order.length; i++)
    expect(
      order[i - 1].compareDocumentPosition(order[i]) &
        Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy()
  // The two tiles sit side by side, as a pair.
  expect(order[1].parentElement).toBe(order[2].parentElement)
  // The page is decluttered: no tool pills, stack or setup line.
  expect(screen.queryByLabelText("Your tools")).toBeNull()
  expect(screen.queryByRole("region", { name: "Recurring work" })).toBeNull()
  expect(view.container.querySelector(".home-card")).toBeNull()
  expect(
    screen.queryByText(/Your requests|shared answers|You asked/)
  ).toBeNull()
  // No parley runs (the teamspaces read is not served here): no pill.
  expect(screen.queryByRole("status", { name: "Live parley" })).toBeNull()
})

it("says how many things need the member and what Krude handled for them today", async () => {
  stubApi({
    [`${path}/agent-requests/inbox`]: {
      requests: [
        request({ id: "waiting", task: "Waiting on you" }),
        ...["a", "b"].map((id) =>
          request({
            id,
            status: "completed",
            decision: {
              decision: "approved",
              message: "Approved by their agent.",
              allowed_tools: [],
              granted_permissions: [],
              decided_at: new Date(NOW - 60_000).toISOString(),
              decided_by: { kind: "agent" },
            },
          })
        ),
      ],
    },
  })
  renderHome({ viewerName: "Ada" })
  expect(
    await screen.findByText("1 thing needs you. Krude handled 2 more today.")
  ).toBeTruthy()
  expect(screen.getByLabelText("1 waiting").textContent).toBe("1")
})

it("orders Pick up where you left off by what was touched last, four at most", async () => {
  const artifact = (id: string, at: string, name: string) =>
    ({
      type: "artifact",
      id,
      at,
      author: "Krude",
      artifact: name,
      version: 1,
    }) as Item
  stubApi({
    [`${path}/threads`]: {
      threads: [
        { ...chat("one", "Launch plan"), updated_at: minutesAgo(30) },
        { ...chat("two", "Old chat"), updated_at: minutesAgo(900) },
      ],
    },
    [`${path}/threads/one`]: {
      ...chat("one", "Launch plan"),
      items: [artifact("e1", minutesAgo(5), "deck.pptx")],
      pending_confirmations: [],
    },
    [`${path}/corpora`]: {
      corpora: [
        { ...base, id: "k1", name: "Handbook", updated_at: minutesAgo(60) },
        { ...base, id: "k2", name: "Archive", updated_at: minutesAgo(2000) },
      ],
    },
  })
  const view = renderHome()
  const row = screen.getByRole("region", { name: "Pick up where you left off" })
  await within(row).findByText("deck.pptx")
  expect(
    within(row)
      .getAllByRole("listitem")
      .map((card) => card.textContent)
  ).toEqual([
    "PPTXdeck.pptxLaunch plan · 5 min ago",
    "Launch planChat · 30 min ago",
    "HandbookLibrary · 1 document",
    "Old chatChat · 15 h ago",
  ])
  // Each card is one button, named for what it opens, its quiet line its
  // description; the thumbnail is decoration.
  const cards = within(row).getAllByRole("listitem")
  expect(
    cards.map((card) => {
      const buttons = within(card).getAllByRole("button")
      expect(buttons).toHaveLength(1)
      return [
        buttons[0].getAttribute("aria-label"),
        document.getElementById(buttons[0].getAttribute("aria-describedby")!)
          ?.textContent,
      ]
    })
  ).toEqual([
    ["Open deck.pptx", "Launch plan · 5 min ago"],
    ["Open Launch plan", "Chat · 30 min ago"],
    ["Open Handbook", "Library · 1 document"],
    ["Open Old chat", "Chat · 15 h ago"],
  ])
  for (const card of cards)
    expect(
      card.querySelector(".home-pickup-thumb")?.getAttribute("aria-hidden")
    ).toBe("true")
  fireEvent.click(within(row).getByRole("button", { name: "Open deck.pptx" }))
  expect(view.onOpenConversation).toHaveBeenLastCalledWith("one")
  fireEvent.click(within(row).getByRole("button", { name: "Open Handbook" }))
  expect(view.onOpenKnowledge).toHaveBeenCalledOnce()
  fireEvent.click(within(row).getByRole("button", { name: "Open Library" }))
  expect(view.onOpenKnowledge).toHaveBeenCalledTimes(2)
  fireEvent.click(
    within(row).getByRole("button", { name: "All conversations" })
  )
  expect(view.onOpenAgent).toHaveBeenCalledOnce()
})

it("lists waiting teammate requests as rows with one action that opens that exact request in Requests", async () => {
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
  const ownReply = request({
    id: "own-reply",
    answer_by: "owner",
    task: "Only you can answer this",
    created_at: minutesAgo(5),
  })
  stubApi({
    [`${path}/agent-requests/inbox`]: {
      requests: [
        consent,
        held,
        ownReply,
        request({
          id: "shared",
          status: "completed",
          task: "What you shared",
          result: answer("The agent shared this answer"),
        }),
      ],
    },
  })
  const view = renderHome()
  const needs = await screen.findByRole("list", { name: "Needs you" })
  await within(needs).findByText("Only you can answer this")
  const rows = within(needs).getAllByRole("listitem")
  expect(rows).toHaveLength(3)
  // One action per row, named for the request it opens.
  expect(
    rows.map((row) =>
      within(row)
        .getAllByRole("button")
        .map((button) => [
          button.textContent,
          button.getAttribute("aria-label"),
        ])
    )
  ).toEqual([
    [["Decide", "Decide on Raya’s request"]],
    [["Review", "Review Raya’s request"]],
    [["Reply", "Reply to Raya’s request"]],
  ])
  expect(rows[0].textContent).toContain("Raya")
  expect(rows[0].textContent).toContain(consent.task)
  expect(rows[0].textContent).toContain(
    "Waiting for your OK · Slack · 30 min ago"
  )
  expect(rows[1].textContent).toContain(
    "Your agent’s answer is held for you · Oryxa"
  )
  expect(rows[2].textContent).toContain(
    "Needs your own reply · Oryxa · 5 min ago"
  )
  // Deciding happens in Requests: no inline cards, held answers or replies.
  expect(screen.queryByText("Held answer with personal data")).toBeNull()
  expect(screen.queryByText("The agent shared this answer")).toBeNull()
  expect(screen.queryByText("What you shared")).toBeNull()
  expect(
    screen.queryByRole("button", {
      name: /Share answer|Allow for 1 hour|Deny|Reply yourself|Send .*masked/,
    })
  ).toBeNull()
  expect(screen.getAllByRole("textbox")).toHaveLength(1)
  fireEvent.click(
    within(needs).getByRole("button", { name: "Review Raya’s request" })
  )
  expect(view.onOpenRequests).toHaveBeenLastCalledWith("held")
  fireEvent.click(
    within(needs).getByRole("button", { name: "Decide on Raya’s request" })
  )
  expect(view.onOpenRequests).toHaveBeenLastCalledWith(consent.id)
  fireEvent.click(
    within(needs).getByRole("button", { name: "Reply to Raya’s request" })
  )
  expect(view.onOpenRequests).toHaveBeenLastCalledWith("own-reply")
  expect(screen.queryByText("Nothing needs you right now.")).toBeNull()
})

it("keeps only this member's unexpired owner decisions and held reviews; bounds rows with All requests", async () => {
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
        ...[1, 2, 3, 4, 5].map((id) =>
          request({ id: `own-${id}`, task: `Owner request ${id}` })
        ),
      ],
    },
  })
  const view = renderHome()
  const needs = await screen.findByRole("list", { name: "Needs you" })
  await within(needs).findByText("Owner request 1")
  expect(within(needs).getAllByRole("listitem")).toHaveLength(4)
  expect(within(needs).queryByText("Owner request 5")).toBeNull()
  expect(screen.queryByText("Foreign owner")).toBeNull()
  expect(screen.queryByText("Agent handles this")).toBeNull()
  expect(screen.queryByText("Expired request")).toBeNull()
  expect(screen.getByText("1 more waiting in Requests.")).toBeTruthy()
  fireEvent.click(
    within(screen.getByRole("region", { name: "Needs you" })).getByRole(
      "button",
      { name: "All requests" }
    )
  )
  expect(view.onOpenRequests).toHaveBeenCalledWith()
})

it("keeps a teammate row and the composer draft mounted during a background refresh", async () => {
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
  const action = await screen.findByRole("button", {
    name: "Decide on Raya’s request",
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
  expect(screen.getByRole("button", { name: "Decide on Raya’s request" })).toBe(
    action
  )
  await act(async () => {
    finish!(json({ requests: [request()] }))
    await refetch
  })
  expect(screen.getByRole("button", { name: "Decide on Raya’s request" })).toBe(
    action
  )
  expect(
    screen.getByRole<HTMLTextAreaElement>("textbox", {
      name: "Start a new conversation",
    })
  ).toBe(composer)
  expect(composer.value).toBe("Unsent question")
})

it("reads briefs from the wake-up thread with their apps, newest first, and opens that thread", async () => {
  stubApi({
    [`${path}/threads`]: {
      threads: [chat("wake", "Updates", "wakeup"), chat("one", "One")],
    },
    [`${path}/apps/connections`]: { connections: [connection("github")] },
    [`${path}/threads/wake`]: wakeDetail([
      appsBatch("b1", minutesAgo(60), "github", "linear"),
      said("a1", minutesAgo(59), "**PR #12** was merged. Details follow."),
      said("m1", minutesAgo(58), "What next?", "member"),
      said("a2", minutesAgo(57), "Not a brief: it answers the member."),
      appsBatch("b2", minutesAgo(10), "an app", "notion"),
      said("a3", minutesAgo(9), "The [Launch](https://x.test) page changed!"),
    ]),
  })
  const view = renderHome()
  const apps = screen.getByRole("region", { name: "While you were away" })
  await within(apps).findByText("The Launch page changed!")
  const rows = within(apps).getAllByRole("listitem")
  expect(rows.map((row) => row.textContent)).toEqual([
    "The Launch page changed!Notion · 9 min ago",
    "PR #12 was merged.GitHub, Linear · 59 min ago",
  ])
  expect(screen.queryByText(/Not a brief/)).toBeNull()
  fireEvent.click(within(rows[1]).getByRole("button"))
  expect(view.onOpenConversation).toHaveBeenLastCalledWith("wake")
  view.onOpenConversation.mockClear()
  fireEvent.click(within(apps).getByRole("button", { name: "All updates" }))
  expect(view.onOpenConversation).toHaveBeenCalledExactlyOnceWith("wake")
  expect(screen.queryByText("Nothing new from your apps.")).toBeNull()
})

it("never leaves While you were away as a bare title", async () => {
  const gone = () =>
    json({ error: { code: "not_found", message: "private diagnostic" } }, 404)
  // Chats this server can't list leave the briefs unknown: one sentence that
  // names the area, and nothing to retry.
  stubApi({
    [`${path}/threads`]: gone,
    [`${path}/apps/connections`]: { connections: [connection("github")] },
  })
  const first = renderHome()
  const apps = screen.getByRole("region", { name: "While you were away" })
  expect(
    await within(apps).findByText(
      "Updates from your apps can’t be loaded right now."
    )
  ).toBeTruthy()
  expect(within(apps).queryByRole("button", { name: "Try again" })).toBeNull()
  expect(
    await screen.findByText("Recent chats can’t be loaded right now.")
  ).toBeTruthy()
  expect(screen.queryByText(/is not available here|private diagnostic/)).toBe(
    null
  )
  first.unmount()

  // With no app connected, it says what would fill it, and where to start.
  stubApi({ [`${path}/threads`]: gone })
  const second = renderHome()
  const empty = screen.getByRole("region", { name: "While you were away" })
  await within(empty).findByText(
    "Connect an app to see what happened while you were away."
  )
  expect(within(empty).queryByText(/can’t be loaded/)).toBeNull()
  fireEvent.click(
    within(empty).getByRole("button", { name: "Connect your tools" })
  )
  expect(second.onOpenTools).toHaveBeenCalledOnce()
  second.unmount()

  // Apps connected, chats read, no wake-up thread yet: nothing new.
  stubApi({
    [`${path}/apps/connections`]: { connections: [connection("github")] },
  })
  renderHome()
  const quiet = screen.getByRole("region", { name: "While you were away" })
  expect(
    await within(quiet).findByText("Nothing new from your apps.")
  ).toBeTruthy()
})

it("says what lands under From your apps before there are briefs", async () => {
  stubApi()
  const first = renderHome({ variant: "glance" })
  const apps = screen.getByRole("region", { name: "From your apps" })
  expect(
    await within(apps).findByText("Connect a tool and its news lands here.")
  ).toBeTruthy()
  expect(within(apps).queryByRole("button", { name: "All updates" })).toBeNull()
  first.unmount()

  stubApi({
    [`${path}/threads`]: { threads: [chat("wake", "Updates", "wakeup")] },
    [`${path}/apps/connections`]: { connections: [connection("github")] },
    [`${path}/threads/wake`]: wakeDetail([
      said("m1", minutesAgo(5), "Hello", "member"),
      said("a1", minutesAgo(4), "Hi there."),
    ]),
  })
  renderHome({ variant: "glance" })
  const updates = screen.getByRole("region", { name: "From your apps" })
  expect(
    await within(updates).findByText("No updates from your apps yet.")
  ).toBeTruthy()
  // At a glance has no All updates; the latest brief is the row that opens it.
  expect(
    within(updates).queryByRole("button", { name: "All updates" })
  ).toBeNull()
})

it("shows the three most recently updated knowledge bases, and All opens Knowledge", async () => {
  stubApi({
    [`${path}/corpora`]: {
      corpora: [
        { ...base, id: "old", name: "Oldest", updated_at: minutesAgo(600) },
        {
          ...base,
          id: "new",
          name: "Newest",
          document_count: 3,
          updated_at: minutesAgo(5),
        },
        { ...base, id: "mid", name: "Middle", updated_at: minutesAgo(120) },
        {
          ...base,
          id: "day",
          name: "Yesterday's",
          updated_at: minutesAgo(1800),
        },
      ],
    },
  })
  const view = renderHome({ variant: "glance" })
  const knowledge = screen.getByRole("region", { name: "Knowledge" })
  await within(knowledge).findByText("Newest · 3")
  expect(
    within(knowledge)
      .getAllByRole("listitem")
      .map((row) => within(row).getByRole("button").getAttribute("aria-label"))
  ).toEqual(["Newest, 3 documents", "Middle, 1 document", "Oldest, 1 document"])
  fireEvent.click(within(knowledge).getByRole("button", { name: /^Library/ }))
  expect(view.onOpenKnowledge).toHaveBeenCalledOnce()
  fireEvent.click(within(knowledge).getByRole("button", { name: /Middle/ }))
  expect(view.onOpenKnowledge).toHaveBeenCalledTimes(2)
})

it("invites knowledge when there is none to look up", async () => {
  stubApi({ [`${path}/corpora`]: { corpora: [] } })
  const view = renderHome({ variant: "glance" })
  const knowledge = screen.getByRole("region", { name: "Knowledge" })
  await within(knowledge).findByText(/Nothing to look up yet\./)
  fireEvent.click(within(knowledge).getByRole("button", { name: /^Library/ }))
  expect(view.onOpenKnowledge).toHaveBeenCalledOnce()
})

it("lists the files the agent saved in recent chats, latest version once, newest first, opening their chat", async () => {
  const artifact = (id: string, at: string, name: string, version: number) =>
    ({
      type: "artifact",
      id,
      at,
      author: "Krude",
      artifact: name,
      version,
    }) as Item
  const go = stubApi({
    [`${path}/threads`]: { threads: [chat("one", "One"), chat("two", "Two")] },
    [`${path}/threads/one`]: {
      ...chat("one", "One"),
      items: [
        artifact("e1", minutesAgo(30), "report.pdf", 1),
        artifact("e2", minutesAgo(10), "report.pdf", 2),
      ],
      pending_confirmations: [],
    },
    [`${path}/threads/two`]: {
      ...chat("two", "Two"),
      items: [artifact("e3", minutesAgo(20), "notes.md", 1)],
      pending_confirmations: [],
    },
  })
  const view = renderHome({ variant: "glance" })
  const files = screen.getByRole("region", { name: "Files your agent made" })
  await within(files).findByText("notes.md")
  expect(
    within(files)
      .getAllByRole("listitem")
      .map((row) => within(row).getByRole("button").getAttribute("aria-label"))
  ).toEqual(["report.pdf, from One", "notes.md, from Two"])
  fireEvent.click(within(files).getByRole("button", { name: /notes\.md/ }))
  expect(view.onOpenConversation).toHaveBeenCalledWith("two")
  // Files come from the chat details Today already reads, not a read of their own.
  expect(
    go.calls.filter((call) => call.url.pathname === `${path}/threads/one`)
  ).toHaveLength(1)
})

it("shows the viewer's own recurring work with its schedule in words", async () => {
  stubApi({
    [`${path}/agent-workflows`]: {
      workflows: [
        workflow("Weekday digest", {
          schedule: {
            expression: "cron(0 9 ? * MON-FRI *)",
            timezone: "UTC",
            state: "enabled",
          },
        }),
        workflow("Someone else's", { owner_user_id: ASKER }),
        workflow("Paused report", {
          status: "disabled",
          schedule: {
            expression: "rate(1 day)",
            timezone: "UTC",
            state: "enabled",
          },
        }),
        workflow("On demand"),
        workflow("Fourth of mine"),
      ],
    },
  })
  renderHome({ variant: "glance" })
  const recurring = screen.getByRole("region", { name: "Recurring work" })
  await within(recurring).findByText("Weekday digest")
  expect(
    within(recurring)
      .getAllByRole("listitem")
      .map((row) => row.textContent)
  ).toEqual([
    "Weekday digestWeekdays at 09:00 UTC",
    "Paused reportPaused",
    "On demandRuns when you start it",
  ])
})

it("says when nothing runs on a schedule", async () => {
  stubApi()
  renderHome({ variant: "glance" })
  const recurring = screen.getByRole("region", { name: "Recurring work" })
  expect(
    await within(recurring).findByText("Nothing runs on a schedule yet.")
  ).toBeTruthy()
})

it("lists every unfinished setup step on one Finish setup line, each opening its place", async () => {
  stubApi({
    [`${path}/apps/slack/status`]: {
      status: "not_installed",
      can_manage: true,
    },
    [`${path}/corpora`]: { corpora: [{ ...base, document_count: 0 }] },
    "/v1/agent-grant": { granted: false },
  })
  const view = renderHome({ variant: "glance" })
  const setup = await screen.findByLabelText("Finish setup")
  await within(setup).findByRole("button", { name: "Connect Slack" })
  await within(setup).findByRole("button", {
    name: "Turn on background access",
  })
  expect(setup.textContent).toBe(
    "Finish setup:Connect Slack · Add knowledge · Turn on background access"
  )
  fireEvent.click(within(setup).getByRole("button", { name: "Connect Slack" }))
  expect(view.onOpenSlack).toHaveBeenCalledOnce()
  fireEvent.click(within(setup).getByRole("button", { name: "Add knowledge" }))
  expect(view.onOpenKnowledge).toHaveBeenCalledOnce()
  fireEvent.click(
    within(setup).getByRole("button", { name: "Turn on background access" })
  )
  expect(view.onOpenAccount).toHaveBeenCalledOnce()
  expect(
    screen.queryByRole("heading", { name: /Finish setup|Set up/ })
  ).toBeNull()
})

it("reads apps, tools and recurring work, and never sent requests or shared activity", async () => {
  const go = stubApi()
  renderHome({ variant: "glance" })
  await screen.findByText("Nothing runs on a schedule yet.")
  const paths = go.calls.map((call) => call.url.pathname)
  expect(paths).toContain(`${path}/apps/connections`)
  expect(paths).toContain(`${path}/agent-workflows`)
  expect(
    paths.filter(
      (pathname) =>
        pathname.endsWith("/agent-requests/sent") ||
        pathname.endsWith("/agent-requests/activity")
    )
  ).toEqual([])
  expect(go.calls.every((call) => call.method === "GET")).toBe(true)
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
  await screen.findByText("2 tool confirmations")
  expect(screen.getByText("1 tool confirmation")).toBeTruthy()
  expect(
    screen.getByText(/limited to your five newest chats and the open chat/)
  ).toBeTruthy()
  const details = go.calls
    .filter((call) => call.url.pathname.startsWith(`${path}/threads/`))
    .map((call) => call.url.pathname.split("/").at(-1))
  // The five newest chats, the open one, and the wake-up thread's briefs.
  expect(details.sort()).toEqual(
    ["chat-0", "chat-1", "chat-2", "chat-3", "chat-4", "chat-10", "wake"].sort()
  )
  expect(screen.queryByText("Nothing needs you right now.")).toBeNull()
  fireEvent.click(
    within(
      screen.getByText(/waiting in Open older chat/).closest("li")!
    ).getByRole<HTMLButtonElement>("button", { name: "Open" })
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
        onOpenAgent={vi.fn()}
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
  await screen.findByText(/waiting in Stored confirmation/)
  deny = true
  await act(async () => {
    await view.client.invalidateQueries({ queryKey: threadKeys.all(ORG) })
  })
  const needs = screen.getByRole("list", { name: "Needs you" })
  await waitFor(() =>
    expect(within(needs).queryByText(/Stored confirmation/)).toBeNull()
  )
  fireEvent.click(
    within(
      within(needs)
        .getByText(/waiting in Open chat/)
        .closest("li")!
    ).getByRole<HTMLButtonElement>("button", { name: "Open" })
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
  await screen.findByText("Tool confirmations can’t be loaded right now.")
  expect(screen.queryByText("Nothing needs you right now.")).toBeNull()
  expect(screen.getByText(/Older chats may still need you/)).toBeTruthy()
})

it("gives quiet complete empty Home exactly once, with only unfinished setup", async () => {
  stubApi({
    [`${path}/corpora`]: { corpora: [{ ...base, document_count: 0 }] },
    [`${path}/apps/slack/status`]: { status: "ended", can_manage: false },
    "/v1/agent-grant": { granted: false },
  })
  renderHome({ variant: "glance" })
  // Slack is not this member's to connect, so the line leaves it out.
  const setup = await screen.findByLabelText("Finish setup")
  await within(setup).findByRole("button", {
    name: "Turn on background access",
  })
  expect(
    within(setup)
      .getAllByRole("button")
      .map((button) => button.textContent)
  ).toEqual(["Add knowledge", "Turn on background access"])
  expect(screen.queryByText(/connect Slack/i)).toBeNull()
  expect(screen.queryByRole("list", { name: "Needs you" })).toBeNull()
  expect(screen.queryByRole("button", { name: "End access" })).toBeNull()
})

it("hides completed setup and does not create Slack authorization URLs", async () => {
  const go = stubApi()
  renderHome()
  await screen.findByText("Nothing needs you right now.")
  expect(screen.queryByLabelText("Finish setup")).toBeNull()
  expect(screen.queryByText(/Finish setup/)).toBeNull()
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
  await screen.findByText(/can ask your agent/)
  const section = screen.getByRole("list", { name: "Needs you" })
  expect(
    within(section).getAllByRole("button", { name: "End access" })
  ).toHaveLength(1)
  const rows = within(section).getAllByRole("listitem")
  expect(rows).toHaveLength(2)
  expect(rows[0].textContent).toContain("Raya can ask your agent")
  expect(
    within(rows[0]).getByRole("button", { name: "End access" })
  ).toBeTruthy()
  expect(rows[1].textContent).toContain("You can ask Raya’s agent")
  // Access does not count as needing the member, nor as quiet.
  expect(screen.queryByText("Nothing needs you right now.")).toBeNull()
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
  await screen.findAllByText(/can ask your agent/)
  const section = screen.getByRole("list", { name: "Needs you" })
  const teammates = screen.getByRole("region", { name: "Needs you" })
  expect(within(section).getAllByRole("listitem")).toHaveLength(3)
  fireEvent.click(
    within(teammates).getByRole<HTMLButtonElement>("button", {
      name: "All access",
    })
  )
  expect(within(section).getAllByRole("listitem")).toHaveLength(4)
  fireEvent.click(
    within(teammates).getByRole<HTMLButtonElement>("button", {
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
    within(teammates).queryByRole("button", { name: "Load more access" })
  ).toBeNull()
  fireEvent.click(
    within(teammates).getByRole<HTMLButtonElement>("button", {
      name: "Show less",
    })
  )
  expect(within(section).getAllByRole("listitem")).toHaveLength(3)
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
  expect(screen.queryByRole("button", { name: "End access" })).toBeNull()
  expect(screen.queryByText(/can ask your agent/)).toBeNull()
  expect(screen.queryByRole("list", { name: "Needs you" })).toBeNull()
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
  stubApi({ [`${path}/threads`]: { threads: [chat("one")] } })
  renderHome({ onOpenConversation: undefined, onNewChat: undefined })
  await screen.findByRole("button", { name: /one/ })
  expect(
    screen.getByRole<HTMLButtonElement>("button", { name: /one/ }).disabled
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
    failed: "Requests can’t be loaded right now.",
    route: `${path}/agent-requests/inbox`,
    seed: { requests: [request({ task: "Private pending task" })] },
    privateText: "Private pending task",
    key: agentKeys.requestBox(ORG, "inbox"),
    needsCheck: true,
  },
  {
    name: "conversations",
    failed: "Recent chats can’t be loaded right now.",
    route: `${path}/threads`,
    seed: { threads: [chat("private", "Private recent title")] },
    privateText: "Private recent title",
    key: threadKeys.list(ORG),
    needsCheck: true,
  },
  {
    name: "open access",
    failed: "Open access windows can’t be loaded right now.",
    route: `${path}/agent-leases`,
    seed: page([activeLease()]),
    privateText: /can ask your agent/,
    key: [...agentKeys.requests(ORG), "active-leases"],
    needsCheck: true,
  },
  {
    name: "updates",
    failed: "Updates from your apps can’t be loaded right now.",
    route: `${path}/threads/wake`,
    seed: wakeDetail([
      appsBatch("batch", minutesAgo(3), "github"),
      said("brief", minutesAgo(2), "A private brief. More detail."),
    ]),
    privateText: "A private brief.",
    key: threadKeys.detail(ORG, "wake"),
    extra: {
      [`${path}/threads`]: { threads: [chat("wake", "Updates", "wakeup")] },
    },
  },
  {
    name: "your tools",
    route: `${path}/apps/connections`,
    seed: { connections: [connection("github")] },
    // Tools only feed a suggestion on the page: it goes, without a notice.
    privateText: "Catch me up on GitHub",
    key: appsQueryKey(ORG),
  },
  {
    name: "knowledge bases",
    failed: "Library collections can’t be loaded right now.",
    route: `${path}/corpora`,
    seed: { corpora: [{ ...base, name: "Private base" }] },
    privateText: "Private base",
    key: ragKeys.corpora(ORG),
  },
  {
    name: "recurring work",
    // At a glance's card says it in its own words.
    failed: "Couldn’t load recurring work.",
    route: `${path}/agent-workflows`,
    seed: { workflows: [workflow("Private weekly digest")] },
    privateText: "Private weekly digest",
    key: agentKeys.workflowList(ORG),
    // Recurring work is at a glance only, where there is no composer.
    glance: true,
  },
])(
  "removes private $name after a refused refresh, preserving the unsent composer",
  async ({
    route,
    seed,
    privateText,
    key,
    needsCheck,
    extra,
    failed,
    glance = false,
  }) => {
    let deny = false
    stubApi({ ...extra, [route]: () => (deny ? refused() : json(seed)) })
    const view = renderHome(glance ? { variant: "glance" } : {})
    await screen.findByText(privateText)
    const field = glance
      ? null
      : screen.getByRole<HTMLTextAreaElement>("textbox", {
          name: "Start a new conversation",
        })
    if (field) fireEvent.change(field, { target: { value: "Keep this draft" } })
    deny = true
    await act(async () => {
      await view.client.invalidateQueries({ queryKey: key })
    })
    if (failed) await screen.findByText(failed)
    await waitFor(() => expect(screen.queryByText(privateText)).toBeNull())
    if (field) expect(field.value).toBe("Keep this draft")
    // A read that decides whether anything needs the member never lets a
    // refusal pass for quiet.
    if (needsCheck)
      expect(screen.queryByText("Nothing needs you right now.")).toBeNull()
    expect(screen.queryByText("private diagnostic")).toBeNull()
  }
)

it.each([
  {
    name: "Slack status",
    route: `${path}/apps/slack/status`,
    seed: { status: "not_installed", can_manage: true },
    step: "Connect Slack",
    key: ["apps", ORG],
  },
  {
    name: "background access",
    route: "/v1/agent-grant",
    seed: { granted: false },
    step: "Turn on background access",
    key: ["auth", "agent-grant"],
  },
])(
  "drops the setup step a refused $name refresh leaves unknown, quietly, at a glance",
  async ({ route, seed, step, key }) => {
    let deny = false
    stubApi({ [route]: () => (deny ? refused() : json(seed)) })
    const view = renderHome({ variant: "glance" })
    const setup = await screen.findByLabelText("Finish setup")
    expect(within(setup).getByRole("button", { name: step })).toBeTruthy()
    deny = true
    await act(async () => {
      await view.client.invalidateQueries({ queryKey: key })
    })
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: step })).toBeNull()
    )
    expect(screen.queryByLabelText("Finish setup")).toBeNull()
    expect(screen.queryByText(/can’t be loaded/)).toBeNull()
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
