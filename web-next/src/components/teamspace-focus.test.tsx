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
import type { AvatarIdentity } from "./workspace-avatar"
import type * as AvatarModule from "./workspace-avatar"
import type * as LiveWorkModule from "./live-work"
import { TeamspaceFocus } from "./teamspace-focus"
import { withMention } from "./teamspace-room"
import type { RoomMessage } from "@/lib/rooms/client"
import type { Teamspace } from "@/lib/teamspaces/client"
import { roomSockets } from "@/test/room-socket"
import {
  apiError,
  contribution,
  entry,
  json,
  mockApi,
  orgPath,
  roomMessage,
  seatsFor,
  teamspace,
  viewerId,
} from "@/test/teamspaces"

// The room arrives over its Durable Object's socket; the stand-in socket
// speaks for the object (src/test/room-socket.ts).
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)

vi.mock("./workspace-avatar", async (importOriginal) => {
  const actual = await importOriginal<typeof AvatarModule>()
  return {
    ...actual,
    IdentityAvatar: ({ identity }: { identity: AvatarIdentity }) => (
      <span
        data-testid="identity-avatar"
        data-kind={identity.kind}
        data-seed={identity.id}
      >
        {identity.initials}
      </span>
    ),
  }
})

// The Live card's own content is live-work.test.tsx's; here only whether
// the card shows.
vi.mock("./live-work", async (importOriginal) => ({
  ...(await importOriginal<typeof LiveWorkModule>()),
  RoomLive: () => <p>The room's live work</p>,
}))

const spacePath = `${orgPath}/teamspaces/tsp_launch`
const childrenPath = "/live/org_sim/rooms/teamspace/tsp_launch/children"
const teamPath = `${orgPath}/teams/team_launch`

type Options = {
  space?: Teamspace
  entries?: unknown[]
  files?: unknown[]
  children?: unknown[]
}

function backend({
  space = teamspace({ contributions: [contribution()] }),
  entries = [],
  files = [],
  children,
}: Options = {}) {
  const seats = seatsFor(space, [viewerId, "user-ada"])
  const api = mockApi(({ url, method, body }) => {
    const path = url.pathname
    if (path === spacePath) return json(space)
    if (path === `${spacePath}/read` && method === "POST")
      return json({ last_read_seq: (body as { seq: number }).seq })
    if (path === `${spacePath}/entries`) return json({ entries })
    if (path === `${orgPath}/files`) return json({ files })
    if (path.startsWith(`${orgPath}/files/`)) return json(files[0])
    if (path === childrenPath && children) return json({ children })
    if (path === `${orgPath}/members`) {
      return json({
        members: [
          { user_id: viewerId, email: "member@example.test", role: "owner" },
          {
            user_id: "user-ada",
            email: "ada@example.test",
            display_name: "Ada",
            role: "member",
          },
          {
            user_id: "user-kenji",
            email: "kenji@example.test",
            display_name: "Kenji",
            role: "member",
          },
        ],
      })
    }
    if (path === `${teamPath}/members` && method === "GET") {
      return json({
        members: [viewerId, "user-ada"].map((user_id) => ({
          team_id: "team_launch",
          user_id,
          role: "member",
        })),
      })
    }
    if (path === `${teamPath}/members` && method === "POST") {
      return json({ team_id: "team_launch", ...(body as object) })
    }
    return undefined
  })
  return { ...api, seats, space }
}

/** The room's socket, once the page has opened it: it opens, and the room arrives. */
async function openRoom(
  seats: ReturnType<typeof seatsFor>,
  messages: RoomMessage[] = []
) {
  await waitFor(() =>
    expect(roomSockets.all.some((socket) => socket.options.enabled)).toBe(true)
  )
  const socket = roomSockets.latest
  socket.open()
  socket.state({ seats, here: [viewerId] })
  socket.says({ type: "room-snapshot", messages, pending: [], results: [] })
  return socket
}

function render(onBack = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  renderBase(
    <QueryClientProvider client={client}>
      <TeamspaceFocus
        organizationId="org_sim"
        teamspaceId="tsp_launch"
        viewerId={viewerId}
        onBack={onBack}
      />
    </QueryClientProvider>
  )
  return { client, onBack }
}

/** Whether the page ever asked for the room: its socket, or Go's room directly. */
function roomAsked(calls: { path: string }[]) {
  return (
    roomSockets.all.some((socket) => socket.options.enabled) ||
    calls.some(
      (call) =>
        call.path.startsWith(`${spacePath}/messages`) ||
        call.path.startsWith(`${spacePath}/catchup`)
    )
  )
}

