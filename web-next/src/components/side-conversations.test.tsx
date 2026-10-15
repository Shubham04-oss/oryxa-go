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
import type { DmPeople, DmPerson } from "./direct-message-people"
import { CallProvider } from "./call-provider"
import { WorkspaceAgentPanel } from "./workspace-agent-panel"
import { WorkspaceShell } from "./workspace-shell"
import type { Session } from "@/lib/auth/client"
import type { Member } from "@/lib/auth/invitations"
import type { Conversation, Inbox } from "@/lib/dms"
import type {
  SideRoster,
  SideServerMessage,
  SideWireAgentMessage,
  SideWireItem,
  SideWireMessage,
  SideWireRequestCard,
  SideWireStatusLine,
} from "@/lib/side/protocol"
import { roomSockets } from "@/test/room-socket"
import type { FakeRoomSocket } from "@/test/room-socket"
import {
  adaWhisperer,
  apiError,
  contribution,
  json,
  memberWhisperer,
  mockApi,
  org,
  orgPath,
  teamspace,
  viewerId,
  whisperer,
} from "@/test/teamspaces"
import type { ApiCall } from "@/test/teamspaces"

// Every side conversation, DM, inbox and chat socket is a stand-in the test
// speaks through as the Durable Object would (src/test/room-socket.ts).
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)
vi.mock(
  "@cloudflare/ai-chat/react",
  async () => (await import("@/test/agent-chat")).aiChatReact
)

const priya = "user-priya"
const sideName = `${org}~tsp_launch~${viewerId}~${priya}`
const sidePath = `/agents/side-conversation-agent/${sideName}`
const spacePath = `${orgPath}/teamspaces/tsp_launch`

const space = teamspace({
  whisperers: [
    memberWhisperer,
    whisperer(priya, "Priya", "agt_nia", "Nia"),
    adaWhisperer,
  ],
  contributions: [
    contribution({
      agent_id: "agt_pricing",
      owner_user_id: priya,
      name: "Pricing",
      role: "Prices the launch",
      delegation_mode: "automatic",
    }),
    contribution({
      agent_id: "agt_notes",
      owner_user_id: priya,
      name: "Notes",
      role: "Keeps the release notes",
      position: 1,
    }),
    contribution({
      agent_id: "agt_drafts",
      owner_user_id: "user-ada",
      name: "Drafts",
      status: "disabled",
      position: 2,
    }),
  ],
})

const roster: SideRoster = {
  teamspaceName: "Launch room",
  paused: false,
  people: [
    {
      userId: viewerId,
      displayName: "Member",
      agentId: "agt_leo",
      agentName: "Leo",
    },
    {
      userId: priya,
      displayName: "Priya",
      agentId: "agt_nia",
      agentName: "Nia",
    },
  ],
  contributions: [
    {
      agentId: "agt_pricing",
      ownerId: priya,
      name: "Pricing",
      role: "Prices the launch",
      delegationMode: "automatic",
    },
    {
      agentId: "agt_notes",
      ownerId: priya,
      name: "Notes",
      role: "Keeps the release notes",
      delegationMode: "request",
    },
  ],
}

function person(id: string, name: string): DmPerson {
  return {
    id,
    name,
    email: `${id}@sim.oryxa`,
    role: "member",
    spaces: [],
    agents: [],
  }
}

/** Whom the panel can name, as useDmPeople hands it over once loaded. */
function peopleOf(list: DmPerson[]): DmPeople {
  const byId = new Map(list.map((entry) => [entry.id, entry]))
  return {
    status: "ready",
    people: list,
    find: (id) => byId.get(id),
    nameOf: (id) => byId.get(id)?.name ?? "Former member",
    peer: (id) => {
      const known = byId.get(id)
      return known
        ? { id, name: known.name, short: known.name.split(" ")[0] }
        : { id, name: "Former member", short: "this person" }
    },
    refetch: vi.fn(),
  }
}

const people = peopleOf([
  person("user-ada", "Ada Lovelace"),
  person(priya, "Priya Shah"),
])

const noConversations: Inbox = {
  conversations: [],
  unreadTotal: 0,
  status: "live",
}

/** Today at 09:mm, so every item falls on one day. */
function at(minute: number): string {
  const moment = new Date()
  moment.setHours(9, minute, 0, 0)
  return moment.toISOString()
}

function message(
  seq: number,
  authorId: string,
  body: string,
  extra: Partial<SideWireMessage> = {}
): SideWireMessage {
  return {
    kind: "message",
    id: `sdi_${seq}`,
    seq,
    authorId,
    body,
    createdAt: at(seq),
    ...extra,
  }
}

function agentMessage(
  seq: number,
  extra: Partial<SideWireAgentMessage> = {}
): SideWireAgentMessage {
  return {
    kind: "agent-message",
    id: `sdi_${seq}`,
    seq,
    agentId: "agt_leo",
    agentName: "Leo",
    ownerId: viewerId,
    body: "",
    createdAt: at(seq),
    updatedAt: at(seq),
    status: "done",
    ...extra,
  }
}

/** A request the member made to Priya's agent, Nia, unless `extra` says otherwise. */
function card(
  seq: number,
  extra: Partial<SideWireRequestCard> = {}
): SideWireRequestCard {
  return {
    kind: "request-card",
    id: `sdi_${seq}`,
    seq,
    requestId: `irq_${seq}`,
    requesterId: viewerId,
    requesterAgentId: "agt_leo",
    requesterAgentName: "Leo",
    targetId: priya,
    targetAgentId: "agt_nia",
    targetAgentName: "Nia",
    approval: "owner",
    status: "pending",
    task: "Check the release notes",
    createdAt: at(seq),
    updatedAt: at(seq),
    expiresAt: "2099-01-01T00:00:00.000Z",
    ...extra,
  }
}

/** A request Priya made to the member's agent, Leo, held for the member. */
function cardForMe(
  seq: number,
  extra: Partial<SideWireRequestCard> = {}
): SideWireRequestCard {
  return card(seq, {
    requesterId: priya,
    requesterAgentId: "agt_nia",
    requesterAgentName: "Nia",
    targetId: viewerId,
    targetAgentId: "agt_leo",
    targetAgentName: "Leo",
    task: "Send the launch brief",
    ...extra,
  })
}

function statusLine(
  seq: number,
  text: string,
  extra: Partial<SideWireStatusLine> = {}
): SideWireStatusLine {
  return {
    kind: "status",
    id: `sdi_${seq}`,
    seq,
    requestId: `irq_${seq - 1}`,
    outcome: "rejected",
    authorId: priya,
    text,
    createdAt: at(seq),
    ...extra,
  }
}

function snapshot(
  items: SideWireItem[] = [],
  overrides: Partial<Extract<SideServerMessage, { type: "side-snapshot" }>> = {}
): SideServerMessage {
  return {
    type: "side-snapshot",
    conversation: "sdc_1",
    items,
    hasOlder: false,
    reads: [
      { userId: priya, seq: 0 },
      { userId: viewerId, seq: 0 },
    ],
    online: [],
    typing: [],
    roster,
    ...overrides,
  }
}

/** The latest socket on the side conversation. */
function sideSocket(): FakeRoomSocket {
  const socket = roomSockets.named(sideName).at(-1)
  if (!socket) throw new Error("no side conversation socket")
  return socket
}

/** The side conversation's socket opens, and the object sends what it holds. */
function opened(sent: SideServerMessage = snapshot()): FakeRoomSocket {
  const socket = sideSocket()
  if (socket.readyState !== socket.OPEN) socket.open()
  socket.says(sent)
  return socket
}

type Route = (
  call: ApiCall & { url: URL }
) => Response | undefined | Promise<Response | undefined>

