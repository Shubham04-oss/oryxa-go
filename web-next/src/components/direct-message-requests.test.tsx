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
import { useState } from "react"
import { DirectMessageView, withRequests } from "./direct-message"
import type { DirectMessageLayout } from "./direct-message"
import { dayLabel } from "./direct-message-format"
import { directMessageName } from "@/lib/dms"
import type { DmServerMessage, DmWireMessage } from "@/lib/dms/protocol"
import { OpenRequestProvider } from "@/lib/dms/request-navigation"
import type { DmRequestNote } from "@/lib/dms/request-notes"
import { roomSockets } from "@/test/room-socket"
import type { FakeRoomSocket } from "@/test/room-socket"
import {
  apiError,
  json,
  mockApi,
  org,
  orgPath,
  viewerId,
} from "@/test/teamspaces"

// Every DM socket is a stand-in the test speaks through as the Durable
// Object would (src/test/room-socket.ts), and Go is `mockApi`.
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)

const priya = "user-priya"
const priyaDm = directMessageName(org, viewerId, priya)
const peer = { id: priya, name: "Priya Shah", short: "Priya" }

/** A moment `minutes` before now, as the objects write it. */
function ago(minutes: number): string {
  return new Date(Date.now() - minutes * 60_000).toISOString()
}

/** Yesterday at noon, local: a day that is always "Yesterday". */
function yesterdayNoon(): string {
  const at = new Date()
  at.setDate(at.getDate() - 1)
  at.setHours(12, 0, 0, 0)
  return at.toISOString()
}

function wire(
  seq: number,
  authorId: string,
  body: string,
  createdAt: string
): DmWireMessage {
  return { id: `dmm_${seq}`, seq, authorId, body, createdAt }
}

/** A request the member made of Priya's agent (or Priya of theirs), less what `overrides` change. */
function request(overrides: Partial<DmRequestNote> = {}): DmRequestNote {
  const at = overrides.at ?? ago(20)
  return {
    requestId: "irq_1",
    revision: 1,
    askerId: viewerId,
    ownerId: priya,
    origin: "agent",
    state: "waiting",
    changedAt: at,
    at,
    ...overrides,
  }
}

function snapshot(
  messages: DmWireMessage[] = [],
  requests: DmRequestNote[] = []
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
    ...(requests.length === 0 ? {} : { requests }),
  }
}

/** The DM's socket, opened and sent what the object holds. */
function opened(
  messages: DmWireMessage[] = [],
  requests: DmRequestNote[] = []
): FakeRoomSocket {
  const socket = roomSockets.named(priyaDm).at(-1)
  if (!socket) throw new Error("no socket on the DM")
  if (socket.readyState !== socket.OPEN) socket.open()
  socket.says(snapshot(messages, requests))
  return socket
}

/** What Go says of a request, and every read of it, by request. */
type Go = {
  status: Record<string, string | undefined>
  reads: string[]
}

/** Go's `GET …/agent-requests/{id}` for the member: the status held for the request, and 404 for one Go does not show them. */
function goSays(status: Record<string, string> = {}): Go {
  const go: Go = { status, reads: [] }
  mockApi(({ method, path }) => {
    const match = new RegExp(`^${orgPath}/agent-requests/([^/?]+)$`).exec(path)
    if (method !== "GET" || !match) return undefined
    go.reads.push(match[1])
    const held = go.status[match[1]]
    if (held === "403") return apiError(403, "forbidden", "No.")
    if (held === undefined)
      return apiError(404, "not_found", "No such request.")
    return json({
      id: match[1],
      status: held,
      expires_at: new Date(Date.now() + 24 * 60 * 60_000).toISOString(),
    })
  })
  return go
}

function Conversation({
  layout = "canvas",
  onOpenRequest,
}: {
  layout?: DirectMessageLayout
  onOpenRequest?: (requestId: string) => void
}) {
  const [draft, setDraft] = useState("")
  return (
    <DirectMessageView
      organizationId={org}
      viewerId={viewerId}
      peer={peer}
      layout={layout}
      draft={draft}
      onDraftChange={setDraft}
      onBack={() => undefined}
      onOpenRequest={onOpenRequest}
    />
  )
}

function renderConversation(
  props: Parameters<typeof Conversation>[0] = {},
  around: (ui: React.ReactElement) => React.ReactElement = (ui) => ui
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      {around(<Conversation {...props} />)}
    </QueryClientProvider>
  )
}

/** The request lines in the log, in order. */
function lines(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>("[data-request-id]")]
}

/** What a line says, less its time and its Open. */
function said(line: HTMLElement): string {
  const clone = line.cloneNode(true) as HTMLElement
  for (const part of clone.querySelectorAll("time, button")) part.remove()
  return clone.textContent.replace(/\s+/g, " ").trim()
}

