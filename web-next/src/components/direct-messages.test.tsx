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
import { useState } from "react"
import type { ReactElement } from "react"
import { DirectMessageView } from "./direct-message"
import { dayLabel } from "./direct-message-format"
import type { DmPeople, DmPerson } from "./direct-message-people"
import { WorkspaceAgentPanel } from "./workspace-agent-panel"
import { WorkspaceShell } from "./workspace-shell"
import type { Session } from "@/lib/auth/client"
import type { Member } from "@/lib/auth/invitations"
import type { Conversation, Inbox } from "@/lib/dms"
import { directMessageName } from "@/lib/dms"
import type {
  DmServerMessage,
  DmWireMessage,
  InboxServerMessage,
} from "@/lib/dms/protocol"
import { roomSockets } from "@/test/room-socket"
import type { FakeRoomSocket } from "@/test/room-socket"
import {
  apiError,
  json,
  mockApi,
  org,
  orgPath,
  teamspace,
  viewerId,
} from "@/test/teamspaces"

// Every DM, inbox and chat socket is a stand-in the test speaks through as
// the Durable Object would (src/test/room-socket.ts).
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)
vi.mock(
  "@cloudflare/ai-chat/react",
  async () => (await import("@/test/agent-chat")).aiChatReact
)

const session: Session = {
  user: { id: viewerId, email: "member@sim.oryxa", display_name: "Member" },
  organizations: [{ id: org, name: "Simulated Org", slug: "org-sim" }],
}

function member(
  userId: string,
  displayName: string | undefined,
  role: Member["role"],
  email: string
): Member {
  return {
    organization_id: org,
    user_id: userId,
    email,
    ...(displayName ? { display_name: displayName } : {}),
    role,
    joined_at: "2026-09-01T09:00:00Z",
  }
}

const members: Member[] = [
  member(viewerId, "Member", "member", "member@sim.oryxa"),
  member("user-priya", "Priya Shah", "admin", "priya@sim.oryxa"),
  member("user-ada", "Ada Lovelace", "member", "ada@sim.oryxa"),
  member("user-owen", "Owen Park", "owner", "owen@sim.oryxa"),
  member("user-sam", undefined, "member", "sam@sim.oryxa"),
]

const priya = "user-priya"
const priyaDm = directMessageName(org, viewerId, priya)
const inboxName = `${org}~${viewerId}`

/** A moment `minutes` before now, as the objects write it. */
function ago(minutes: number): string {
  return new Date(Date.now() - minutes * 60_000).toISOString()
}

/** Today (local) at `hours`:`minutes`:`seconds`. */
function today(hours: number, minutes: number, seconds = 0): string {
  const at = new Date()
  at.setHours(hours, minutes, seconds, 0)
  return at.toISOString()
}

function dm(
  peerId: string,
  minutesAgo: number,
  fields: Partial<Conversation> & { from?: string; body?: string } = {}
): Conversation {
  const { from, body, ...rest } = fields
  const at = ago(minutesAgo)
  return {
    kind: "dm",
    name: directMessageName(org, viewerId, peerId),
    peerId,
    ...(body
      ? { lastMessage: { authorId: from ?? peerId, body, createdAt: at } }
      : {}),
    unread: 0,
    updatedAt: at,
    ...rest,
  }
}

function wire(
  seq: number,
  authorId: string,
  body: string,
  createdAt: string,
  extra: Partial<DmWireMessage> = {}
): DmWireMessage {
  return { id: `dmm_${seq}`, seq, authorId, body, createdAt, ...extra }
}

function snapshot(
  messages: DmWireMessage[] = [],
  fields: Partial<Extract<DmServerMessage, { type: "dm-snapshot" }>> = {}
): DmServerMessage {
  return {
    type: "dm-snapshot",
    conversation: "dmc_1",
    messages,
    hasOlder: false,
    reads: [
      { userId: priya, seq: 0 },
      { userId: viewerId, seq: 0 },
    ],
    online: [],
    typing: [],
    ...fields,
  }
}

/** The latest socket on an object, opened and sent what it holds. */
function opened(
  name: string,
  message: DmServerMessage | InboxServerMessage
): FakeRoomSocket {
  const socket = roomSockets.named(name).at(-1)
  if (!socket) throw new Error(`no socket on ${name}`)
  if (socket.readyState !== socket.OPEN) socket.open()
  socket.says(message)
  return socket
}

function inboxSays(conversations: Conversation[], unreadTotal = 0) {
  return opened(inboxName, {
    type: "inbox-snapshot",
    conversations,
    unreadTotal,
  })
}