beforeEach(() => {
  roomSockets.reset()
  // Focusing the composer asks whether this is a phone (DESIGN.md §5).
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

describe("TeamspaceFocus", () => {
  it("shows the real title and status, and the room with people and agents told apart", async () => {
    const api = backend()
    const [member, , ada, juno, pricing] = api.seats
    render()

    expect(
      await screen.findByRole("heading", { name: "Launch room", level: 1 })
    ).toBeTruthy()
    await openRoom(api.seats, [
      roomMessage(1, ada, "Morning! Can someone check the rollout?"),
      roomMessage(2, juno, "**Ready.** The rollout checklist is done."),
      roomMessage(3, member, "Thanks, Juno."),
      roomMessage(4, pricing, "Pricing looks right."),
    ])
    // The crumbs lead back to Teamspaces; the hero says who is in.
    const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" })
    expect(crumbs.textContent).toBe("TeamspacesLaunch room")
    expect(screen.getByText("Launch team · 2 people and 3 agents")).toBeTruthy()
    expect(screen.getByRole("img", { name: "Member, Ada" })).toBeTruthy()

    const conversation = screen.getByRole("region", {
      name: "Launch room conversation",
    })
    const lines = await within(conversation).findAllByRole("article")
    expect(lines.map((line) => line.getAttribute("aria-label"))).toEqual([
      "Ada",
      "Juno, Ada's agent",
      "You",
      "Pricing, Specialist",
    ])
    expect(
      lines.map(
        (line) =>
          within(line).getByTestId("identity-avatar").dataset.kind +
          ":" +
          within(line).getByTestId("identity-avatar").dataset.seed
      )
    ).toEqual([
      "user:user-ada",
      "agent:agt_juno",
      `user:${viewerId}`,
      "agent:agt_pricing",
    ])
    // Agents answer in Markdown, rendered as elements rather than HTML.
    expect((await within(lines[1]).findByText("Ready.")).tagName).toBe("STRONG")
    expect(within(lines[0]).getByText(/Can someone check/).tagName).toBe("P")

    expect(within(conversation).getByRole("status").textContent).toBe("2 here")
    const participants = within(conversation).getByRole("group", {
      name: "Participants",
    })
    expect(
      within(participants)
        .getAllByRole("button")
        .map((seat) => seat.getAttribute("aria-label"))
    ).toEqual([
      "Mention Member (you) (@member)",
      "Mention Ada (@ada)",
      "Mention Leo (@leo)",
      "Mention Juno (@juno)",
      "Mention Pricing (@pricing)",
    ])
  })

  it("marks the room read through the newest message on screen, once the teamspace carries read cursors", async () => {
    const api = backend({
      space: teamspace({
        contributions: [contribution()],
        last_read_seq: 1,
        unread_count: 2,
        last_message: {
          seq: 3,
          author_kind: "person",
          author_id: "user-ada",
          author_handle: "ada",
          author_name: "Ada",
          preview: "Done.",
          created_at: "2026-10-16T09:00:00Z",
        },
      }),
    })
    const [, , ada] = api.seats
    render()
    await openRoom(api.seats, [
      roomMessage(1, ada, "Morning!"),
      roomMessage(2, ada, "Rollout?"),
      roomMessage(3, ada, "Done."),
    ])
    await waitFor(
      () =>
        expect(
          api.calls.filter((call) => call.path === `${spacePath}/read`)
        ).toEqual([
          { method: "POST", path: `${spacePath}/read`, body: { seq: 3 } },
        ]),
      { timeout: 3000 }
    )
  })

  it("never marks a room read for a teamspace without read cursors", async () => {
    const api = backend()
    const [, , ada] = api.seats
    render()
    await openRoom(api.seats, [roomMessage(1, ada, "Morning!")])
    await new Promise((resolve) => setTimeout(resolve, 1500))
    expect(api.calls.some((call) => call.path.endsWith("/read"))).toBe(false)
  })

  it("posts through the pill composer, and shows sending while the room's agents answer", async () => {
    const api = backend()
    render()
    const field = await screen.findByRole("textbox", {
      name: "Message Launch room",
    })
    const socket = await openRoom(api.seats)
    await waitFor(() => expect(field.hasAttribute("disabled")).toBe(false))

    fireEvent.change(field, { target: { value: "  Is the rollout ready?  " } })
    fireEvent.keyDown(field, { key: "Enter" })

    await waitFor(() =>
      expect(socket.said("room-post")).toEqual([
        {
          type: "room-post",
          id: expect.any(String),
          body: "Is the rollout ready?",
        },
      ])
    )
    // Only the room's object talks to Go.
    expect(api.calls.filter((call) => call.method === "POST")).toEqual([])
    expect((field as HTMLTextAreaElement).value).toBe("")
    const conversation = screen.getByRole("region", {
      name: "Launch room conversation",
    })
    expect(within(conversation).getByText("Sending…")).toBeTruthy()
    expect(within(conversation).getByText("Is the rollout ready?")).toBeTruthy()
    expect(
      screen.getByText("Sending… agents may take a little while to answer.")
    ).toBeTruthy()

    const [member, , , juno] = api.seats
    const mine = roomMessage(1, member, "Is the rollout ready?")
    const reply = roomMessage(2, juno, "Yes: every step is checked.")
    // The room relays each message as Go records it, then the post's answer.
    socket.says({ type: "room-messages", messages: [mine] })
    socket.says({ type: "room-messages", messages: [reply] })
    socket.says({
      type: "room-posted",
      result: {
        id: socket.said("room-post")[0].id,
        ok: true,
        messages: [mine, reply],
      },
    })

    expect(
      await within(conversation).findByText("Yes: every step is checked.")
    ).toBeTruthy()
    expect(within(conversation).queryByText("Sending…")).toBeNull()
    expect(
      screen.queryByText("Sending… agents may take a little while to answer.")
    ).toBeNull()
    expect(
      within(conversation)
        .getAllByRole("article")
        .map((line) => line.getAttribute("aria-label"))
    ).toEqual(["You", "Juno, Ada's agent"])
  })

  it("keeps the draft and says why when the room refuses a post", async () => {
    const api = backend()
    render()
    const field = await screen.findByRole("textbox", {
      name: "Message Launch room",
    })
    const socket = await openRoom(api.seats)
    await waitFor(() => expect(field.hasAttribute("disabled")).toBe(false))

    fireEvent.change(field, { target: { value: "Hello" } })
    fireEvent.click(screen.getByRole("button", { name: "Send message" }))
    await waitFor(() => expect(socket.said("room-post")).toHaveLength(1))
    socket.says({
      type: "room-posted",
      result: {
        id: socket.said("room-post")[0].id,
        ok: false,
        status: 403,
        code: "forbidden",
        message: "room is not available to this member",
      },
    })

    expect(
      await screen.findByText(
        "Only teammates with an active agent can post in this room."
      )
    ).toBeTruthy()
    expect((field as HTMLTextAreaElement).value).toBe("Hello")
  })

  it("mentions a seat from the presence row", async () => {
    const api = backend()
    render()
    const field = await screen.findByRole("textbox", {
      name: "Message Launch room",
    })
    await openRoom(api.seats)
    await waitFor(() => expect(field.hasAttribute("disabled")).toBe(false))

    fireEvent.click(
      screen.getByRole("button", { name: "Mention Juno (@juno)" })
    )
    expect((field as HTMLTextAreaElement).value).toBe("@juno ")
    expect(withMention("@juno thanks", "Juno")).toBe("@juno thanks")
    expect(withMention("thanks", "ada")).toBe("thanks @ada ")
  })

  it("never reads a paused room, and says who can open it again", async () => {
    const api = backend({
      space: teamspace({ status: "disabled", can_manage: false }),
    })
    render()

    expect(
      await screen.findByText(
        "This teamspace is paused. Its room opens again when a team admin resumes it."
      )
    ).toBeTruthy()
    expect(
      screen.getByText("Paused · Launch team · 2 people and 2 agents")
    ).toBeTruthy()
    expect(
      screen
        .getByRole("textbox", { name: "Message Launch room" })
        .hasAttribute("disabled")
    ).toBe(true)
    expect(roomAsked(api.calls)).toBe(false)
  })

  it("keeps someone who manages the team without being on it out of the room and its work", async () => {
    const api = backend({ space: teamspace({ is_member: false }) })
    render()

    expect(
      await screen.findByText(
        "Only members of the Launch team can join this room."
      )
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("tab", { name: "Activity" }))
    expect(
      screen.getByText(
        "Only members of the Launch team can read its shared work."
      )
    ).toBeTruthy()
    expect(roomAsked(api.calls)).toBe(false)
    expect(api.calls.some((call) => call.path.includes("/entries"))).toBe(false)
  })

  it("says so when the room turns the member away", async () => {
    const api = backend()
    render()
    const socket = await openRoom(api.seats)
    socket.says({
      type: "room-status",
      status: "gone",
      message: "room not found",
    })
    expect(
      await screen.findByText("This room is no longer available to you.")
    ).toBeTruthy()
    expect(
      screen
        .getByRole("textbox", { name: "Message Launch room" })
        .hasAttribute("disabled")
    ).toBe(true)
  })

  it("says when the teamspace itself is gone, and goes back", async () => {
    mockApi(() => apiError(404, "not_found", "teamspace not found"))
    const { onBack } = render()
    expect(
      await screen.findByText("This teamspace is no longer available to you.")
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Back to teamspaces" }))
    expect(onBack).toHaveBeenCalled()
  })

  it("maps the tabs to real data and says plainly what doesn't exist yet", async () => {
    const api = backend({
      entries: [
        entry({
          artifacts: [
            { name: "Rollout plan", uri: "https://docs.example/rollout" },
            { name: "Sneaky", uri: "javascript:alert(1)" },
          ],
          tools_used: ["docs.read"],
        }),
      ],
      files: [
        {
          id: "file_1",
          organization_id: "org_sim",
          scope: "team",
          team_id: "team_launch",
          filename: "launch-checklist.md",
          content_type: "text/markdown",
          purpose: "upload",
          size_bytes: 2048,
          sha256: "x",
          created_by: "user-ada",
          created_at: "2026-09-23T08:00:00Z",
          expires_at: "2026-10-23T08:00:00Z",
        },
      ],
    })
    render()
    const views = await screen.findByRole("tablist", {
      name: "Launch room views",
    })
    expect(
      within(views)
        .getAllByRole("tab")
        .map((tab) => tab.textContent)
    ).toEqual([
      "Room",
      "Plan",
      expect.stringMatching(/^Files/),
      "Parleys",
      "Activity",
    ])
    // Files carries its count once the team's files are read.
    expect(
      await within(views).findByRole("tab", { name: "Files 1" })
    ).toBeTruthy()

    // Room: beside the room, what the teamspace is for and who is in it.
    const side = screen.getByRole("complementary", {
      name: "About Launch room",
    })
    expect(
      within(side).getByText("Plans, owners and updates for the next release.")
    ).toBeTruthy()
    expect(within(side).getByText("Pricing")).toBeTruthy()
    expect(within(side).getByText("Checks the launch pricing")).toBeTruthy()
    expect(within(side).getByText("Asks its owner first")).toBeTruthy()
    // Nothing runs in the room, so there is no Live card.
    expect(within(side).queryByText("Live")).toBeNull()
    // The newest team files, with their kind; the card opens the Files tab.
    expect(within(side).getByText("launch-checklist.md")).toBeTruthy()
    expect(within(side).getByText("MD")).toBeTruthy()
    expect(within(side).getByText(/^added /)).toBeTruthy()
    fireEvent.click(
      within(side).getByRole("button", {
        name: "Files 1, open the Files tab",
      })
    )
    expect(within(views).getByRole("tab", { name: "Files 1" })).toHaveProperty(
      "ariaSelected",
      "true"
    )
    fireEvent.click(within(views).getByRole("tab", { name: "Room" }))

    fireEvent.click(within(views).getByRole("tab", { name: "Plan" }))
    expect(
      screen.getByRole("tabpanel", { name: "Plan" }).textContent
    ).toContain("A shared plan is coming")
    // The room stays mounted behind another tab.
    expect(
      screen.getByRole("textbox", { name: "Message Launch room", hidden: true })
    ).toBeTruthy()

    fireEvent.click(within(views).getByRole("tab", { name: "Parleys" }))
    expect(
      screen.getByRole("tabpanel", { name: "Parleys" }).textContent
    ).toContain("Past parleys will be listed here")

    fireEvent.click(within(views).getByRole("tab", { name: "Files 1" }))
    const files = screen.getByRole("tabpanel", { name: "Files 1" })
    expect(within(files).getByText("launch-checklist.md")).toBeTruthy()
    expect(within(files).getByText(/^2 KB · added/)).toBeTruthy()
    expect(
      api.calls.filter(
        (call) =>
          call.path === `${orgPath}/files?scope=team&team_id=team_launch`
      )
    ).toHaveLength(1)

    fireEvent.click(within(views).getByRole("tab", { name: "Activity" }))
    const panel = screen.getByRole("tabpanel", { name: "Activity" })
    const ledger = await within(panel).findByRole("list", {
      name: "Shared work",
    })
    expect(within(ledger).getByText("Decision")).toBeTruthy()
    expect(within(ledger).getByText("Ship on Thursday")).toBeTruthy()
    expect(within(ledger).getByText("Leo")).toBeTruthy()
    expect(within(ledger).getByText("1 tool used")).toBeTruthy()
    expect(
      within(ledger)
        .getByRole("link", { name: "Rollout plan" })
        .getAttribute("href")
    ).toBe("https://docs.example/rollout")
    // An address an agent wrote is untrusted: an unsafe one stays text.
    expect(within(ledger).queryByRole("link", { name: "Sneaky" })).toBeNull()
    expect(within(ledger).getByText("Sneaky")).toBeTruthy()

    // Arrow keys move between the tabs, from the focused one (a real click
    // focuses it; fireEvent.click doesn't). The library moves focus, and so
    // the selection, a frame later.
    const activity = within(views).getByRole("tab", { name: "Activity" })
    act(() => activity.focus())
    fireEvent.keyDown(activity, { key: "ArrowRight" })
    await waitFor(() =>
      expect(
        within(views)
          .getByRole("tab", { name: "Room" })
          .getAttribute("aria-selected")
      ).toBe("true")
    )
  })

  it("says so when nothing has been shared or filed yet", async () => {
    backend()
    render()
    const views = await screen.findByRole("tablist", {
      name: "Launch room views",
    })
    fireEvent.click(within(views).getByRole("tab", { name: "Activity" }))
    expect(
      await screen.findByText(
        "Nothing has been shared yet. When the room decides something, it lands here."
      )
    ).toBeTruthy()
    fireEvent.click(await within(views).findByRole("tab", { name: "Files 0" }))
    expect(
      await screen.findByText("No files have been shared with Launch yet.")
    ).toBeTruthy()
  })

  it("shows the Live card only while something runs in the room", async () => {
    backend({
      children: [
        {
          kind: "sandbox",
          name: "shared",
          status: "running",
          createdAt: "2026-10-15T08:00:00Z",
          lastUsedAt: "2026-10-15T08:05:00Z",
        },
      ],
    })
    render()
    const side = await screen.findByRole("complementary", {
      name: "About Launch room",
    })
    expect(await within(side).findByText("The room's live work")).toBeTruthy()
    expect(within(side).getByRole("heading", { name: "Live" })).toBeTruthy()
  })

  it("leaves the Live card out once everything in the room has stopped", async () => {
    const api = backend({
      children: [
        {
          kind: "sandbox",
          name: "shared",
          status: "stopped",
          createdAt: "2026-10-15T08:00:00Z",
          lastUsedAt: "2026-10-15T08:05:00Z",
        },
      ],
    })
    render()
    const side = await screen.findByRole("complementary", {
      name: "About Launch room",
    })
    const views = screen.getByRole("tablist", { name: "Launch room views" })
    // Once the children and the (empty) files are read, neither card shows.
    await within(views).findByRole("tab", { name: "Files 0" })
    await waitFor(() =>
      expect(api.calls.some((call) => call.path === childrenPath)).toBe(true)
    )
    // Let the children's answer land before looking for the card.
    await act(async () => {})
    expect(within(side).queryByRole("heading", { name: "Live" })).toBeNull()
    expect(within(side).queryByText("The room's live work")).toBeNull()
    expect(within(side).queryByRole("heading", { name: /^Files/ })).toBeNull()
  })

  it("offers Invite only to whoever manages the team, and adds a teammate", async () => {
    const api = backend()
    render()
    fireEvent.click(await screen.findByRole("button", { name: "Invite" }))
    const dialog = await screen.findByRole("dialog", {
      name: "Invite to Launch room",
    })
    // Only people not yet on the team are offered.
    const people = await within(dialog).findByRole("list", {
      name: "People to add",
    })
    expect(
      within(people)
        .getAllByRole("listitem")
        .map((item) => item.querySelector("strong")?.textContent)
    ).toEqual(["Kenji"])
    fireEvent.click(within(people).getByRole("button", { name: "Add Kenji" }))
    expect(
      await within(people).findByRole("button", { name: "Kenji added" })
    ).toBeTruthy()
    expect(
      api.calls.filter(
        (call) => call.method === "POST" && call.path === `${teamPath}/members`
      )
    ).toEqual([
      {
        method: "POST",
        path: `${teamPath}/members`,
        body: { user_id: "user-kenji", role: "member" },
      },
    ])
  })

  it("hides Invite from a member who doesn't manage the team", async () => {
    backend({ space: teamspace({ can_manage: false }) })
    render()
    expect(
      await screen.findByRole("heading", { name: "Launch room", level: 1 })
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Invite" })).toBeNull()
  })
})