/** What the conversation last said aloud. */
function announced(): string | null | undefined {
  return document.querySelector(".workspace-dm-announcer")?.textContent
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

describe("a request's line in a DM", () => {
  it.each<DirectMessageLayout>(["canvas", "panel"])(
    "stands among the messages where it was first noted, as a quiet line of fixed words, in the %s layout",
    async (layout) => {
      goSays({ irq_1: "pending" })
      renderConversation({ layout })
      opened(
        [
          wire(1, priya, "Morning", ago(60)),
          wire(2, viewerId, "Did my agent reach yours?", ago(5)),
        ],
        [request({ at: ago(20) })]
      )

      const [line] = await waitFor(() => {
        const found = lines()
        expect(found).toHaveLength(1)
        return found
      })
      expect(said(line)).toBe("You asked Priya's agent · waiting")
      expect(line.getAttribute("data-request-state")).toBe("waiting")
      // Between the two messages, by when its line stands.
      const log = screen.getByRole("log", {
        name: "Conversation with Priya Shah",
      })
      const order = [
        ...log.querySelectorAll<HTMLElement>(
          ".workspace-dm-text, [data-request-id]"
        ),
      ].map((part) => (part.dataset.requestId ? "request" : part.textContent))
      expect(order).toEqual(["Morning", "request", "Did my agent reach yours?"])
      // Its time is when the line stands, and nothing of anybody's words is in it.
      expect(within(line).getByText(/\d/).getAttribute("datetime")).toBeTruthy()
      expect(line.textContent).not.toMatch(/irq_1/)
    }
  )

  it("says who asked whose agent from either side, and where when it was Slack", async () => {
    goSays({ irq_1: "pending", irq_2: "pending", irq_3: "pending" })
    renderConversation()
    opened(
      [],
      [
        request({ requestId: "irq_1", at: ago(30) }),
        request({
          requestId: "irq_2",
          askerId: priya,
          ownerId: viewerId,
          at: ago(20),
        }),
        request({
          requestId: "irq_3",
          askerId: priya,
          ownerId: viewerId,
          origin: "slack",
          at: ago(10),
        }),
      ]
    )
    await waitFor(() => expect(lines()).toHaveLength(3))
    expect(lines().map(said)).toEqual([
      "You asked Priya's agent · waiting",
      "Priya asked your agent · waiting",
      "Priya asked your agent in Slack · waiting",
    ])
  })

  it("moves in place as the request goes on, keeps its place, and reads Go again for each revision", async () => {
    const go = goSays({ irq_1: "pending" })
    renderConversation()
    const socket = opened(
      [wire(1, priya, "Morning", ago(60))],
      [request({ at: ago(20) })]
    )
    await waitFor(() => expect(said(lines()[0])).toContain("waiting"))
    expect(go.reads).toEqual(["irq_1"])

    go.status.irq_1 = "approved"
    socket.says({
      type: "dm-request",
      request: request({
        at: ago(20),
        revision: 2,
        state: "approved",
        changedAt: ago(2),
      }),
    })
    await waitFor(() =>
      expect(said(lines()[0])).toBe("You asked Priya's agent · approved")
    )
    expect(lines()).toHaveLength(1)
    expect(go.reads).toEqual(["irq_1", "irq_1"])
    // Where its line stands did not move: still after the message.
    expect(
      screen.getByText("Morning").compareDocumentPosition(lines()[0]) &
        Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy()

    // Answered is final, and Go is read for it too, as for every line.
    go.status.irq_1 = "completed"
    socket.says({
      type: "dm-request",
      request: request({
        at: ago(20),
        revision: 3,
        state: "answered",
        changedAt: ago(1),
      }),
    })
    await waitFor(() =>
      expect(said(lines()[0])).toBe("You asked Priya's agent · answered")
    )
    await waitFor(() => expect(go.reads).toEqual(["irq_1", "irq_1", "irq_1"]))
  })

  it("takes Go's word over the note's while the request can move: answered elsewhere, or expired, which no event says", async () => {
    goSays({ irq_1: "completed", irq_2: "expired", irq_3: "review" })
    renderConversation()
    opened(
      [],
      [
        request({ requestId: "irq_1", state: "waiting", at: ago(30) }),
        request({ requestId: "irq_2", state: "approved", at: ago(20) }),
        request({ requestId: "irq_3", state: "waiting", at: ago(10) }),
      ]
    )
    await waitFor(() =>
      expect(lines().map(said)).toEqual([
        "You asked Priya's agent · answered",
        "You asked Priya's agent · expired",
        "You asked Priya's agent · held",
      ])
    )
  })

  it("says the note's word until Go has answered, and keeps it when Go cannot", async () => {
    const answer = mockApi(({ path }) =>
      path.includes("/agent-requests/")
        ? apiError(503, "unavailable", "Go is down.")
        : undefined
    )
    renderConversation()
    opened([], [request({ state: "held" })])
    expect(said(lines()[0])).toBe("You asked Priya's agent · held")
    await waitFor(() => expect(answer.calls.length).toBeGreaterThan(0))
    expect(said(lines()[0])).toBe("You asked Priya's agent · held")
  })

  it("reads Go for every line, a finished request too, but takes what it says for one that cannot move: no timer for its expiry", async () => {
    const go = goSays({
      irq_0: "completed",
      irq_1: "rejected",
      irq_2: "failed",
      irq_3: "withheld",
    })
    renderConversation()
    opened(
      [],
      ["answered", "declined", "failed", "withheld"].map((state, index) =>
        request({
          requestId: `irq_${index}`,
          state: state as DmRequestNote["state"],
          at: ago(40 - index),
        })
      )
    )
    await waitFor(() => expect(lines()).toHaveLength(4))
    expect(lines().map(said)).toEqual([
      "You asked Priya's agent · answered",
      "You asked Priya's agent · declined",
      "You asked Priya's agent · failed",
      "You asked Priya's agent · not shared",
    ])
    await waitFor(() =>
      expect([...go.reads].sort()).toEqual(["irq_0", "irq_1", "irq_2", "irq_3"])
    )
  })

  it("does not show a finished request's line either when Go does not show the member the request", async () => {
    goSays({ irq_1: "completed" })
    renderConversation()
    opened(
      [wire(1, priya, "Morning", ago(60))],
      [
        request({ requestId: "irq_1", state: "answered", at: ago(30) }),
        // Go knows nothing of this one: a note that names a request that is not there.
        request({ requestId: "irq_ghost", state: "answered", at: ago(20) }),
      ]
    )
    await waitFor(() =>
      expect(lines().map((line) => line.dataset.requestId)).toEqual(["irq_1"])
    )
    expect(screen.getByText("Morning")).toBeTruthy()
  })

  it("is shared by the two views of one DM a page may hold: Go is read once for a request", async () => {
    const go = goSays({ irq_1: "pending" })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <Conversation layout="canvas" />
        <Conversation layout="panel" />
      </QueryClientProvider>
    )
    for (const socket of roomSockets.named(priyaDm)) {
      socket.open()
      socket.says(snapshot([], [request()]))
    }
    await waitFor(() => expect(lines()).toHaveLength(2))
    expect(go.reads).toEqual(["irq_1"])
  })

  it("is not shown when Go does not show the member the request", async () => {
    const go = goSays({ irq_2: "pending", irq_3: "403" })
    renderConversation()
    opened(
      [wire(1, priya, "Morning", ago(60))],
      [
        request({ requestId: "irq_1", at: ago(30) }),
        request({ requestId: "irq_2", at: ago(20) }),
        request({ requestId: "irq_3", at: ago(10) }),
      ]
    )
    await waitFor(() => expect(go.reads).toHaveLength(3))
    await waitFor(() =>
      expect(lines().map((line) => line.dataset.requestId)).toEqual(["irq_2"])
    )
    expect(screen.getByText("Morning")).toBeTruthy()
  })

  it("starts a day of its own when it is the first thing said that day", async () => {
    goSays({ irq_1: "pending" })
    renderConversation()
    opened(
      [wire(1, priya, "Today's word", ago(5))],
      [request({ at: yesterdayNoon() })]
    )
    await waitFor(() => expect(lines()).toHaveLength(1))
    const log = screen.getByRole("log", {
      name: "Conversation with Priya Shah",
    })
    const days = [...log.querySelectorAll(".workspace-dm-day")].map(
      (day) => day.textContent
    )
    expect(days).toEqual([dayLabel(yesterdayNoon()), "Today"])
    expect(days[0]).toBe("Yesterday")
  })

  it("stands above the invitation in a conversation with no message", async () => {
    goSays({ irq_1: "pending" })
    renderConversation()
    opened([], [request()])
    await waitFor(() => expect(lines()).toHaveLength(1))
    expect(screen.getByText("Say hello to Priya")).toBeTruthy()
  })

  it("is gone from a conversation the member may no longer open", async () => {
    goSays({ irq_1: "pending" })
    mockApi(({ path }) => {
      if (
        path === `/agents/direct-message-agent/${encodeURIComponent(priyaDm)}`
      ) {
        return apiError(403, "forbidden", "No.")
      }
      if (path.includes("/agent-requests/")) {
        return json({ id: "irq_1", status: "pending", expires_at: ago(-60) })
      }
      return undefined
    })
    renderConversation()
    const socket = opened([], [request()])
    await waitFor(() => expect(lines()).toHaveLength(1))
    socket.drop()
    await waitFor(() =>
      expect(screen.getByText("You can't message this person")).toBeTruthy()
    )
    expect(lines()).toEqual([])
  })
})

describe("a request's Open", () => {
  it("calls the view's function with the request's id", async () => {
    goSays({ irq_1: "pending" })
    const onOpenRequest = vi.fn()
    renderConversation({ onOpenRequest })
    opened([], [request()])
    const open = await screen.findByRole("button", {
      name: "Open the request: You asked Priya's agent, waiting",
    })
    fireEvent.click(open)
    expect(onOpenRequest).toHaveBeenCalledExactlyOnceWith("irq_1")
  })

  it("takes the shell's function when the view is given none, in every view of the DM", async () => {
    goSays({ irq_1: "pending" })
    const opens = vi.fn()
    renderConversation({}, (ui) => (
      <OpenRequestProvider value={opens}>{ui}</OpenRequestProvider>
    ))
    opened([], [request()])
    fireEvent.click(
      await screen.findByRole("button", { name: /^Open the request/ })
    )
    expect(opens).toHaveBeenCalledExactlyOnceWith("irq_1")
  })

  it("prefers the view's own function to the shell's", async () => {
    goSays({ irq_1: "pending" })
    const shell = vi.fn()
    const own = vi.fn()
    renderConversation({ onOpenRequest: own }, (ui) => (
      <OpenRequestProvider value={shell}>{ui}</OpenRequestProvider>
    ))
    opened([], [request()])
    fireEvent.click(
      await screen.findByRole("button", { name: /^Open the request/ })
    )
    expect(own).toHaveBeenCalledTimes(1)
    expect(shell).not.toHaveBeenCalled()
  })

  it("is not offered when nothing was wired to open a request", async () => {
    goSays({ irq_1: "pending" })
    renderConversation()
    opened([], [request()])
    await waitFor(() => expect(lines()).toHaveLength(1))
    expect(screen.queryByRole("button", { name: /Open/ })).toBeNull()
  })
})

describe("a request that moves while the DM is open", () => {
  it("is said aloud, but not what was there when it opened", async () => {
    goSays({ irq_1: "pending", irq_2: "pending" })
    renderConversation()
    const socket = opened([], [request()])
    await waitFor(() => expect(lines()).toHaveLength(1))
    expect(announced() ?? "").toBe("")

    socket.says({
      type: "dm-request",
      request: request({ revision: 2, state: "approved", changedAt: ago(1) }),
    })
    await waitFor(() =>
      expect(announced()).toBe("You asked Priya's agent: approved")
    )

    // Another request, made by Priya from Slack, is news too.
    socket.says({
      type: "dm-request",
      request: request({
        requestId: "irq_2",
        askerId: priya,
        ownerId: viewerId,
        origin: "slack",
        at: ago(0),
      }),
    })
    await waitFor(() =>
      expect(announced()).toBe("Priya asked your agent in Slack: waiting")
    )
  })
})

describe("merging requests among messages", () => {
  const message = (seq: number, createdAt: string) => ({
    id: `dmm_${seq}`,
    seq,
    authorId: priya,
    body: `line ${seq}`,
    createdAt,
  })
  const note = (requestId: string, at: string) => request({ requestId, at })

  it("puts each before the first message that came after it, and the rest at the end", () => {
    const merged = withRequests(
      [
        message(1, "2026-09-24T09:00:00.000Z"),
        message(2, "2026-09-24T09:10:00.000Z"),
        message(3, "2026-09-24T09:20:00.000Z"),
      ],
      [
        note("a", "2026-09-24T08:00:00.000Z"),
        note("b", "2026-09-24T09:05:00.000Z"),
        note("c", "2026-09-24T09:05:00.000Z"),
        note("d", "2026-09-24T09:30:00.000Z"),
      ]
    )
    expect(
      merged.map((item) => ("authorId" in item ? item.seq : item.id))
    ).toEqual(["a", 1, "b", "c", 2, 3, "d"])
  })

  it("goes before a message that came at the same moment, and leaves messages alone with none", () => {
    const first = message(1, "2026-09-24T09:00:00.000Z")
    expect(
      withRequests([first], [note("a", "2026-09-24T09:00:00.000Z")]).map(
        (item) => ("authorId" in item ? item.seq : item.id)
      )
    ).toEqual(["a", 1])
    expect(withRequests([first], [])).toEqual([first])
    expect(withRequests([], [])).toEqual([])
  })
})