function withQueries(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

/** The services a signed-in shell reads: the members, and one teamspace Ada shares. */
function backend(route?: Parameters<typeof mockApi>[0]) {
  return mockApi(async (call) => {
    const answer = await route?.(call)
    if (answer) return answer
    const path = call.url.pathname
    if (path === `${orgPath}/members`) return json({ members })
    if (path === `${orgPath}/teamspaces`) {
      return json({ teamspaces: [teamspace()] })
    }
    return undefined
  })
}

function renderShell(
  props: Partial<Parameters<typeof WorkspaceShell>[0]> = {}
) {
  return withQueries(
    <WorkspaceShell
      session={session}
      role="member"
      organizationRole="member"
      selectedOrganization={session.organizations[0]}
      initialMode="team"
      initialSection="people"
      {...props}
    />
  )
}

function person(id: string, name: string, spaces: string[] = []): DmPerson {
  return {
    id,
    name,
    email: `${id}@sim.oryxa`,
    role: "member",
    spaces,
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

const panelPeople = peopleOf([
  person("user-ada", "Ada Lovelace", ["Launch room"]),
  person(priya, "Priya Shah"),
  person("user-owen", "Owen Park"),
])

function renderPanel({
  inbox,
  expanded = true,
}: {
  inbox: Inbox
  expanded?: boolean
}) {
  // The Agents tab reads the member's agents; nobody answers here.
  mockApi(() => undefined)
  return withQueries(
    <WorkspaceAgentPanel
      expanded={expanded}
      onExpand={vi.fn()}
      onCollapse={vi.fn()}
      organizationId={org}
      directMessages={{
        viewerId,
        inbox,
        people: panelPeople,
      }}
    />
  )
}

/** A DM on its own, as the shell and the panel mount it, holding its draft. */
function Conversation({ active }: { active: boolean }) {
  const [draft, setDraft] = useState("")
  return (
    <DirectMessageView
      organizationId={org}
      viewerId={viewerId}
      peer={{ id: priya, name: "Priya Shah", short: "Priya" }}
      layout="canvas"
      active={active}
      draft={draft}
      onDraftChange={setDraft}
    />
  )
}

function renderConversation({ active = true }: { active?: boolean } = {}) {
  return withQueries(<Conversation active={active} />)
}

/** What the conversation last said aloud. */
function announced(): string | null | undefined {
  return document.querySelector(".workspace-dm-announcer")?.textContent
}

/** The log's size, which jsdom does not lay out: where the reader is. */
function scrolled(log: HTMLElement, top: number, height = 2000) {
  Object.defineProperty(log, "scrollHeight", {
    configurable: true,
    value: height,
  })
  Object.defineProperty(log, "clientHeight", {
    configurable: true,
    value: 500,
  })
  log.scrollTop = top
  fireEvent.scroll(log)
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
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("Team → People", () => {
  it("lists everyone in the organization but the member, with their role and shared teamspaces, and searches them", async () => {
    backend()
    renderShell()

    const list = await screen.findByRole("list", { name: "People" })
    const rows = within(list).getAllByRole("button")
    expect(rows.map((row) => row.getAttribute("aria-label"))).toEqual([
      "Message Ada Lovelace",
      "Message Owen Park",
      "Message Priya Shah",
      "Message sam@sim.oryxa",
    ])
    const ada = within(list).getByRole("button", {
      name: "Message Ada Lovelace",
    })
    expect(ada.getAttribute("aria-describedby")).toBeTruthy()
    expect(within(ada).getByText("Member · Launch room")).toBeTruthy()
    expect(within(list).getByText("Admin")).toBeTruthy()
    expect(within(list).getByText("Owner")).toBeTruthy()

    const search = screen.getByRole("searchbox", { name: "Search people" })
    fireEvent.change(search, { target: { value: "PRI" } })
    expect(
      within(list)
        .getAllByRole("button")
        .map((row) => row.getAttribute("aria-label"))
    ).toEqual(["Message Priya Shah"])
    fireEvent.change(search, { target: { value: "sam@" } })
    expect(
      within(screen.getByRole("list", { name: "People" })).getByRole("button", {
        name: "Message sam@sim.oryxa",
      })
    ).toBeTruthy()
    fireEvent.change(search, { target: { value: "nobody" } })
    expect(screen.getByText("No people match your search.")).toBeTruthy()
  })

  it("says so honestly while it loads, when it fails, and without a workspace", async () => {
    let failing = true
    backend(({ url }) =>
      url.pathname === `${orgPath}/members` && failing
        ? apiError(503, "unavailable", "down")
        : undefined
    )
    renderShell()
    expect(screen.getByText("Loading people…")).toBeTruthy()
    expect(
      await screen.findByText("Couldn't load the people in your organization.")
    ).toBeTruthy()
    failing = false
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(
      await screen.findByRole("button", { name: "Message Priya Shah" })
    ).toBeTruthy()

    cleanup()
    render(
      <QueryClientProvider client={new QueryClient()}>
        <WorkspaceShell preview initialMode="team" initialSection="people" />
      </QueryClientProvider>
    )
    expect(
      screen.getByText("Sign in to a workspace to message its people.")
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: /^Message / })).toBeNull()
  })

  it("opens a DM in the main canvas: the header names the person, and back returns to People with focus on them", async () => {
    backend()
    const onDirectMessageChange = vi.fn()
    renderShell({ onDirectMessageChange })

    fireEvent.click(
      await screen.findByRole("button", { name: "Message Priya Shah" })
    )
    expect(
      screen.getByRole("heading", { name: "Priya Shah", level: 1 })
    ).toBeTruthy()
    expect(
      screen.queryByRole("heading", { name: "People", level: 1 })
    ).toBeNull()
    expect(screen.getByText("Direct message")).toBeTruthy()
    expect(onDirectMessageChange).toHaveBeenLastCalledWith(priya)
    const socket = roomSockets.named(priyaDm).at(-1)
    expect(socket?.options).toMatchObject({
      agent: "DirectMessageAgent",
      enabled: true,
    })
    expect(screen.getByText("Loading the conversation…")).toBeTruthy()
    // Opened by the member: straight to the composer.
    const composer = screen.getByRole("textbox", { name: "Message Priya Shah" })
    expect(document.activeElement).toBe(composer)

    opened(priyaDm, snapshot([], { online: [priya] }))
    expect(screen.getByText("Say hello to Priya")).toBeTruthy()
    expect(screen.getByText("Online")).toBeTruthy()
    expect(
      document.querySelector(
        '.workspace-header-detail [data-online="true"] [aria-label="Priya Shah"]'
      )
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Back to people" }))
    expect(
      screen.getByRole("heading", { name: "People", level: 1 })
    ).toBeTruthy()
    expect(onDirectMessageChange).toHaveBeenLastCalledWith(null)
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Message Priya Shah" })
    )
  })

  it("opens the DM named in the page's address, without taking focus", async () => {
    backend()
    const onDirectMessageChange = vi.fn()
    renderShell({
      initialMode: "home",
      initialSection: undefined,
      initialDirectMessage: priya,
      onDirectMessageChange,
    })
    expect(
      await screen.findByRole("heading", { name: "Priya Shah", level: 1 })
    ).toBeTruthy()
    expect(
      screen.getByRole("navigation", { name: "Team navigation" })
    ).toBeTruthy()
    expect(roomSockets.named(priyaDm)).toHaveLength(1)
    expect(document.activeElement).toBe(document.body)
    expect(onDirectMessageChange).not.toHaveBeenCalled()

    // Leaving People leaves the DM, and the address follows.
    fireEvent.click(screen.getByRole("button", { name: /^Teamspaces/ }))
    expect(onDirectMessageChange).toHaveBeenLastCalledWith(null)
  })

  it("names someone who has left as a former member, and says they can't be messaged", async () => {
    const gone = "user-gone"
    const goneDm = directMessageName(org, viewerId, gone)
    backend(({ path }) =>
      path === `/agents/direct-message-agent/${goneDm}`
        ? apiError(
            404,
            "peer_not_found",
            "This person is not in your organization."
          )
        : undefined
    )
    renderShell({ initialDirectMessage: gone })
    expect(
      await screen.findByRole("heading", { name: "Former member", level: 1 })
    ).toBeTruthy()
    roomSockets.named(goneDm)[0].drop()
    expect(
      await screen.findByText("You can't message this person")
    ).toBeTruthy()
    const composer = screen.getByRole("textbox", {
      name: "Message Former member",
    })
    expect(composer.hasAttribute("disabled")).toBe(true)
    expect(composer.getAttribute("placeholder")).toBe(
      "You can't message this person"
    )
  })

  it("ignores an address naming the member, or no one a DM could be with", () => {
    backend()
    for (const peer of [viewerId, "not a user"]) {
      renderShell({ initialDirectMessage: peer })
      expect(
        screen.getByRole("heading", { name: "People", level: 1 })
      ).toBeTruthy()
      expect(
        roomSockets.all.some(
          (socket) =>
            socket.options.agent === "DirectMessageAgent" &&
            socket.options.enabled
        )
      ).toBe(false)
      cleanup()
    }
  })
})

describe("Team's recent conversations", () => {
  it("shows the three latest DMs, each unread one marked, and opens one in the canvas", async () => {
    backend()
    renderShell({ initialSection: "teamspaces" })
    const recent = screen.getByRole("region", { name: "Recent conversations" })
    inboxSays(
      [
        dm(priya, 2, { body: "See you at 3", unread: 2 }),
        dm("user-ada", 30, { from: viewerId, body: "Sent the deck" }),
        {
          ...dm("user-owen", 45, { body: "About the launch" }),
          kind: "side",
          name: `${org}~tsp_launch~user-member~user-owen`,
          teamspaceId: "tsp_launch",
          teamspaceName: "Launch room",
        },
        dm("user-owen", 60, { body: "Lunch?" }),
        dm("user-sam", 90, { body: "Thanks!" }),
      ],
      2
    )
    await within(recent).findByRole("button", {
      name: "Open conversation with Priya Shah, 2 unread",
    })
    const items = within(recent).getAllByRole("button", {
      name: /^Open conversation/,
    })
    expect(items.map((item) => item.getAttribute("aria-label"))).toEqual([
      "Open conversation with Priya Shah, 2 unread",
      "Open conversation with Ada Lovelace",
      "Open conversation with Owen Park",
    ])
    expect(within(items[0]).getByText("See you at 3")).toBeTruthy()
    expect(within(items[1]).getByText("You: Sent the deck")).toBeTruthy()
    expect(items[0].getAttribute("data-unread")).toBe("true")
    expect(items[1].getAttribute("data-unread")).toBeNull()

    fireEvent.click(items[0])
    expect(
      screen.getByRole("heading", { name: "Priya Shah", level: 1 })
    ).toBeTruthy()
    expect(items[0].getAttribute("aria-current")).toBe("page")
    // People is not the page while its conversation is.
    expect(
      screen
        .getByRole("button", { name: "People" })
        .getAttribute("aria-current")
    ).toBeNull()

    // The rest are one click away, in the agent panel's DMs tab.
    fireEvent.click(
      within(recent).getByRole("button", { name: "All conversations" })
    )
    const panel = screen.getByRole("complementary", { name: "Agent panel" })
    expect(
      within(panel)
        .getByRole("tab", { name: "DMs, 2 unread" })
        .getAttribute("aria-selected")
    ).toBe("true")
    expect(
      within(panel).getAllByRole("button", {
        name: /^Open (side )?conversation/,
      })
    ).toHaveLength(5)
  })

  it("says when there is nothing to show yet", async () => {
    backend()
    renderShell()
    const recent = screen.getByRole("region", { name: "Recent conversations" })
    expect(within(recent).queryByText("Start a chat from People.")).toBeNull()
    inboxSays([])
    expect(within(recent).getByText("Start a chat from People.")).toBeTruthy()
  })

  it("closes the mobile sidebar after opening a conversation", async () => {
    vi.stubGlobal("matchMedia", () => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
    backend()
    const { container } = renderShell({ initialSection: "teamspaces" })
    const shell = container.querySelector(".workspace-shell")
    inboxSays([dm(priya, 5, { body: "Hi" })])
    fireEvent.click(screen.getByRole("button", { name: "Expand left sidebar" }))
    expect(shell?.getAttribute("data-left")).toBe("expanded")
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Open conversation with Priya Shah",
      })
    )
    expect(shell?.getAttribute("data-left")).toBe("collapsed")
    const composer = screen.getByRole("textbox", { name: "Message Priya Shah" })
    // On a phone, opening it does not raise the keyboard.
    expect(document.activeElement).not.toBe(composer)
    expect(document.activeElement).toBe(
      screen.getByRole("log", { name: "Conversation with Priya Shah" })
    )
  })
})