/** The agent panel as the shell mounts it, with the member's inbox, on a teamspace page or not. */
function renderPanel({
  teamspaceId,
  inbox = noConversations,
  route,
  withCalls = false,
}: {
  teamspaceId?: string
  inbox?: Inbox
  route?: Route
  /** Inside the workspace's CallProvider, where parleys are offered. */
  withCalls?: boolean
} = {}) {
  const api = mockApi(async (call) => {
    const answer = await route?.(call)
    if (answer) return answer
    if (call.url.pathname === spacePath) return json(space)
    return undefined
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const panel = (props: { teamspaceId?: string; inbox: Inbox }) => {
    const agentPanel = (
      <WorkspaceAgentPanel
        expanded
        onExpand={vi.fn()}
        onCollapse={vi.fn()}
        organizationId={org}
        directMessages={{ viewerId, inbox: props.inbox, people }}
        teamspaceId={props.teamspaceId}
      />
    )
    return (
      <QueryClientProvider client={client}>
        {withCalls ? <CallProvider>{agentPanel}</CallProvider> : agentPanel}
      </QueryClientProvider>
    )
  }
  const view = render(panel({ teamspaceId, inbox }))
  return {
    api,
    rerender: (props: { teamspaceId?: string; inbox?: Inbox }) =>
      view.rerender(
        panel({ teamspaceId: props.teamspaceId, inbox: props.inbox ?? inbox })
      ),
  }
}

/** Opens the side conversation with Priya from the teamspace's teammates, and returns its view. */
async function openWithPriya(
  options: Parameters<typeof renderPanel>[0] = {}
): Promise<HTMLElement> {
  renderPanel({ teamspaceId: "tsp_launch", ...options })
  fireEvent.click(
    await screen.findByRole("button", { name: "Talk with Priya Shah" })
  )
  return screen.getByRole("region", {
    name: "Side conversation with Priya Shah in Launch room",
  })
}

/** The composer's field, whoever it is addressed to. */
function composer(view: HTMLElement): HTMLTextAreaElement {
  return within(view).getByRole<HTMLTextAreaElement>("textbox")
}

function write(view: HTMLElement, text: string) {
  const field = composer(view)
  fireEvent.change(field, { target: { value: text } })
  fireEvent.keyDown(field, { key: "Enter" })
}

/** Picks whom the next message goes to, from the pill's menu. */
function sendTo(view: HTMLElement, choice: RegExp) {
  fireEvent.click(
    within(view).getByRole("button", { name: /^Who this goes to: / })
  )
  const menu = within(view).getByRole("menu", { name: "Who this goes to" })
  fireEvent.click(within(menu).getByRole("menuitemradio", { name: choice }))
}

/** What the conversation last said aloud. */
function announced(view: HTMLElement): string | null | undefined {
  return view.querySelector(".workspace-dm-announcer")?.textContent
}

beforeEach(() => {
  roomSockets.reset()
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

describe("The Agents tab on a teamspace page", () => {
  it("lists each teammate with their agents and how each takes a request, and only there", async () => {
    const { rerender } = renderPanel({ teamspaceId: "tsp_launch" })
    const section = screen.getByRole("region", { name: "In this teamspace" })
    expect(within(section).getByText("Loading teammates…")).toBeTruthy()
    await within(section).findByRole("button", { name: "Talk with Priya Shah" })
    expect(
      within(section)
        .getAllByRole("button", { name: /^Talk with / })
        .map((row) => row.getAttribute("aria-label"))
    ).toEqual(["Talk with Ada Lovelace", "Talk with Priya Shah"])
    const priyasAgents = within(section).getByRole("list", {
      name: "Priya Shah's agents",
    })
    expect(
      within(priyasAgents)
        .getAllByRole("button")
        .map((agent) => agent.getAttribute("aria-label"))
    ).toEqual([
      "Ask Nia, Priya Shah's agent (asks first)",
      "Ask Pricing, Priya Shah's specialist (automatic)",
      "Ask Notes, Priya Shah's specialist (asks first)",
    ])
    expect(within(priyasAgents).getByText("automatic")).toBeTruthy()
    // A paused contribution is not offered, and nobody is shown the member.
    expect(within(section).queryByText("Drafts")).toBeNull()
    expect(within(section).queryByText("Member")).toBeNull()
    // The member's own agent is still here, below.
    expect(
      screen.getByRole("textbox", { name: "Message your agent" })
    ).toBeTruthy()

    rerender({ teamspaceId: undefined })
    expect(
      screen.queryByRole("region", { name: "In this teamspace" })
    ).toBeNull()
    expect(
      screen.getByRole("textbox", { name: "Message your agent" })
    ).toBeTruthy()
  })

  it("counts what the member has not read with each teammate", async () => {
    renderPanel({
      teamspaceId: "tsp_launch",
      inbox: {
        conversations: [
          {
            kind: "side",
            name: sideName,
            peerId: priya,
            teamspaceId: "tsp_launch",
            teamspaceName: "Launch room",
            lastMessage: {
              authorId: priya,
              body: "About Thursday",
              createdAt: at(1),
            },
            unread: 2,
            updatedAt: at(1),
          },
        ],
        unreadTotal: 2,
        status: "live",
      },
    })
    const row = await screen.findByRole("button", {
      name: "Talk with Priya Shah, 2 unread",
    })
    expect(within(row).getByText("2")).toBeTruthy()
  })

  it("says honestly when the member has nobody to talk with here", async () => {
    renderPanel({
      teamspaceId: "tsp_launch",
      route: ({ url }) =>
        url.pathname === spacePath
          ? json(teamspace({ whisperers: [memberWhisperer] }))
          : undefined,
    })
    expect(
      await screen.findByText(
        "No teammates here yet. They show here once they have an active agent."
      )
    ).toBeTruthy()

    cleanup()
    renderPanel({
      teamspaceId: "tsp_launch",
      route: ({ url }) =>
        url.pathname === spacePath
          ? json({ ...space, is_member: false })
          : undefined,
    })
    expect(
      await screen.findByText(
        "Only members of the Launch team can talk with teammates here."
      )
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: /^Talk with / })).toBeNull()
  })

  it("opens the side conversation with a teammate, and goes back to the list with focus on them", async () => {
    const view = await openWithPriya()
    const socket = sideSocket()
    expect(socket.options).toMatchObject({
      agent: "SideConversationAgent",
      name: sideName,
      enabled: true,
    })
    // The member's own agents step aside while it is open.
    expect(
      screen.queryByRole("region", { name: "In this teamspace" })
    ).toBeNull()
    expect(within(view).getByText("Loading the conversation…")).toBeTruthy()
    // Opened by the member: straight to the composer.
    expect(document.activeElement).toBe(
      within(view).getByRole("textbox", { name: "Message Priya" })
    )

    opened(snapshot([], { online: [priya] }))
    expect(
      within(view).getByRole("heading", { name: "Priya Shah", level: 2 })
    ).toBeTruthy()
    expect(within(view).getByText("Launch room · Online")).toBeTruthy()
    const here = within(view).getByRole("list", { name: "Agents here" })
    // Both people's own agents first, then Priya's specialists.
    expect(
      within(here)
        .getAllByRole("listitem")
        .map((agent) => agent.textContent)
    ).toEqual([
      expect.stringContaining("Nia, Priya's agent"),
      expect.stringContaining("Leo, your agent"),
      expect.stringContaining("Pricing, Priya's specialist"),
      expect.stringContaining("+1 more"),
    ])
    expect(
      within(view).getByText("Talk with Priya about Launch room")
    ).toBeTruthy()
    expect(
      within(view).getByText(
        "Your agents can help. It stays between the two of you, away from the room."
      )
    ).toBeTruthy()

    // Another tab and back: it opens again where it was, without taking
    // focus from the tab.
    fireEvent.click(screen.getByRole("tab", { name: "DMs" }))
    expect(
      screen.queryByRole("region", { name: /^Side conversation/ })
    ).toBeNull()
    const agentsTab = screen.getByRole("tab", { name: "Agents" })
    agentsTab.focus()
    fireEvent.click(agentsTab)
    const again = screen.getByRole("region", {
      name: "Side conversation with Priya Shah in Launch room",
    })
    expect(document.activeElement).toBe(agentsTab)

    fireEvent.click(
      within(again).getByRole("button", { name: "Back to agents" })
    )
    expect(
      screen.queryByRole("region", { name: /^Side conversation/ })
    ).toBeNull()
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Talk with Priya Shah" })
    )
  })

  it("keeps the conversation open when the member leaves the teamspace, and goes back to the tab", async () => {
    const { rerender } = renderPanel({ teamspaceId: "tsp_launch" })
    fireEvent.click(
      await screen.findByRole("button", { name: "Talk with Priya Shah" })
    )
    rerender({ teamspaceId: undefined })
    const view = screen.getByRole("region", {
      name: "Side conversation with Priya Shah in Launch room",
    })
    fireEvent.click(
      within(view).getByRole("button", { name: "Back to agents" })
    )
    expect(
      screen.queryByRole("region", { name: "In this teamspace" })
    ).toBeNull()
    expect(document.activeElement).toBe(
      screen.getByRole("tab", { name: "Agents" })
    )
  })

  it("opens it asking the teammate's agent that was picked, once the conversation says who is here", async () => {
    renderPanel({ teamspaceId: "tsp_launch" })
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Ask Pricing, Priya Shah's specialist (automatic)",
      })
    )
    const view = screen.getByRole("region", {
      name: "Side conversation with Priya Shah in Launch room",
    })
    // Until then the choice waits rather than showing the wrong one.
    const waiting = within(view).getByRole("button", {
      name: "Who this goes to: loading",
    })
    expect(waiting.hasAttribute("disabled")).toBe(true)
    expect(within(view).getByText("Connecting…")).toBeTruthy()

    const socket = opened()
    expect(
      within(view).getByRole("button", { name: "Who this goes to: Pricing" })
    ).toBeTruthy()
    expect(
      within(view).getByText("Pricing runs this without asking Priya.")
    ).toBeTruthy()
    const field = within(view).getByRole("textbox", {
      name: "Ask Pricing, Priya's specialist",
    })
    fireEvent.change(field, { target: { value: "Price the launch" } })
    fireEvent.keyDown(field, { key: "Enter" })
    expect(socket.said("side-ask-teammate")).toEqual([
      expect.objectContaining({
        agentId: "agt_pricing",
        body: "Price the launch",
      }),
    ])
    expect(socket.said("side-send")).toEqual([])
    // Asking is for one message: the next goes to Priya again.
    expect(
      within(view).getByRole("button", { name: "Who this goes to: Priya" })
    ).toBeTruthy()
  })
})

