// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
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
import { TeamspaceFocus } from "./teamspace-focus"
import { withMention } from "./teamspace-room"
import type { RoomMessage } from "@/lib/rooms/client"
import type { Teamspace } from "@/lib/teamspaces/client"
import {
  apiError,
  contribution,
  deferred,
  entry,
  json,
  mockApi,
  orgPath,
  roomMessage,
  seatsFor,
  teamspace,
  viewerId,
} from "@/test/teamspaces"

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

const spacePath = `${orgPath}/teamspaces/tsp_launch`

type Options = {
  space?: Teamspace
  messages?: RoomMessage[]
  post?: (body: unknown) => Response | Promise<Response>
  roomAnswer?: () => Response | undefined
  entries?: unknown[]
  files?: unknown[]
}

function backend({
  space = teamspace({ contributions: [contribution()] }),
  messages = [],
  post,
  roomAnswer,
  entries = [],
  files = [],
}: Options = {}) {
  const seats = seatsFor(space, [viewerId, "user-ada"])
  const held = [...messages]
  const api = mockApi(({ method, url, body }) => {
    const path = url.pathname
    if (path === spacePath) return json(space)
    if (path.startsWith(`${spacePath}/`) && roomAnswer) {
      const answer = roomAnswer()
      if (answer) return answer
    }
    if (path === `${spacePath}/catchup`) {
      return json({ brief: { through_seq: 0 }, messages: held })
    }
    if (path === `${spacePath}/messages` && method === "GET") {
      const after = Number(url.searchParams.get("after") ?? 0)
      return json({ messages: held.filter((item) => item.seq > after), seats })
    }
    if (path === `${spacePath}/messages` && method === "POST" && post) {
      return post(body)
    }
    if (path === `${spacePath}/entries`) return json({ entries })
    if (path === `${orgPath}/files`) return json({ files })
    if (path.startsWith(`${orgPath}/files/`)) return json(files[0])
    return undefined
  })
  return { ...api, seats, held, space }
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

function roomReads(calls: { path: string }[]) {
  return calls.filter(
    (call) =>
      call.path.startsWith(`${spacePath}/messages`) ||
      call.path.startsWith(`${spacePath}/catchup`)
  )
}

beforeEach(() => {
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
    api.held.push(
      roomMessage(1, ada, "Morning! Can someone check the rollout?"),
      roomMessage(2, juno, "**Ready.** The rollout checklist is done."),
      roomMessage(3, member, "Thanks, Juno."),
      roomMessage(4, pricing, "Pricing looks right.")
    )
    render()

    expect(
      await screen.findByRole("heading", { name: "Launch room", level: 1 })
    ).toBeTruthy()
    expect(screen.getByText("Teamspace · Active")).toBeTruthy()

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

  it("posts through the pill composer, and shows sending while the room's agents answer", async () => {
    const answer = deferred<Response>()
    const api = backend({ post: () => answer.promise })
    render()
    const field = await screen.findByRole("textbox", {
      name: "Message Launch room",
    })
    await waitFor(() => expect(field.hasAttribute("disabled")).toBe(false))

    fireEvent.change(field, { target: { value: "  Is the rollout ready?  " } })
    fireEvent.keyDown(field, { key: "Enter" })

    await waitFor(() =>
      expect(api.calls.filter((call) => call.method === "POST")).toEqual([
        {
          method: "POST",
          path: `${spacePath}/messages`,
          body: { body: "Is the rollout ready?" },
        },
      ])
    )
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
    api.held.push(mine, reply)
    answer.resolve(json({ messages: [mine, reply] }, 201))

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
    backend({
      post: () =>
        apiError(403, "forbidden", "room is not available to this member"),
    })
    render()
    const field = await screen.findByRole("textbox", {
      name: "Message Launch room",
    })
    await waitFor(() => expect(field.hasAttribute("disabled")).toBe(false))

    fireEvent.change(field, { target: { value: "Hello" } })
    fireEvent.click(screen.getByRole("button", { name: "Send message" }))

    expect(
      await screen.findByText(
        "Only teammates with an active agent can post in this room."
      )
    ).toBeTruthy()
    expect((field as HTMLTextAreaElement).value).toBe("Hello")
  })

  it("mentions a seat from the presence row", async () => {
    backend()
    render()
    const field = await screen.findByRole("textbox", {
      name: "Message Launch room",
    })
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
    expect(screen.getByText("Teamspace · Paused")).toBeTruthy()
    expect(
      screen
        .getByRole("textbox", { name: "Message Launch room" })
        .hasAttribute("disabled")
    ).toBe(true)
    expect(roomReads(api.calls)).toEqual([])
  })

  it("keeps someone who manages the team without being on it out of the room and its work", async () => {
    const api = backend({ space: teamspace({ is_member: false }) })
    render()

    expect(
      await screen.findByText(
        "Only members of the Launch team can join this room."
      )
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Activity" }))
    expect(
      screen.getByText(
        "Only members of the Launch team can read its shared work."
      )
    ).toBeTruthy()
    expect(roomReads(api.calls)).toEqual([])
    expect(api.calls.some((call) => call.path.includes("/entries"))).toBe(false)
  })

  it("says so when the room turns the member away", async () => {
    backend({ roomAnswer: () => apiError(404, "not_found", "room not found") })
    render()
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

  it("maps the work tabs to real data and says plainly what doesn't exist yet", async () => {
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
    const panel = await screen.findByRole("region", {
      name: "Launch room work panel",
    })
    const views = screen.getByRole("group", { name: "Work views" })
    expect(panel.contains(views)).toBe(false)

    // Overview: the description, the status and the contributed agents.
    expect(
      within(panel).getByText("Plans, owners and updates for the next release.")
    ).toBeTruthy()
    expect(within(panel).getByText("Active")).toBeTruthy()
    expect(within(panel).getByText("Pricing")).toBeTruthy()
    expect(within(panel).getByText("Checks the launch pricing")).toBeTruthy()
    expect(within(panel).getByText("Asks its owner first")).toBeTruthy()

    fireEvent.click(within(views).getByRole("button", { name: "Tasks" }))
    expect(
      within(panel).getByText("Teamspaces don't track tasks yet.")
    ).toBeTruthy()

    fireEvent.click(within(views).getByRole("button", { name: "Files" }))
    expect(await within(panel).findByText("launch-checklist.md")).toBeTruthy()
    expect(within(panel).getByText(/^2 KB · added/)).toBeTruthy()
    expect(
      api.calls.some(
        (call) =>
          call.path === `${orgPath}/files?scope=team&team_id=team_launch`
      )
    ).toBe(true)

    fireEvent.click(within(views).getByRole("button", { name: "Activity" }))
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
  })

  it("says so when nothing has been shared or filed yet", async () => {
    backend()
    render()
    const panel = await screen.findByRole("region", {
      name: "Launch room work panel",
    })
    const views = screen.getByRole("group", { name: "Work views" })
    fireEvent.click(within(views).getByRole("button", { name: "Activity" }))
    expect(
      await within(panel).findByText(
        "Nothing has been shared yet. When the room decides something, it lands here."
      )
    ).toBeTruthy()
    fireEvent.click(within(views).getByRole("button", { name: "Files" }))
    expect(
      await within(panel).findByText(
        "No files have been shared with Launch yet."
      )
    ).toBeTruthy()
  })
})