describe("The DMs tab", () => {
  const conversations: Conversation[] = [
    dm(priya, 3, { body: "See you at 3", unread: 12 }),
    dm("user-ada", 120, { from: viewerId, body: "Sent the deck" }),
    {
      ...dm("user-owen", 60 * 30, { body: "About the launch" }),
      kind: "side",
      name: `${org}~tsp_launch~user-member~user-owen`,
      teamspaceId: "tsp_launch",
      teamspaceName: "Launch room",
      unread: 1,
    },
    dm("user-gone", 60 * 24 * 40, { body: "Bye" }),
  ]
  const inbox: Inbox = { conversations, unreadTotal: 13, status: "live" }

  it("lists each conversation: who, the last message, when, and what is unread; a side conversation with its teamspace", () => {
    renderPanel({ inbox })
    fireEvent.click(screen.getByRole("tab", { name: "DMs, 13 unread" }))

    const list = screen.getByRole("list", { name: "Conversations" })
    const rows = within(list).getAllByRole("button")
    expect(rows.map((row) => row.getAttribute("aria-label"))).toEqual([
      "Open conversation with Priya Shah, 12 unread",
      "Open conversation with Ada Lovelace",
      "Open side conversation with Owen Park in Launch room, 1 unread",
      "Open conversation with Former member",
    ])
    expect(within(rows[0]).getByText("See you at 3")).toBeTruthy()
    expect(within(rows[0]).getByText("12")).toBeTruthy()
    expect(within(rows[0]).getByText("3m")).toBeTruthy()
    expect(within(rows[1]).getByText("You: Sent the deck")).toBeTruthy()
    expect(within(rows[1]).getByText("2h")).toBeTruthy()
    expect(within(rows[1]).queryByText(/unread/)).toBeNull()
    expect(within(rows[2]).getByText("Launch room")).toBeTruthy()
    // The preview is read after the name.
    expect(
      document.getElementById(rows[0].getAttribute("aria-describedby") ?? "")
        ?.textContent
    ).toBe("See you at 3")

    // A side conversation opens in the tab too (side-conversations.test.tsx).
    fireEvent.click(rows[2])
    expect(
      screen.getByRole("region", {
        name: "Side conversation with Owen Park in Launch room",
      })
    ).toBeTruthy()
    expect(
      roomSockets.named(`${org}~tsp_launch~user-member~user-owen`)
    ).toHaveLength(1)
  })

  it("opens a conversation in its compact layout, and goes back to the list with focus on its row", () => {
    renderPanel({ inbox })
    fireEvent.click(screen.getByRole("tab", { name: "DMs, 13 unread" }))
    fireEvent.click(
      screen.getByRole("button", {
        name: "Open conversation with Priya Shah, 12 unread",
      })
    )
    const conversation = screen.getByRole("region", {
      name: "Direct message with Priya Shah",
    })
    expect(
      within(conversation).getByRole("heading", {
        name: "Priya Shah",
        level: 2,
      })
    ).toBeTruthy()
    expect(conversation.getAttribute("data-layout")).toBe("panel")
    expect(document.activeElement).toBe(
      within(conversation).getByRole("textbox", { name: "Message Priya Shah" })
    )
    opened(priyaDm, snapshot([], { online: [priya] }))
    expect(within(conversation).getByText("Online")).toBeTruthy()

    fireEvent.click(
      within(conversation).getByRole("button", {
        name: "Back to conversations",
      })
    )
    expect(screen.queryByRole("region", { name: /^Direct message/ })).toBeNull()
    expect(document.activeElement).toBe(
      screen.getByRole("button", {
        name: "Open conversation with Priya Shah, 12 unread",
      })
    )
  })

  it("starts a new message with anyone in the organization", () => {
    renderPanel({
      inbox: { conversations: [], unreadTotal: 0, status: "live" },
    })
    fireEvent.click(screen.getByRole("tab", { name: "DMs" }))
    expect(screen.getByText("No conversations yet.")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "New message" }))
    const search = screen.getByRole("searchbox", { name: "Search people" })
    expect(document.activeElement).toBe(search)
    const people = screen.getByRole("list", { name: "People" })
    expect(
      within(people)
        .getAllByRole("button")
        .map((row) => row.getAttribute("aria-label"))
    ).toEqual([
      "Message Ada Lovelace",
      "Message Priya Shah",
      "Message Owen Park",
    ])
    expect(within(people).getByText("Member · Launch room")).toBeTruthy()
    fireEvent.change(search, { target: { value: "owen" } })
    fireEvent.click(screen.getByRole("button", { name: "Message Owen Park" }))
    expect(
      screen.getByRole("region", { name: "Direct message with Owen Park" })
    ).toBeTruthy()
    expect(
      roomSockets.named(directMessageName(org, viewerId, "user-owen"))
    ).toHaveLength(1)

    fireEvent.click(
      screen.getByRole("button", { name: "Back to conversations" })
    )
    fireEvent.click(screen.getByRole("button", { name: "New message" }))
    fireEvent.keyDown(
      screen.getByRole("searchbox", { name: "Search people" }),
      {
        key: "Escape",
      }
    )
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "New message" })
    )
  })

  it("says honestly what the list cannot show", () => {
    const { rerender } = renderPanel({
      inbox: { conversations: [], unreadTotal: 0, status: "connecting" },
    })
    fireEvent.click(screen.getByRole("tab", { name: "DMs" }))
    expect(screen.getByText("Loading conversations…")).toBeTruthy()

    const panel = (value: Inbox) => (
      <QueryClientProvider client={new QueryClient()}>
        <WorkspaceAgentPanel
          expanded
          onExpand={vi.fn()}
          onCollapse={vi.fn()}
          organizationId={org}
          directMessages={{
            viewerId,
            inbox: value,
            people: panelPeople,
          }}
        />
      </QueryClientProvider>
    )
    rerender(panel({ conversations: [], unreadTotal: 0, status: "error" }))
    fireEvent.click(screen.getByRole("tab", { name: "DMs" }))
    expect(
      screen.getByText("Can't reach your conversations right now.")
    ).toBeTruthy()
    rerender(
      panel({ conversations: [], unreadTotal: 0, status: "session_expired" })
    )
    expect(
      screen.getByText(
        /Your session has ended. Sign in again to see your messages./
      )
    ).toBeTruthy()
    expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy()
  })

  it("carries the unread total on the collapsed rail's DMs icon, 9+ past nine", () => {
    const { container, rerender } = renderPanel({ inbox, expanded: false })
    const rail = screen.getByRole("button", { name: "Open DMs, 13 unread" })
    expect(within(rail).getByText("9+")).toBeTruthy()

    const panel = (value: Inbox) => (
      <QueryClientProvider client={new QueryClient()}>
        <WorkspaceAgentPanel
          expanded={false}
          onExpand={vi.fn()}
          onCollapse={vi.fn()}
          organizationId={org}
          directMessages={{
            viewerId,
            inbox: value,
            people: panelPeople,
          }}
        />
      </QueryClientProvider>
    )
    // A phone has no rail: its edge control says so instead.
    expect(
      screen.getByRole("button", { name: "Expand agent panel, 13 unread" })
    ).toBeTruthy()
    expect(container.querySelector(".workspace-panel-unread")).toBeTruthy()
    rerender(panel({ ...inbox, unreadTotal: 3 }))
    expect(
      within(
        screen.getByRole("button", { name: "Open DMs, 3 unread" })
      ).getByText("3")
    ).toBeTruthy()
    rerender(panel({ ...inbox, unreadTotal: 0 }))
    expect(screen.getByRole("button", { name: "Open DMs" })).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "Expand agent panel" })
    ).toBeTruthy()
    expect(container.querySelector(".workspace-unread-badge")).toBeNull()
    expect(container.querySelector(".workspace-panel-unread")).toBeNull()
  })
})