describe("Whom a message goes to", () => {
  it("sends Priya a message by default, and asks either person's agent from a small menu", async () => {
    const view = await openWithPriya()
    const socket = opened()

    write(view, "Hello")
    expect(socket.said("side-send").map((sent) => sent.body)).toEqual(["Hello"])

    const pill = within(view).getByRole("button", {
      name: "Who this goes to: Priya",
    })
    expect(pill.getAttribute("aria-haspopup")).toBe("menu")
    fireEvent.click(pill)
    expect(pill.getAttribute("aria-expanded")).toBe("true")
    const menu = within(view).getByRole("menu", { name: "Who this goes to" })
    const choices = within(menu).getAllByRole("menuitemradio")
    expect(choices).toHaveLength(5)
    expect(
      choices.map((choice) => choice.getAttribute("aria-checked"))
    ).toEqual(["true", "false", "false", "false", "false"])
    expect(within(choices[1]).getByText("Ask my agent")).toBeTruthy()
    expect(
      within(choices[1]).getByText("Leo · Priya sees the answer too")
    ).toBeTruthy()
    expect(within(choices[2]).getByText("Ask Priya's agent")).toBeTruthy()
    expect(within(choices[2]).getByText("Nia · asks Priya first")).toBeTruthy()
    expect(
      within(choices[3]).getByText("Priya's specialist · runs without asking")
    ).toBeTruthy()
    // It opens on the choice in use, and the arrows move through it.
    expect(document.activeElement).toBe(choices[0])
    fireEvent.keyDown(menu, { key: "ArrowDown" })
    expect(document.activeElement).toBe(choices[1])
    fireEvent.keyDown(menu, { key: "ArrowUp" })
    fireEvent.keyDown(menu, { key: "ArrowUp" })
    expect(document.activeElement).toBe(choices[4])
    // Escape closes it, back on the pill.
    fireEvent.keyDown(menu, { key: "Escape" })
    expect(within(view).queryByRole("menu")).toBeNull()
    expect(document.activeElement).toBe(pill)

    sendTo(view, /Ask my agent/)
    const field = within(view).getByRole("textbox", {
      name: "Ask Leo, your agent",
    })
    // The choice hands focus back to the field.
    expect(document.activeElement).toBe(field)
    expect(within(view).getByText("Priya sees Leo's answer too.")).toBeTruthy()
    write(view, "What's left for Thursday?")
    expect(socket.said("side-ask-mine").map((sent) => sent.body)).toEqual([
      "What's left for Thursday?",
    ])
    // Shown at once as the member's question to Leo.
    expect(within(view).getByText("Asked Leo")).toBeTruthy()

    sendTo(view, /Ask Priya's agent/)
    expect(
      within(view).getByText("Priya approves this before Nia starts.")
    ).toBeTruthy()
    write(view, "Draft the release notes")
    expect(socket.said("side-ask-teammate")).toEqual([
      expect.objectContaining({
        agentId: "agt_nia",
        body: "Draft the release notes",
      }),
    ])
    // The request shows at once as a card, sending.
    const sending = within(view).getByRole("article", {
      name: "You asked Nia, Priya's agent",
    })
    expect(within(sending).getByText("Sending…")).toBeTruthy()
    expect(within(sending).getByText("Draft the release notes")).toBeTruthy()
    expect(socket.said("side-send")).toHaveLength(1)
  })

  it("holds a question to the member's own agent while it still answers", async () => {
    const view = await openWithPriya()
    const socket = opened(
      snapshot([agentMessage(1, { status: "streaming", body: "Looking" })])
    )
    sendTo(view, /Ask my agent/)
    expect(
      within(view).getByText("Leo is still answering. Ask again once it has.")
    ).toBeTruthy()
    const field = composer(view)
    fireEvent.change(field, { target: { value: "And the risks?" } })
    fireEvent.keyDown(field, { key: "Enter" })
    expect(socket.said("side-ask-mine")).toEqual([])
    expect(field.value).toBe("And the risks?")
    const sendButton = within(view).getByRole("button", {
      name: "Send message",
    })
    expect(sendButton.hasAttribute("disabled")).toBe(true)

    socket.says({
      type: "side-item",
      item: agentMessage(1, {
        status: "waiting",
        body: "Looking",
        confirmations: [{ callId: "call_1", tool: "read_calendar" }],
      }),
    })
    expect(
      within(view).getByText("Answer Leo's confirmation above first.")
    ).toBeTruthy()

    socket.says({
      type: "side-item",
      item: agentMessage(1, { body: "Two tasks remain." }),
    })
    expect(sendButton.hasAttribute("disabled")).toBe(false)
    fireEvent.keyDown(field, { key: "Enter" })
    expect(socket.said("side-ask-mine").map((sent) => sent.body)).toEqual([
      "And the risks?",
    ])
  })

  it("says a request that could not be filed, and files it again on Retry", async () => {
    const view = await openWithPriya()
    const socket = opened()
    sendTo(view, /Ask Priya's agent/)
    write(view, "Draft the release notes")
    const [asked] = socket.said("side-ask-teammate")
    socket.says({
      type: "side-failed",
      clientId: asked.clientId,
      code: "too_many_requests",
      message: "Five of your requests are waiting already.",
    })
    const failed = within(view).getByRole("article", {
      name: "You asked Nia, Priya's agent",
    })
    expect(
      within(failed).getByText(
        "Not filed. Five of your requests are waiting already."
      )
    ).toBeTruthy()
    expect(announced(view)).toBe("Your request to Nia wasn't filed.")
    fireEvent.click(
      within(failed).getByRole("button", { name: "Ask Nia again" })
    )
    expect(socket.said("side-ask-teammate")).toEqual([asked, asked])
    expect(within(failed).getByText("Sending…")).toBeTruthy()
  })
})

describe("What a side conversation holds", () => {
  it("shows people's messages as a DM's, agents' messages with whose they are, cards and quiet lines", async () => {
    const view = await openWithPriya()
    opened(
      snapshot([
        message(1, priya, "Can we ship Thursday?"),
        message(2, viewerId, "What's left?", { asksAgentId: "agt_leo" }),
        agentMessage(3, { body: "**Two** tasks remain.", replyTo: 2 }),
        card(4, { status: "completed" }),
        agentMessage(5, {
          agentId: "agt_nia",
          agentName: "Nia",
          ownerId: priya,
          body: "The notes are ready.",
          via: { requestId: "irq_4", onBehalfOf: priya, approvedBy: priya },
        }),
        card(6, {
          targetAgentId: "agt_pricing",
          targetAgentName: "Pricing",
          approval: "agent",
          status: "completed",
          task: "Price the launch",
        }),
        agentMessage(7, {
          agentId: "agt_pricing",
          agentName: "Pricing",
          ownerId: priya,
          body: "$49 a seat.",
          via: { requestId: "irq_6", onBehalfOf: priya },
        }),
        statusLine(8, "Priya declined the request to Notes: not this week."),
        agentMessage(9, {
          agentId: "agt_nia",
          agentName: "Nia",
          ownerId: priya,
          status: "streaming",
        }),
      ])
    )
    const log = within(view).getByRole("log", {
      name: "Side conversation with Priya Shah",
    })
    expect(log.getAttribute("aria-live")).toBe("off")

    // People's messages read as a DM's: Priya's beside her avatar, the
    // member's in a bubble, the question to Leo marked as such.
    const theirs = within(log)
      .getByText("Can we ship Thursday?")
      .closest(".workspace-dm-group")
    expect(theirs?.getAttribute("data-mine")).toBeNull()
    const question = within(log)
      .getByText("What's left?")
      .closest(".workspace-dm-message") as HTMLElement
    expect(
      question.closest(".workspace-dm-group")?.getAttribute("data-mine")
    ).toBe("true")
    expect(within(question).getByText("Asked Leo")).toBeTruthy()

    const leo = within(log).getByRole("article", { name: "Leo, your agent" })
    expect(within(leo).getByText("Two").tagName).toBe("STRONG")
    expect(within(leo).getByText("· your agent")).toBeTruthy()

    const [nia, niaWorking] = within(log).getAllByRole("article", {
      name: "Nia, Priya's agent",
    })
    expect(within(nia).getByText("The notes are ready.")).toBeTruthy()
    expect(
      within(nia).getByText("on behalf of Priya · approved by Priya")
    ).toBeTruthy()
    // An answer still being written says so until it has words.
    expect(within(niaWorking).getByText("Nia is working…")).toBeTruthy()
    expect(niaWorking.getAttribute("aria-busy")).toBe("true")

    const pricing = within(log).getByRole("article", {
      name: "Pricing, Priya's specialist",
    })
    expect(
      within(pricing).getByText("on behalf of Priya · ran without asking")
    ).toBeTruthy()

    const asked = within(log).getByRole("article", {
      name: "You asked Nia, Priya's agent",
    })
    expect(within(asked).getByText("You asked Nia")).toBeTruthy()
    expect(
      within(asked).getByText("Priya's agent · asks Priya first")
    ).toBeTruthy()
    expect(within(asked).getByText("Check the release notes")).toBeTruthy()
    expect(within(asked).getByText("Done.")).toBeTruthy()
    const automatic = within(log).getByRole("article", {
      name: "You asked Pricing, Priya's specialist",
    })
    expect(
      within(automatic).getByText("Priya's specialist · runs without asking")
    ).toBeTruthy()

    const quiet = within(log).getByText(
      /^Priya declined the request to Notes: not this week\./
    )
    expect(quiet.className).toBe("workspace-side-status")
  })

  it("tells the member who asked where their request stands", async () => {
    const view = await openWithPriya()
    const socket = opened(
      snapshot([
        card(1),
        card(2, {
          targetAgentId: "agt_pricing",
          targetAgentName: "Pricing",
          approval: "agent",
          task: "Price the launch",
        }),
      ])
    )
    const waiting = within(view).getByRole("article", {
      name: "You asked Nia, Priya's agent",
    })
    expect(
      within(waiting).getByText("Waiting for Priya to approve.")
    ).toBeTruthy()
    // Only Priya decides it.
    expect(
      within(waiting).queryByRole("button", { name: /^Approve/ })
    ).toBeNull()
    expect(within(view).queryByRole("button", { name: /^Decline/ })).toBeNull()
    const automatic = within(view).getByRole("article", {
      name: "You asked Pricing, Priya's specialist",
    })
    expect(
      within(automatic).getByText("Waiting for Pricing to start.")
    ).toBeTruthy()

    // Approved elsewhere: the card follows, and says so aloud.
    socket.says({
      type: "side-item",
      item: card(1, {
        status: "approved",
        decision: {
          decision: "approved",
          message: "Approved in the side conversation.",
          decidedAt: at(3),
        },
      }),
    })
    expect(
      within(waiting).getByText("Approved by Priya. Nia is working on it…")
    ).toBeTruthy()
    expect(announced(view)).toBe("Approved by Priya. Nia is working on it…")
    socket.says({
      type: "side-item",
      item: card(2, {
        targetAgentId: "agt_pricing",
        targetAgentName: "Pricing",
        approval: "agent",
        task: "Price the launch",
        status: "approved",
      }),
    })
    expect(
      within(automatic).getByText("Pricing is working on it…")
    ).toBeTruthy()

    for (const [status, words] of [
      ["rejected", "Declined by Priya."],
      ["failed", "Failed."],
      ["expired", "Expired."],
    ] as const) {
      socket.says({ type: "side-item", item: card(1, { status }) })
      expect(within(waiting).getByText(words)).toBeTruthy()
    }
  })

  it("lets the agent's owner approve or decline a request held for them, with a note", async () => {
    const view = await openWithPriya()
    const socket = opened(snapshot([cardForMe(1), cardForMe(2)]))
    const [first, second] = within(view).getAllByRole("article", {
      name: "Priya Shah asked Leo, your agent",
    })
    expect(within(first).getByText("Priya asked Leo")).toBeTruthy()
    expect(within(first).getByText("your agent · asks you first")).toBeTruthy()
    expect(within(first).getByText("Waiting for you to approve.")).toBeTruthy()

    fireEvent.click(within(first).getByRole("button", { name: "Add a note" }))
    const note = within(first).getByRole("textbox", {
      name: "A note for Priya Shah (optional)",
    })
    expect(document.activeElement).toBe(note)
    fireEvent.change(note, { target: { value: "Go ahead, use the new deck." } })
    fireEvent.click(
      within(first).getByRole("button", {
        name: "Approve Leo's request from Priya Shah",
      })
    )
    // It asked for no tools, so Approve allows none, and says so.
    expect(socket.said("side-decide")).toEqual([
      {
        type: "side-decide",
        requestId: "irq_1",
        decision: "approved",
        message: "Go ahead, use the new deck.",
        allowedTools: [],
      },
    ])
    // On its way: the buttons wait.
    expect(first.getAttribute("aria-busy")).toBe("true")
    expect(within(first).getByText("Approving…")).toBeTruthy()
    for (const button of within(first).getAllByRole("button", {
      name: /request from Priya Shah$/,
    })) {
      expect(button.hasAttribute("disabled")).toBe(true)
    }

    socket.says({
      type: "side-item",
      item: cardForMe(1, {
        status: "approved",
        decision: {
          decision: "approved",
          message: "Go ahead, use the new deck.",
          decidedAt: at(3),
        },
      }),
    })
    expect(
      within(first).getByText("Approved by you. Leo is working on it…")
    ).toBeTruthy()
    expect(within(first).queryByRole("button", { name: /^Approve/ })).toBeNull()
    // The buttons went with the decision; focus stays with the card.
    await waitFor(() => expect(document.activeElement).toBe(first))

    // A decision the object refuses: the card says why, and stays open.
    fireEvent.click(
      within(second).getByRole("button", {
        name: "Decline Leo's request from Priya Shah",
      })
    )
    expect(socket.said("side-decide").at(-1)).toEqual({
      type: "side-decide",
      requestId: "irq_2",
      decision: "rejected",
    })
    socket.says({
      type: "side-refused",
      target: "irq_2",
      code: "unavailable",
      message: "That couldn't be done just now. Try again.",
    })
    expect(await within(second).findByRole("alert")).toHaveProperty(
      "textContent",
      "That couldn't be done just now. Try again."
    )
    expect(
      within(second)
        .getByRole("button", { name: "Decline Leo's request from Priya Shah" })
        .hasAttribute("disabled")
    ).toBe(false)
  })

  it("tells the owner what approving lets their agent read, and what each choice allows", async () => {
    const view = await openWithPriya()
    const both = ["apps", "knowledge"]
    const socket = opened(
      snapshot([
        cardForMe(1, { requestedTools: both }),
        cardForMe(2, { requestedTools: both }),
        cardForMe(3, { requestedTools: ["knowledge"] }),
        // Asked for no tools, or none that open anything.
        cardForMe(4),
        cardForMe(5, { requestedTools: ["github.search"] }),
        // The member asked it: Priya is the one asked.
        card(6, { requestedTools: both }),
      ])
    )
    const [first, second, third, fourth, fifth] = within(view).getAllByRole(
      "article",
      { name: "Priya Shah asked Leo, your agent" }
    )
    expect(
      within(first).getByText(
        "Leo may read your connected apps and team knowledge."
      )
    ).toBeTruthy()
    expect(
      within(first)
        .getAllByRole("button", { name: /request from Priya Shah/ })
        .map((button) => button.textContent)
    ).toEqual(["Decline", "Approve without tools", "Approve with tools"])
    const approve = within(first).getByRole("button", {
      name: "Approve with tools for Leo's request from Priya Shah",
    })
    expect(approve.getAttribute("data-primary")).toBe("true")
    const withoutTools = within(first).getByRole("button", {
      name: "Approve Leo's request from Priya Shah without tools",
    })
    expect(
      document.getElementById(withoutTools.getAttribute("aria-describedby")!)
        ?.textContent
    ).toBe("Approve without tools means Leo can’t look anything up.")
    expect(within(third).getByText("Leo may read team knowledge.")).toBeTruthy()
    for (const plain of [fourth, fifth]) {
      expect(within(plain).queryByText(/may read/)).toBeNull()
      expect(within(plain).queryByText(/can’t look anything up/)).toBeNull()
      expect(
        within(plain)
          .getAllByRole("button", { name: /request from Priya Shah/ })
          .map((button) => button.textContent)
      ).toEqual(["Decline", "Approve"])
    }
    const mine = within(view).getByRole("article", {
      name: "You asked Nia, Priya's agent",
    })
    expect(within(mine).queryByText(/may read/)).toBeNull()
    expect(within(mine).queryByText(/can’t look anything up/)).toBeNull()

    fireEvent.click(
      within(first).getByRole("button", {
        name: "Approve with tools for Leo's request from Priya Shah",
      })
    )
    fireEvent.click(
      within(second).getByRole("button", {
        name: "Approve Leo's request from Priya Shah without tools",
      })
    )
    fireEvent.click(
      within(fifth).getByRole("button", {
        name: "Approve Leo's request from Priya Shah",
      })
    )
    fireEvent.click(
      within(fourth).getByRole("button", {
        name: "Decline Leo's request from Priya Shah",
      })
    )
    expect(socket.said("side-decide")).toEqual([
      {
        type: "side-decide",
        requestId: "irq_1",
        decision: "approved",
        allowedTools: ["apps", "knowledge"],
      },
      {
        type: "side-decide",
        requestId: "irq_2",
        decision: "approved",
        allowedTools: [],
      },
      {
        type: "side-decide",
        requestId: "irq_5",
        decision: "approved",
        allowedTools: ["github.search"],
      },
      { type: "side-decide", requestId: "irq_4", decision: "rejected" },
    ])
    expect(within(second).getByText("Approving…")).toBeTruthy()
  })

  it("shows the owner an answer held for them, why in plain words, and sends their review", async () => {
    const view = await openWithPriya()
    const heldAnswer = {
      stored: "Reach Ada on **555-0100**, or at ada@sim.oryxa.",
      masked: "Reach Ada on {PHONE}, or at {EMAIL}.",
    }
    const socket = opened(
      snapshot([
        cardForMe(1, {
          status: "review",
          guard: { state: "held", kinds: ["EMAIL", "PHONE"] },
          heldAnswer,
        }),
        cardForMe(2, {
          status: "review",
          guard: { state: "held", kinds: ["UNCHECKED"] },
          heldAnswer: { stored: heldAnswer.stored },
        }),
        // Not read as the member yet.
        cardForMe(3, {
          status: "review",
          guard: { state: "held", kinds: ["ADDRESS"] },
        }),
        cardForMe(4, { status: "approved" }),
        // Its agent's failure, held as any answer is.
        cardForMe(5, {
          status: "review",
          guard: { state: "held", kinds: ["EMAIL"] },
          heldAnswer: {
            stored: "I couldn't reach ada@sim.oryxa's calendar.",
            masked: "I couldn't reach {EMAIL}'s calendar.",
            failed: true,
          },
        }),
      ])
    )
    const [first, second, third, fourth, fifth] = within(view).getAllByRole(
      "article",
      { name: "Priya Shah asked Leo, your agent" }
    )
    expect(
      within(fifth).getByText("Leo couldn't finish the request, and says:")
    ).toBeTruthy()
    expect(within(first).queryByText(/couldn't finish/)).toBeNull()
    expect(
      within(first).getByText(
        "Leo's answer includes an email address and a phone number. Review it before it's shared."
      )
    ).toBeTruthy()
    // The answer as Send shares it, and a preview of what Send masked does.
    expect(within(first).getByText("555-0100").tagName).toBe("STRONG")
    expect(within(first).getByText("Send masked shares:")).toBeTruthy()
    expect(
      within(first).getByText("Reach Ada on {PHONE}, or at {EMAIL}.")
    ).toBeTruthy()
    expect(
      within(first)
        .getAllByRole("button", { name: /answer to Priya Shah/ })
        .map((button) => [
          button.getAttribute("aria-label"),
          button.textContent,
        ])
    ).toEqual([
      ["Don't send Leo's answer to Priya Shah", "Don't send"],
      ["Send Leo's answer to Priya Shah", "Send"],
      ["Send Leo's answer to Priya Shah, masked", "Send masked"],
    ])
    expect(
      within(second).getByText(
        "Leo's answer couldn't be checked automatically. Review it before it's shared."
      )
    ).toBeTruthy()
    expect(view.textContent).not.toContain("UNCHECKED")
    // Not checked, it has no masked version: no preview, no Send masked.
    expect(within(second).queryByText("Send masked shares:")).toBeNull()
    expect(
      within(second)
        .getAllByRole("button", { name: /answer to Priya Shah/ })
        .map((button) => button.textContent)
    ).toEqual(["Don't send", "Send"])
    // Nothing is sent before the member has seen it; not sending never waits.
    expect(within(third).getByText("Loading the answer…")).toBeTruthy()
    expect(
      within(third)
        .getAllByRole("button", { name: /answer to Priya Shah/ })
        .map((button) => button.textContent)
    ).toEqual(["Don't send"])

    fireEvent.click(
      within(first).getByRole("button", {
        name: "Send Leo's answer to Priya Shah",
      })
    )
    expect(first.getAttribute("aria-busy")).toBe("true")
    expect(within(first).getByText("Sending…")).toBeTruthy()
    fireEvent.click(
      within(fifth).getByRole("button", {
        name: "Send Leo's answer to Priya Shah, masked",
      })
    )
    fireEvent.click(
      within(third).getByRole("button", {
        name: "Don't send Leo's answer to Priya Shah",
      })
    )
    expect(socket.said("side-review")).toEqual([
      { type: "side-review", requestId: "irq_1", action: "send" },
      { type: "side-review", requestId: "irq_5", action: "send_masked" },
      { type: "side-review", requestId: "irq_3", action: "withhold" },
    ])

    // Sent: the answer and its choices go, and focus stays with the card.
    socket.says({
      type: "side-item",
      item: cardForMe(1, {
        status: "completed",
        review: { action: "send", decidedAt: at(9), by: "owner" },
      }),
    })
    expect(within(first).queryByText("Send masked shares:")).toBeNull()
    expect(
      within(first).queryByRole("button", { name: /answer to Priya Shah/ })
    ).toBeNull()
    await waitFor(() => expect(document.activeElement).toBe(first))

    // One held while the member watches is said aloud.
    socket.says({
      type: "side-item",
      item: cardForMe(4, {
        status: "review",
        guard: { state: "held", kinds: ["PHONE"] },
      }),
    })
    expect(announced(view)).toBe(
      "Leo's answer includes a phone number. Review it before it's shared."
    )
    expect(
      within(fourth).getByText(
        "Leo's answer includes a phone number. Review it before it's shared."
      )
    ).toBeTruthy()
  })

  it("tells the member who asked that the answer waits for review, then shows it reviewed, or not shared", async () => {
    const view = await openWithPriya()
    const socket = opened(
      snapshot([
        card(1, { status: "review" }),
        card(2, { status: "approved" }),
        // Go withheld it when nobody reviewed it in time.
        card(3, {
          status: "withheld",
          review: {
            action: "withhold",
            decidedAt: "2099-01-01T00:00:00.000Z",
            by: "system",
            reason: "expired before review",
          },
        }),
      ])
    )
    const [first, second, third] = within(view).getAllByRole("article", {
      name: "You asked Nia, Priya's agent",
    })
    expect(
      within(first).getByText("Waiting for Priya to review the answer.")
    ).toBeTruthy()
    expect(within(first).queryByRole("button")).toBeNull()
    expect(within(first).queryByText(/Send masked/)).toBeNull()
    expect(within(third).getByText("Expired.")).toBeTruthy()

    socket.says({ type: "side-item", item: card(2, { status: "review" }) })
    expect(announced(view)).toBe("Waiting for Priya to review the answer.")

    // Priya sends it: the answer lands as today, saying she reviewed it.
    socket.says({
      type: "side-item",
      item: card(1, {
        status: "completed",
        review: { action: "send", decidedAt: at(5), by: "owner" },
      }),
    })
    socket.says({
      type: "side-item",
      item: agentMessage(4, {
        agentId: "agt_nia",
        agentName: "Nia",
        ownerId: priya,
        body: "Reach Ada on 555-0100.",
        via: {
          requestId: "irq_1",
          onBehalfOf: priya,
          approvedBy: priya,
          reviewedBy: priya,
        },
      }),
    })
    expect(within(first).getByText("Done.")).toBeTruthy()
    const nia = within(view).getByRole("article", {
      name: "Nia, Priya's agent",
    })
    expect(within(nia).getByText("Reach Ada on 555-0100.")).toBeTruthy()
    expect(
      within(nia).getByText(
        "on behalf of Priya · approved by Priya · reviewed by Priya"
      )
    ).toBeTruthy()

    // Priya keeps the other to herself.
    socket.says({
      type: "side-item",
      item: card(2, {
        status: "withheld",
        review: { action: "withhold", decidedAt: at(6), by: "owner" },
      }),
    })
    socket.says({
      type: "side-item",
      item: statusLine(5, "Priya chose not to share Nia's answer.", {
        requestId: "irq_2",
        outcome: "withheld",
      }),
    })
    expect(within(second).getByText("Not shared by Priya.")).toBeTruthy()
    const log = within(view).getByRole("log", {
      name: "Side conversation with Priya Shah",
    })
    expect(
      within(log).getByText(/^Priya chose not to share Nia's answer\./)
        .className
    ).toBe("workspace-side-status")
    expect(announced(view)).toBe("Priya chose not to share Nia's answer.")
  })

  it("says why Oryxa withheld an answer, and reads an agent's decline in its fixed words", async () => {
    const view = await openWithPriya()
    opened(
      snapshot([
        // Oryxa withheld it: Priya could no longer send it here.
        card(1, {
          status: "withheld",
          review: {
            action: "withhold",
            decidedAt: at(1),
            by: "system",
            reason:
              "Oryxa withheld this answer because the agent that asked no longer holds a seat in the teamspace.",
          },
        }),
        statusLine(
          2,
          "Oryxa withheld Nia's answer because the agent that asked no longer holds a seat in the teamspace.",
          { requestId: "irq_1", outcome: "withheld" }
        ),
        // Priya's automatic specialist declined, in Go's fixed words.
        card(3, {
          targetAgentId: "agt_pricing",
          targetAgentName: "Pricing",
          approval: "agent",
          status: "rejected",
          decision: {
            decision: "rejected",
            message: "Declined by their agent.",
            decidedAt: at(3),
            by: "agent",
          },
        }),
        statusLine(4, "Pricing declined the request.", {
          requestId: "irq_3",
        }),
      ])
    )
    const withheld = within(view).getByRole("article", {
      name: "You asked Nia, Priya's agent",
    })
    expect(within(withheld).getByText("Withheld by Oryxa.")).toBeTruthy()
    const log = within(view).getByRole("log", {
      name: "Side conversation with Priya Shah",
    })
    expect(
      within(log).getByText(
        /^Oryxa withheld Nia's answer because the agent that asked no longer holds a seat in the teamspace\./
      )
    ).toBeTruthy()
    const pricing = within(view).getByRole("article", {
      name: "You asked Pricing, Priya's specialist",
    })
    expect(within(pricing).getByText("Declined by Pricing.")).toBeTruthy()
    expect(
      within(log).getByText(/^Pricing declined the request\./)
    ).toBeTruthy()
    expect(view.textContent).not.toContain("Declined by their agent.")
  })

  it("shows the member's own agent's confirmations to them alone", async () => {
    const view = await openWithPriya()
    const socket = opened(
      snapshot([
        agentMessage(1, {
          status: "waiting",
          body: "I can send the brief.",
          confirmations: [
            {
              callId: "call_1",
              tool: "send_email",
              hint: "Send the brief to the launch list.",
              arguments: { to: "launch@sim.oryxa" },
            },
          ],
        }),
        // Priya's own agent waits on her: what it waits on is hers to see,
        // even were the page handed it.
        agentMessage(2, {
          agentId: "agt_nia",
          agentName: "Nia",
          ownerId: priya,
          status: "waiting",
          confirmations: [{ callId: "call_2", tool: "delete_file" }],
        }),
      ])
    )
    const leo = within(view).getByRole("article", { name: "Leo, your agent" })
    const confirmation = within(leo).getByRole("group", {
      name: "Confirm Send email",
    })
    expect(
      within(confirmation).getByText("Send the brief to the launch list.")
    ).toBeTruthy()
    fireEvent.click(
      within(confirmation).getByRole("button", { name: "Approve" })
    )
    expect(socket.said("side-confirm")).toEqual([
      {
        type: "side-confirm",
        seq: 1,
        answers: [{ id: "call_1", confirmed: true }],
      },
    ])

    const nia = within(view).getByRole("article", {
      name: "Nia, Priya's agent",
    })
    expect(
      within(nia).getByText("Nia is waiting for Priya to confirm a step.")
    ).toBeTruthy()
    expect(within(nia).queryByRole("group")).toBeNull()
    expect(within(nia).queryByRole("button")).toBeNull()
    expect(
      within(view).queryByRole("group", { name: "Confirm Delete file" })
    ).toBeNull()
  })

  it("tells an answer's owner, quietly, what the guard hid from the other person, and nobody else", async () => {
    const view = await openWithPriya()
    opened(
      snapshot([
        message(1, viewerId, "What's the key?", { asksAgentId: "agt_leo" }),
        agentMessage(2, {
          body: "The key is {PASSWORD}.",
          replyTo: 1,
          guard: { state: "masked", kinds: ["PASSWORD"] },
        }),
        // Nothing found, or nothing named: nothing to say.
        agentMessage(3, {
          body: "Friday.",
          guard: { state: "clear", kinds: [] },
        }),
        agentMessage(4, {
          body: "Thursday.",
          guard: { state: "masked", kinds: [] },
        }),
        agentMessage(5, { body: "Saturday." }),
        // Priya's agent's answer is hers: the note is the owner's alone,
        // even were the page handed one.
        agentMessage(6, {
          agentId: "agt_nia",
          agentName: "Nia",
          ownerId: priya,
          body: "Noted {PASSWORD}.",
          guard: { state: "masked", kinds: ["PASSWORD"] },
        }),
        // A held answer's own note, on the owner's copy, for what it hid.
        agentMessage(7, {
          body: "Reach Ada at {EMAIL}.",
          guard: { state: "held", kinds: ["EMAIL", "PHONE"] },
        }),
      ])
    )
    const notes = within(view).getAllByText(/^Shared with /)
    expect(notes.map((note) => note.textContent)).toEqual([
      "Shared with a password hidden",
      "Shared with an email address and a phone number hidden",
    ])
    const [first, , , , second] = within(view).getAllByRole("article", {
      name: "Leo, your agent",
    })
    expect(within(first).getByText("The key is {PASSWORD}.")).toBeTruthy()
    expect(within(first).getByText("Shared with a password hidden")).toBe(
      notes[0]
    )
    expect(
      within(second).getByText(
        "Shared with an email address and a phone number hidden"
      )
    ).toBe(notes[1])
    const nia = within(view).getByRole("article", {
      name: "Nia, Priya's agent",
    })
    expect(within(nia).getByText("Noted {PASSWORD}.")).toBeTruthy()
    expect(within(nia).queryByText(/^Shared with /)).toBeNull()
    expect(notes[0].className).toBe("workspace-side-via")
  })
})

describe("A side conversation's states", () => {
  it("keeps a paused teamspace's conversation readable, and says why nothing can be sent", async () => {
    const view = await openWithPriya()
    opened(
      snapshot([message(1, priya, "Before the pause"), cardForMe(2)], {
        roster: { ...roster, paused: true },
      })
    )
    expect(within(view).getByText("Before the pause")).toBeTruthy()
    const field = composer(view)
    expect(field.hasAttribute("disabled")).toBe(true)
    expect(field.getAttribute("placeholder")).toBe("Launch room is paused")
    expect(
      within(view).getByText(
        "Launch room is paused, so this conversation is read-only until a team admin resumes it."
      )
    ).toBeTruthy()
    expect(
      within(view)
        .getByRole("button", { name: "Who this goes to: Priya" })
        .hasAttribute("disabled")
    ).toBe(true)
    // Nor can a request be decided while it is paused.
    expect(within(view).queryByRole("button", { name: /^Approve/ })).toBeNull()
  })

  it("says the member can't open it once one of them is off the team", async () => {
    const view = await openWithPriya({
      route: ({ path }) =>
        path === sidePath
          ? apiError(404, "peer_not_found", "Not on the teamspace's team.")
          : undefined,
    })
    sideSocket().drop()
    expect(
      await within(view).findByText("You can't open this conversation")
    ).toBeTruthy()
    expect(
      within(view).getByText(
        "Priya may no longer be on Launch room's team, or the teamspace is gone."
      )
    ).toBeTruthy()
    expect(composer(view).hasAttribute("disabled")).toBe(true)
    expect(composer(view).getAttribute("placeholder")).toBe(
      "You can't message Priya here"
    )
  })

  it("says it cannot reach the conversation, and tries again at once when asked", async () => {
    const view = await openWithPriya({
      route: ({ path }) =>
        path === sidePath ? json(undefined, 204) : undefined,
    })
    const socket = opened(snapshot([message(1, priya, "Hi")]))
    socket.drop()
    expect(
      await within(view).findByText(
        /Can't reach this conversation. Trying again…/
      )
    ).toBeTruthy()
    // What is there stays, and the composer still takes a message.
    expect(within(view).getByText("Hi")).toBeTruthy()
    expect(composer(view).hasAttribute("disabled")).toBe(false)
    fireEvent.click(within(view).getByRole("button", { name: "Try again" }))
    expect(socket.reconnects).toBe(1)
    await waitFor(() => expect(socket.readyState).toBe(socket.OPEN))
    socket.says(snapshot([message(1, priya, "Hi")]))
    expect(within(view).queryByText(/Can't reach this conversation/)).toBeNull()
  })

  it("asks the member to sign in again once the session has ended", async () => {
    const view = await openWithPriya({
      route: ({ path }) =>
        path === sidePath || path === "/v1/auth/refresh"
          ? apiError(401, "unauthorized", "access token is required")
          : undefined,
    })
    opened(snapshot([message(1, priya, "Hi")]))
    sideSocket().drop()
    expect(
      await within(view).findByText(
        /Your session has ended. Sign in again to keep messaging./
      )
    ).toBeTruthy()
    expect(within(view).getByRole("button", { name: "Sign in" })).toBeTruthy()
    expect(composer(view).hasAttribute("disabled")).toBe(true)
  })

  it("reads the conversation while it is on screen, and says news aloud", async () => {
    const view = await openWithPriya()
    const socket = opened(snapshot([message(1, priya, "Morning")]))
    expect(socket.said("side-read")).toEqual([{ type: "side-read", seq: 1 }])
    socket.says({
      type: "side-item",
      item: agentMessage(2, {
        agentId: "agt_nia",
        agentName: "Nia",
        ownerId: priya,
        status: "streaming",
      }),
    })
    expect(announced(view)).toBe("Nia is answering.")
    expect(socket.said("side-read").at(-1)).toEqual({
      type: "side-read",
      seq: 2,
    })
    // The answer, written in place, is said as it is done.
    socket.says({
      type: "side-item",
      item: agentMessage(2, {
        agentId: "agt_nia",
        agentName: "Nia",
        ownerId: priya,
        body: "The notes\n\nare **ready**.",
      }),
    })
    expect(announced(view)).toBe("Nia: The notes are ready.")
    socket.says({ type: "side-typing", userId: priya, typing: true })
    expect(within(view).getByText("Priya is typing…")).toBeTruthy()
  })
})

describe("The DMs tab", () => {
  const side: Conversation = {
    kind: "side",
    name: sideName,
    peerId: priya,
    teamspaceId: "tsp_launch",
    teamspaceName: "Launch room",
    lastMessage: {
      authorId: "agt_nia",
      body: "Nia: The notes are ready.",
      createdAt: at(5),
    },
    unread: 2,
    updatedAt: at(5),
  }

  it("opens a side conversation from its list, labelled with its teamspace, with no teamspace open", () => {
    renderPanel({
      inbox: { conversations: [side], unreadTotal: 2, status: "live" },
    })
    fireEvent.click(screen.getByRole("tab", { name: "DMs, 2 unread" }))
    const row = screen.getByRole("button", {
      name: "Open side conversation with Priya Shah in Launch room, 2 unread",
    })
    expect(within(row).getByText("Priya Shah")).toBeTruthy()
    expect(within(row).getByText("Launch room")).toBeTruthy()
    expect(within(row).getByText("Nia: The notes are ready.")).toBeTruthy()

    fireEvent.click(row)
    const view = screen.getByRole("region", {
      name: "Side conversation with Priya Shah in Launch room",
    })
    expect(sideSocket().options).toMatchObject({
      agent: "SideConversationAgent",
      enabled: true,
    })
    expect(document.activeElement).toBe(
      within(view).getByRole("textbox", { name: "Message Priya" })
    )
    const socket = opened(
      snapshot([
        agentMessage(1, {
          agentId: "agt_nia",
          agentName: "Nia",
          ownerId: priya,
          body: "The notes are ready.",
        }),
      ])
    )
    // On screen at its end: read, which clears the inbox's count.
    expect(socket.said("side-read")).toEqual([{ type: "side-read", seq: 1 }])

    fireEvent.click(
      within(view).getByRole("button", { name: "Back to conversations" })
    )
    expect(document.activeElement).toBe(
      screen.getByRole("button", {
        name: "Open side conversation with Priya Shah in Launch room, 2 unread",
      })
    )
  })

  it("says a conversation with someone who has left can't be opened", async () => {
    const gone = "user-gone"
    const goneName = `${org}~tsp_launch~${gone}~${viewerId}`
    renderPanel({
      inbox: {
        conversations: [{ ...side, name: goneName, peerId: gone, unread: 0 }],
        unreadTotal: 0,
        status: "live",
      },
      route: ({ path }) =>
        path === `/agents/side-conversation-agent/${goneName}`
          ? apiError(404, "peer_not_found", "Not on the teamspace's team.")
          : undefined,
    })
    fireEvent.click(screen.getByRole("tab", { name: "DMs" }))
    fireEvent.click(
      screen.getByRole("button", {
        name: "Open side conversation with Former member in Launch room",
      })
    )
    const view = screen.getByRole("region", {
      name: "Side conversation with Former member in Launch room",
    })
    const socket = roomSockets.named(goneName).at(-1)
    socket?.drop()
    expect(
      await within(view).findByText(
        "This person may no longer be on Launch room's team, or the teamspace is gone."
      )
    ).toBeTruthy()
  })

  it("keeps a draft, and whom it goes to, across both places it opens", async () => {
    const { rerender } = renderPanel({
      teamspaceId: "tsp_launch",
      inbox: { conversations: [side], unreadTotal: 2, status: "live" },
    })
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Talk with Priya Shah, 2 unread",
      })
    )
    let view = screen.getByRole("region", { name: /^Side conversation/ })
    opened()
    sendTo(view, /Ask my agent/)
    fireEvent.change(composer(view), { target: { value: "Half-written" } })
    fireEvent.click(
      within(view).getByRole("button", { name: "Back to agents" })
    )

    rerender({ teamspaceId: undefined })
    fireEvent.click(screen.getByRole("tab", { name: "DMs, 2 unread" }))
    fireEvent.click(
      screen.getByRole("button", {
        name: "Open side conversation with Priya Shah in Launch room, 2 unread",
      })
    )
    view = screen.getByRole("region", { name: /^Side conversation/ })
    opened()
    expect(composer(view).value).toBe("Half-written")
    expect(
      within(view).getByRole("button", { name: "Who this goes to: Leo" })
    ).toBeTruthy()
  })
})

describe("On the teamspace page", () => {
  const session: Session = {
    user: { id: viewerId, email: "member@sim.oryxa", display_name: "Member" },
    organizations: [{ id: org, name: "Simulated Org", slug: "org-sim" }],
  }
  const members: Member[] = [
    [viewerId, "Member"],
    [priya, "Priya Shah"],
    ["user-ada", "Ada Lovelace"],
  ].map(([userId, name]) => ({
    organization_id: org,
    user_id: userId,
    email: `${userId}@sim.oryxa`,
    display_name: name,
    role: "member",
    joined_at: "2026-09-01T09:00:00Z",
  }))

  it("offers the teammates while the teamspace is open, and not once the member leaves it", async () => {
    mockApi(({ url }) => {
      const path = url.pathname
      if (path === `${orgPath}/teamspaces`) return json({ teamspaces: [space] })
      if (path === spacePath) return json(space)
      if (path === `${orgPath}/members`) return json({ members })
      return undefined
    })
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <WorkspaceShell
          session={session}
          role="member"
          organizationRole="member"
          selectedOrganization={session.organizations[0]}
          initialMode="team"
        />
      </QueryClientProvider>
    )
    fireEvent.click(
      await screen.findByRole("button", { name: "Manage Launch room" })
    )
    fireEvent.click(screen.getByRole("button", { name: "Open teamspace" }))
    fireEvent.click(screen.getByRole("button", { name: "Open Agents" }))
    const panel = screen.getByRole("complementary", { name: "Agent panel" })
    expect(
      await within(panel).findByRole("button", { name: "Talk with Priya Shah" })
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Back to teamspaces" }))
    expect(
      within(panel).queryByRole("region", { name: "In this teamspace" })
    ).toBeNull()
  })
})

describe("a parley in a side conversation", () => {
  it("is offered in the side conversation's own room", async () => {
    const view = await openWithPriya({ withCalls: true })
    opened()
    fireEvent.click(await within(view).findByRole("button", { name: "Parley" }))
    const room = "/live/org_sim/rooms/side/tsp_launch_user-member_user-priya"
    await waitFor(() =>
      expect(
        vi
          .mocked(fetch)
          .mock.calls.some(
            ([url, init]) =>
              String(url).endsWith(`${room}/call/join`) &&
              init?.method === "POST"
          )
      ).toBe(true)
    )
  })

  it("isn't offered outside the workspace, where there are no calls", async () => {
    const view = await openWithPriya()
    opened()
    expect(within(view).queryByRole("button", { name: "Parley" })).toBeNull()
  })
})