describe("A DM conversation", () => {
  it("separates the days and groups each person's messages by the minute", () => {
    renderConversation()
    const yesterday = new Date()
    yesterday.setDate(yesterday.getDate() - 1)
    yesterday.setHours(12, 0, 0, 0)
    const older = new Date(2025, 2, 4, 9, 30)
    opened(
      priyaDm,
      snapshot([
        wire(1, priya, "Long ago", older.toISOString()),
        wire(2, priya, "Yesterday's note", yesterday.toISOString()),
        wire(3, priya, "Morning!", today(0, 1, 5)),
        wire(4, priya, "Are you around?", today(0, 1, 40)),
        wire(5, viewerId, "Yes", today(0, 1, 50)),
        wire(6, priya, "Great", today(0, 2, 10)),
      ])
    )
    const log = screen.getByRole("log", {
      name: "Conversation with Priya Shah",
    })
    const separators = [...log.querySelectorAll(".workspace-dm-day")].map(
      (node) => node.textContent
    )
    expect(separators).toEqual([
      dayLabel(older.toISOString()),
      "Yesterday",
      "Today",
    ])
    expect(separators[0]).toBe(
      new Intl.DateTimeFormat(undefined, {
        day: "numeric",
        month: "short",
        year: "numeric",
      }).format(older)
    )
    const groups = [...log.querySelectorAll(".workspace-dm-group")].map(
      (group) =>
        [...group.querySelectorAll(".workspace-dm-text")].map(
          (text) => text.textContent
        )
    )
    expect(groups).toEqual([
      ["Long ago"],
      ["Yesterday's note"],
      ["Morning!", "Are you around?"],
      ["Yes"],
      ["Great"],
    ])
    const mine = log.querySelectorAll('.workspace-dm-group[data-mine="true"]')
    expect(mine).toHaveLength(1)
    expect(mine[0].textContent).toContain("You,")
  })

  it("sends with Enter, shows it faded until the object confirms it, and keeps Shift+Enter for a new line", () => {
    renderConversation()
    const socket = opened(priyaDm, snapshot())
    const composer = screen.getByRole("textbox", { name: "Message Priya Shah" })
    fireEvent.change(composer, { target: { value: "Hello" } })
    fireEvent.keyDown(composer, { key: "Enter", shiftKey: true })
    expect(socket.said("dm-send")).toEqual([])
    fireEvent.keyDown(composer, { key: "Enter" })
    const [sent] = socket.said("dm-send")
    expect(sent.body).toBe("Hello")
    expect((composer as HTMLTextAreaElement).value).toBe("")

    const pending = screen.getByText("Hello").closest(".workspace-dm-message")
    expect(pending?.getAttribute("data-status")).toBe("sending")
    expect(pending?.getAttribute("aria-busy")).toBe("true")
    socket.says({
      type: "dm-message",
      message: wire(1, viewerId, "Hello", ago(0), { clientId: sent.clientId }),
    })
    const confirmed = screen.getByText("Hello").closest(".workspace-dm-message")
    // The same element, now confirmed.
    expect(confirmed).toBe(pending)
    expect(confirmed?.getAttribute("data-status")).toBeNull()
  })

  it("marks a failed message, and Retry sends it again under the same id", () => {
    renderConversation()
    const socket = opened(priyaDm, snapshot())
    const composer = screen.getByRole("textbox", { name: "Message Priya Shah" })
    fireEvent.change(composer, { target: { value: "Too quick" } })
    fireEvent.keyDown(composer, { key: "Enter" })
    const [sent] = socket.said("dm-send")
    socket.says({
      type: "dm-send-failed",
      clientId: sent.clientId,
      code: "rate_limited",
      message: "You're sending messages too quickly. Wait a moment.",
    })
    const failed = screen
      .getByText("Too quick")
      .closest(".workspace-dm-message")
    expect(failed?.getAttribute("data-status")).toBe("failed")
    expect(within(failed as HTMLElement).getByText("Not sent.")).toBeTruthy()
    expect(announced()).toBe("Your message to Priya Shah wasn't sent.")

    fireEvent.click(
      within(failed as HTMLElement).getByRole("button", { name: "Retry" })
    )
    expect(socket.said("dm-send")).toEqual([sent, sent])
    expect(
      screen
        .getByText("Too quick")
        .closest(".workspace-dm-message")
        ?.getAttribute("data-status")
    ).toBe("sending")
  })

  it("will not send a message over the limit, and says how long it is", () => {
    renderConversation()
    opened(priyaDm, snapshot())
    const composer = screen.getByRole("textbox", { name: "Message Priya Shah" })
    fireEvent.change(composer, { target: { value: "x".repeat(3_900) } })
    // Numbers as the reader's locale writes them.
    expect(screen.getByText(/^3.900 of 4.000 characters$/)).toBeTruthy()
    fireEvent.change(composer, { target: { value: "x".repeat(4_001) } })
    expect(
      screen.getByText(/^Too long to send: 4.001 of 4.000 characters\.$/)
    ).toBeTruthy()
    expect(
      screen
        .getByRole("button", { name: "Send message" })
        .hasAttribute("disabled")
    ).toBe(true)
    fireEvent.keyDown(composer, { key: "Enter" })
    expect(roomSockets.named(priyaDm)[0].said("dm-send")).toEqual([])
  })

  it("says Seen under the member's last message the other person has read, until they reply", () => {
    renderConversation()
    const socket = opened(
      priyaDm,
      snapshot([
        wire(1, priya, "Hi", ago(10)),
        wire(2, viewerId, "Hey!", ago(9)),
        wire(3, viewerId, "Got a minute?", ago(8)),
      ])
    )
    expect(screen.queryByText("Seen")).toBeNull()
    socket.says({ type: "dm-read", userId: priya, seq: 2 })
    const seen = screen.getByText("Seen")
    expect(seen.closest(".workspace-dm-message")?.textContent).toContain("Hey!")
    socket.says({ type: "dm-read", userId: priya, seq: 3 })
    expect(
      screen.getByText("Seen").closest(".workspace-dm-message")?.textContent
    ).toContain("Got a minute?")
    socket.says({ type: "dm-message", message: wire(4, priya, "Sure", ago(1)) })
    expect(screen.queryByText("Seen")).toBeNull()
  })

  it("shows the other person typing, and says when the member is", () => {
    renderConversation()
    const socket = opened(priyaDm, snapshot())
    socket.says({ type: "dm-typing", userId: priya, typing: true })
    expect(screen.getByText("Priya is typing…")).toBeTruthy()
    socket.says({ type: "dm-typing", userId: priya, typing: false })
    expect(screen.queryByText("Priya is typing…")).toBeNull()

    const composer = screen.getByRole("textbox", { name: "Message Priya Shah" })
    fireEvent.change(composer, { target: { value: "W" } })
    fireEvent.change(composer, { target: { value: "" } })
    expect(socket.said("dm-typing")).toEqual([
      { type: "dm-typing", typing: true },
      { type: "dm-typing", typing: false },
    ])
  })

  it("marks the conversation read at its end while on screen, and not while hidden", () => {
    const { unmount } = renderConversation({ active: false })
    let socket = opened(priyaDm, snapshot([wire(1, priya, "Hi", ago(2))]))
    expect(socket.said("dm-read")).toEqual([])
    unmount()

    renderConversation()
    socket = opened(priyaDm, snapshot([wire(1, priya, "Hi", ago(2))]))
    expect(socket.said("dm-read")).toEqual([{ type: "dm-read", seq: 1 }])
    socket.says({
      type: "dm-message",
      message: wire(2, priya, "Hello?", ago(1)),
    })
    expect(socket.said("dm-read").at(-1)).toEqual({ type: "dm-read", seq: 2 })
  })

  it("keeps the reader's place further up, with a pill to the new messages, said aloud", () => {
    renderConversation()
    const socket = opened(
      priyaDm,
      snapshot(
        Array.from({ length: 30 }, (_, index) =>
          wire(
            index + 1,
            index % 2 ? viewerId : priya,
            `Line ${index + 1}`,
            ago(60 - index)
          )
        ),
        {
          reads: [
            { userId: viewerId, seq: 30 },
            { userId: priya, seq: 30 },
          ],
        }
      )
    )
    const log = screen.getByRole("log", {
      name: "Conversation with Priya Shah",
    })
    expect(log.getAttribute("aria-live")).toBe("off")
    scrolled(log, 400)

    socket.says({
      type: "dm-message",
      message: wire(31, priya, "Look at this", ago(0)),
    })
    expect(log.scrollTop).toBe(400)
    const pill = screen.getByRole("button", { name: "1 new message" })
    expect(announced()).toBe("1 new message from Priya Shah below")
    // Not read while the reader is up there.
    expect(socket.said("dm-read")).toEqual([])
    socket.says({
      type: "dm-message",
      message: wire(32, priya, "And this", ago(0)),
    })
    expect(screen.getByRole("button", { name: "2 new messages" })).toBe(pill)

    fireEvent.click(pill)
    expect(log.scrollTop).toBe(2000)
    expect(screen.queryByRole("button", { name: /new messages?$/ })).toBeNull()
    expect(socket.said("dm-read").at(-1)).toEqual({ type: "dm-read", seq: 32 })

    // At the end, a new message is read out in full.
    socket.says({
      type: "dm-message",
      message: wire(33, priya, "Thanks!", ago(0)),
    })
    expect(announced()).toBe("Priya Shah: Thanks!")
  })

  it("loads the page before at the top, keeping the reader's place", async () => {
    renderConversation()
    const page = Array.from({ length: 50 }, (_, index) =>
      wire(index + 51, priya, `Line ${index + 51}`, ago(100 - index))
    )
    const socket = opened(priyaDm, snapshot(page, { hasOlder: true }))
    const log = screen.getByRole("log", {
      name: "Conversation with Priya Shah",
    })
    scrolled(log, 60)
    const [asked] = socket.said("dm-history")
    expect(asked).toMatchObject({ beforeSeq: 51 })
    expect(screen.getByText("Loading earlier messages…")).toBeTruthy()

    Object.defineProperty(log, "scrollHeight", {
      configurable: true,
      value: 3500,
    })
    socket.says({
      type: "dm-history",
      requestId: asked.requestId,
      messages: Array.from({ length: 50 }, (_, index) =>
        wire(index + 1, priya, `Line ${index + 1}`, ago(200 - index))
      ),
      hasOlder: false,
    })
    // The same message stays under the reader: 1,500px more above it.
    expect(log.scrollTop).toBe(60 + 1500)
    await waitFor(() =>
      expect(
        screen.getByText(
          "This is the start of your conversation with Priya Shah."
        )
      ).toBeTruthy()
    )
    expect(screen.getByText("Line 1")).toBeTruthy()
  })

  it("says it cannot reach the conversation, and tries again at once when asked", async () => {
    mockApi(({ path }) =>
      path === `/agents/direct-message-agent/${priyaDm}`
        ? json(undefined, 204)
        : undefined
    )
    renderConversation()
    const socket = opened(priyaDm, snapshot([wire(1, priya, "Hi", ago(3))]))
    socket.drop()
    expect(
      screen.getByText(/Can't reach this conversation. Trying again…/)
    ).toBeTruthy()
    // What is there stays, and the composer still takes a message.
    expect(screen.getByText("Hi")).toBeTruthy()
    expect(
      screen
        .getByRole("textbox", { name: "Message Priya Shah" })
        .hasAttribute("disabled")
    ).toBe(false)
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(socket.reconnects).toBe(1)
    await waitFor(() => expect(socket.readyState).toBe(socket.OPEN))
    socket.says(snapshot([wire(1, priya, "Hi", ago(3))]))
    expect(screen.queryByText(/Can't reach this conversation/)).toBeNull()
  })

  it("says the member can't message someone who left the organization", async () => {
    mockApi(({ path }) =>
      path === `/agents/direct-message-agent/${priyaDm}`
        ? apiError(
            404,
            "peer_not_found",
            "This person is not in your organization."
          )
        : undefined
    )
    renderConversation()
    roomSockets.named(priyaDm)[0].drop()
    expect(
      await screen.findByText("You can't message this person")
    ).toBeTruthy()
    expect(
      screen.getByText("They're no longer in this organization.")
    ).toBeTruthy()
    const composer = screen.getByRole("textbox", { name: "Message Priya Shah" })
    expect(composer.hasAttribute("disabled")).toBe(true)
    expect(composer.getAttribute("placeholder")).toBe("You can't message Priya")
  })

  it("asks the member to sign in again once the session has ended", async () => {
    mockApi(({ path }) =>
      path === `/agents/direct-message-agent/${priyaDm}` ||
      path === "/v1/auth/refresh"
        ? apiError(401, "unauthorized", "access token is required")
        : undefined
    )
    const assign = vi.fn()
    vi.stubGlobal("location", {
      assign,
      hash: "",
      pathname: "/",
      search: `?dm=${priya}`,
    })
    renderConversation()
    const socket = opened(priyaDm, snapshot([wire(1, priya, "Hi", ago(3))]))
    socket.drop()
    expect(
      await screen.findByText(
        /Your session has ended. Sign in again to keep messaging./
      )
    ).toBeTruthy()
    expect(screen.getByText("Hi")).toBeTruthy()
    expect(
      screen
        .getByRole("textbox", { name: "Message Priya Shah" })
        .hasAttribute("disabled")
    ).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }))
    expect(assign).toHaveBeenCalledWith(
      `/auth/sign-in?redirect=${encodeURIComponent(`/?dm=${priya}`)}`
    )
  })
})

describe("One DM in both places", () => {
  it("keeps the main canvas and the panel's DMs tab on the same live conversation", async () => {
    backend()
    renderShell()
    inboxSays([dm(priya, 5, { body: "Hi" })])
    fireEvent.click(
      await screen.findByRole("button", { name: "Message Priya Shah" })
    )
    fireEvent.click(screen.getByRole("button", { name: "Open DMs" }))
    const panel = screen.getByRole("complementary", { name: "Agent panel" })
    fireEvent.click(
      within(panel).getByRole("button", {
        name: "Open conversation with Priya Shah",
      })
    )
    const sockets = roomSockets.named(priyaDm)
    expect(sockets).toHaveLength(2)
    for (const socket of sockets) {
      socket.open()
      socket.says(snapshot([wire(1, priya, "Hi", ago(5))], { online: [priya] }))
    }
    const views = screen.getAllByRole("region", {
      name: "Direct message with Priya Shah",
    })
    expect(views.map((view) => view.getAttribute("data-layout"))).toEqual([
      "canvas",
      "panel",
    ])

    const panelComposer = within(views[1]).getByRole("textbox", {
      name: "Message Priya Shah",
    })
    fireEvent.change(panelComposer, { target: { value: "From the panel" } })
    fireEvent.keyDown(panelComposer, { key: "Enter" })
    const [sent] = sockets[1].said("dm-send")
    expect(sockets[0].said("dm-send")).toEqual([])
    const stored = wire(2, viewerId, "From the panel", ago(0), {
      clientId: sent.clientId,
    })
    act(() => {
      for (const socket of sockets) {
        socket.options.onMessage?.({
          data: JSON.stringify({ type: "dm-message", message: stored }),
        })
      }
    })
    for (const view of views) {
      expect(within(view).getByText("From the panel")).toBeTruthy()
    }
    // Both views know she is online, and so does the list.
    expect(within(views[1]).getByText("Online")).toBeTruthy()
    expect(
      screen.getByRole("heading", { name: "Priya Shah", level: 1 })
        .nextElementSibling?.textContent
    ).toBe("Online")
    fireEvent.click(
      within(views[1]).getByRole("button", { name: "Back to conversations" })
    )
    const row = within(panel).getByRole("button", {
      name: "Open conversation with Priya Shah",
    })
    // The canvas still has her DM open: the list shows what it knows.
    expect(
      row
        .querySelector(".workspace-presence-avatar")
        ?.getAttribute("data-online")
    ).toBe("true")
    fireEvent.click(screen.getByRole("button", { name: "Back to people" }))
    expect(
      row
        .querySelector(".workspace-presence-avatar")
        ?.getAttribute("data-online")
    ).toBeNull()
  })

  it("gives the conversation the visible height on a phone while the keyboard is up", async () => {
    vi.stubGlobal("matchMedia", () => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
    const viewport = Object.assign(new EventTarget(), { height: 420 })
    vi.stubGlobal("visualViewport", viewport)
    backend()
    const { container } = renderShell({ initialDirectMessage: priya })
    const middle = container.querySelector<HTMLElement>(".workspace-middle")
    // It opens once the members list says who she is.
    expect(screen.getByText("Loading the conversation…")).toBeTruthy()
    const composer = await screen.findByRole("textbox", {
      name: "Message Priya Shah",
    })
    fireEvent.focus(composer)
    expect(middle?.getAttribute("data-composing")).toBe("true")
    expect(middle?.style.height).toBe("420px")
    act(() => {
      viewport.height = 300
      viewport.dispatchEvent(new Event("resize"))
    })
    expect(middle?.style.height).toBe("300px")
    fireEvent.blur(composer)
    expect(middle?.getAttribute("data-composing")).toBeNull()
    expect(middle?.style.height).toBe("")
  })
})
