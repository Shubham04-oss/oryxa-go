// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { useTeamspaceRoom } from "./use-teamspace-room"
import { teamspaceKeys } from "@/lib/teamspaces/queries"
import type { Teamspace } from "@/lib/teamspaces/client"
import type { RoomMessage } from "@/lib/rooms/client"
import { roomGoneCloseCode } from "@/lib/rooms/room-agent"
import { roomSockets } from "@/test/room-socket"
import {
  apiError,
  json,
  mockApi,
  org,
  roomMessage,
  seatsFor,
  teamspace,
  viewerId,
} from "@/test/teamspaces"

vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)

const roomName = `${org}~tsp_launch`
const check = `/agents/teamspace-room-agent/${roomName}`

function setup(
  space: Teamspace | undefined,
  organizationId: string | undefined = org
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  function wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    )
  }
  const hook = renderHook(
    ({ current }: { current: Teamspace | undefined }) =>
      useTeamspaceRoom(organizationId, current, viewerId),
    { wrapper, initialProps: { current: space } }
  )
  return { ...hook, queryClient }
}

const seats = seatsFor(teamspace(), [viewerId])
const [memberSeat, , adaSeat, junoSeat] = seats

/** The room as its object sends it when the socket opens. */
function openRoom(messages: RoomMessage[] = [], here = [viewerId]) {
  const socket = roomSockets.latest
  socket.open()
  socket.state({ seats, here })
  socket.says({ type: "room-snapshot", messages, pending: [], results: [] })
  return socket
}

let visibility: DocumentVisibilityState = "visible"

beforeEach(() => {
  roomSockets.reset()
  visibility = "visible"
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => visibility,
  })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("useTeamspaceRoom", () => {
  it("opens nothing it may not use", () => {
    const api = mockApi(() => undefined)

    expect(setup(undefined).result.current.status.connection).toBe("closed")
    expect(
      setup(teamspace({ is_member: false, can_manage: true })).result.current
        .status.connection
    ).toBe("not-member")

    // Reading a room marks the member present in it, so a room the member
    // may not use is never opened at all.
    expect(roomSockets.all.every((socket) => !socket.options.enabled)).toBe(
      true
    )
    expect(roomSockets.all.flatMap((socket) => socket.sent)).toEqual([])
    expect(api.calls).toEqual([])
  })

  it("opens the teamspace's room object, says whether the tab is visible, and is live once the room arrives", () => {
    const { result } = setup(teamspace())
    const socket = roomSockets.latest
    expect(socket.options).toMatchObject({
      agent: "TeamspaceRoomAgent",
      name: roomName,
      enabled: true,
    })
    expect(result.current.status.connection).toBe("connecting")

    socket.open()
    expect(socket.sent).toEqual([{ type: "room-visibility", visible: true }])
    expect(result.current.status.connection).toBe("connecting")

    openRoom([roomMessage(1, adaSeat, "Morning, everyone")])
    expect(result.current.status.connection).toBe("live")
    expect(result.current.messages.map((item) => item.body)).toEqual([
      "Morning, everyone",
    ])
    expect(result.current.presence.here.map((seat) => seat.handle)).toEqual([
      "member",
    ])
    expect(result.current.presence.me?.user_id).toBe(viewerId)
    expect(result.current.status.canPost).toBe(true)
  })

  it("tells the room whenever the tab is hidden or visible again, while the socket is open", () => {
    setup(teamspace())
    const socket = openRoom()

    visibility = "hidden"
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"))
    })
    visibility = "visible"
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"))
    })
    expect(socket.said("room-visibility")).toEqual([
      { type: "room-visibility", visible: true },
      { type: "room-visibility", visible: false },
      { type: "room-visibility", visible: true },
    ])

    // A closed socket is told as it opens again, not queued a stale word.
    socket.drop()
    visibility = "hidden"
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"))
    })
    expect(socket.said("room-visibility")).toHaveLength(3)
    socket.open()
    expect(socket.said("room-visibility").at(-1)).toEqual({
      type: "room-visibility",
      visible: false,
    })
  })

  it("adds what the room records once per seq, and starts over from a reopened socket's room when it cannot join it up", () => {
    const { result } = setup(teamspace())
    const socket = openRoom([roomMessage(1, adaSeat, "One")])

    socket.says({
      type: "room-messages",
      messages: [
        roomMessage(2, junoSeat, "Two"),
        roomMessage(1, adaSeat, "One"),
      ],
    })
    expect(result.current.messages.map((item) => item.seq)).toEqual([1, 2])

    // Reopened after a while: the room's recent messages meet what the page
    // holds, so they join up.
    socket.drop()
    socket.open()
    socket.says({
      type: "room-snapshot",
      messages: [
        roomMessage(2, junoSeat, "Two"),
        roomMessage(3, adaSeat, "Three"),
      ],
      pending: [],
      results: [],
    })
    expect(result.current.messages.map((item) => item.seq)).toEqual([1, 2, 3])

    // Reopened after so long that messages would be missing between them.
    socket.drop()
    socket.open()
    socket.says({
      type: "room-snapshot",
      messages: [roomMessage(400, adaSeat, "Much later")],
      pending: [],
      results: [],
    })
    expect(result.current.messages.map((item) => item.seq)).toEqual([400])
  })

  it("counts somebody with the room open and visible as here at once, and Go's word for everybody else", () => {
    const { result } = setup(teamspace())
    const socket = openRoom()
    expect(result.current.presence.here.map((seat) => seat.handle)).toEqual([
      "member",
    ])

    socket.state({ seats, here: [viewerId, "user-ada"] })
    expect(result.current.presence.here.map((seat) => seat.handle)).toEqual([
      "member",
      "ada",
    ])
    expect(
      result.current.presence.seats.find((seat) => seat.handle === "ada")
        ?.present
    ).toBe(true)

    // Gone from view, but read by Go in the last 90 s: still here.
    socket.state({
      seats: seatsFor(teamspace(), [viewerId, "user-ada"]),
      here: [viewerId],
    })
    expect(result.current.presence.here).toHaveLength(2)
  })

  it("sends a post, shows it as outgoing until the room records it, then takes the room's answer", async () => {
    const { result, queryClient } = setup(teamspace())
    const socket = openRoom()
    const entries = teamspaceKeys.entries(org, "tsp_launch", 100)
    queryClient.setQueryData(entries, [])

    let sent: Promise<boolean> | undefined
    act(() => {
      sent = result.current.send("  Can someone check the rollout?  ")
    })
    const [posted] = socket.said("room-post")
    expect(posted).toEqual({
      type: "room-post",
      id: expect.any(String),
      body: "Can someone check the rollout?",
    })
    expect(result.current.status.outgoing).toEqual([
      expect.objectContaining({
        body: "Can someone check the rollout?",
        landed: false,
      }),
    ])

    const mine = roomMessage(1, memberSeat, "Can someone check the rollout?")
    socket.says({ type: "room-messages", messages: [mine] })
    expect(result.current.status.outgoing).toEqual([
      expect.objectContaining({ landed: true }),
    ])

    const reply = roomMessage(2, junoSeat, "Juno here: the rollout is ready.")
    socket.says({
      type: "room-posted",
      result: { id: posted.id, ok: true, messages: [mine, reply] },
    })
    expect(await sent).toBe(true)
    await waitFor(() => expect(result.current.status.outgoing).toEqual([]))
    expect(result.current.status.sendError).toBeUndefined()
    expect(result.current.messages.map((item) => item.seq)).toEqual([1, 2])
    // The room publishes what it concludes to the ledger.
    expect(queryClient.getQueryState(entries)?.isInvalidated).toBe(true)
  })

  it("keeps the draft and says why when the room refuses a post", async () => {
    const { result, queryClient } = setup(teamspace())
    const socket = openRoom()
    const detail = teamspaceKeys.teamspace(org, "tsp_launch")
    queryClient.setQueryData(detail, teamspace())
    mockApi(() => json(teamspace()))

    const cases: Array<[Record<string, unknown>, string]> = [
      [
        {
          status: 403,
          code: "forbidden",
          message: "room is not available to this member",
        },
        "Only teammates with an active agent can post in this room.",
      ],
      [
        {
          status: 400,
          code: "invalid_request",
          message: "a message is at most 4000 characters",
        },
        "A message is at most 4000 characters.",
      ],
      [
        { status: 404, code: "not_found", message: "room not found" },
        "This room is no longer available to you.",
      ],
      [
        {
          status: 429,
          code: "rate_limited",
          message: "Too many messages at once.",
        },
        "Too many messages at once. Try again in a moment.",
      ],
      [
        { status: 503, code: "rooms_unavailable", message: "not configured" },
        "Rooms aren't available on this server.",
      ],
      [
        { status: 0, message: "The room could not be reached." },
        "Couldn't send your message. Try again.",
      ],
    ]
    for (const [refusal, words] of cases) {
      let kept: Promise<boolean> | undefined
      act(() => {
        kept = result.current.send("Hello")
      })
      const posted = socket.said("room-post").at(-1)!
      socket.says({
        type: "room-posted",
        result: { id: posted.id, ok: false, ...refusal } as never,
      })
      expect(await kept).toBe(false)
      await waitFor(() => expect(result.current.status.sendError).toBe(words))
      expect(result.current.status.outgoing).toEqual([])
    }
    // A 400 is the teamspace changing: the page's copy is re-read.
    expect(queryClient.getQueryState(detail)?.isInvalidated).toBe(true)
  })

  it("counts a post that failed after it landed as said", async () => {
    const { result } = setup(teamspace())
    const socket = openRoom()

    let kept: Promise<boolean> | undefined
    act(() => {
      kept = result.current.send("Hello")
    })
    const posted = socket.said("room-post")[0]
    // The room recorded it, then the answer was lost.
    socket.says({
      type: "room-messages",
      messages: [roomMessage(1, memberSeat, "Hello")],
    })
    socket.says({
      type: "room-posted",
      result: {
        id: posted.id,
        ok: false,
        status: 502,
        code: "bad_gateway",
        message: "upstream unavailable",
      },
    })
    expect(await kept).toBe(true)
    await waitFor(() =>
      expect(result.current.status.sendError).toBe(
        "Your message is in the room, but the agents' answers didn't come back."
      )
    )
  })

  it("says a post again, once, with a fresh session when the socket's cookie had expired", async () => {
    const api = mockApi(({ url }) =>
      url.pathname === "/v1/auth/me"
        ? json({ user: { id: viewerId }, organizations: [] })
        : undefined
    )
    const { result } = setup(teamspace())
    const socket = openRoom()

    let kept: Promise<boolean> | undefined
    act(() => {
      kept = result.current.send("Hello")
    })
    const first = socket.said("room-post")[0]
    socket.says({
      type: "room-posted",
      result: {
        id: first.id,
        ok: false,
        status: 401,
        code: "session_expired",
        message: "Your session has ended.",
      },
    })
    // The session is read (which refreshes the cookie), the socket reopens
    // with it, and the post goes again under a new id.
    await waitFor(() => expect(socket.said("room-post")).toHaveLength(2))
    expect(api.calls.map((call) => call.path)).toEqual(["/v1/auth/me"])
    expect(socket.reconnects).toBe(1)
    const second = socket.said("room-post")[1]
    expect(second.id).not.toBe(first.id)
    expect(second.body).toBe("Hello")
    socket.says({
      type: "room-posted",
      result: {
        id: second.id,
        ok: true,
        messages: [roomMessage(1, memberSeat, "Hello")],
      },
    })
    expect(await kept).toBe(true)
  })

  it("renews the session when the room says it ended, and says so when it cannot be", async () => {
    mockApi(({ url }) =>
      url.pathname === "/v1/auth/refresh"
        ? new Response(null, { status: 401 })
        : apiError(401, "unauthorized", "session expired")
    )
    const { result } = setup(teamspace())
    const socket = openRoom()

    socket.says({ type: "room-status", status: "session_expired" })
    await waitFor(() =>
      expect(result.current.status).toMatchObject({
        connection: "error",
        error: "Your session has ended. Sign in again to read this room.",
      })
    )
    expect(socket.options.enabled).toBe(false)
    expect(socket.reconnects).toBe(0)
  })

  it("answers a paused room as paused and re-reads the teamspace, as a Go before read-only rooms refuses its reads (400)", async () => {
    mockApi(() => json(teamspace({ status: "disabled" })))
    const { result, queryClient } = setup(teamspace())
    const detail = teamspaceKeys.teamspace(org, "tsp_launch")
    queryClient.setQueryData(detail, teamspace())
    const socket = openRoom([roomMessage(1, adaSeat, "Before the pause")])

    socket.says({
      type: "room-status",
      status: "paused",
      message: "this room is paused",
    })
    expect(result.current.status).toMatchObject({
      connection: "paused",
      canPost: false,
    })
    await waitFor(() =>
      expect(queryClient.getQueryState(detail)?.isInvalidated).toBe(true)
    )
    // What was said stays on screen.
    expect(result.current.messages).toHaveLength(1)

    // Resumed: the room says it is live again.
    socket.says({ type: "room-status", status: "live" })
    expect(result.current.status.connection).toBe("live")
  })

  it("opens a paused teamspace's room read-only: what it holds shows, and nothing can be said", async () => {
    const { result, queryClient } = setup(teamspace({ status: "disabled" }))
    const detail = teamspaceKeys.teamspace(org, "tsp_launch")
    queryClient.setQueryData(detail, teamspace({ status: "disabled" }))
    const socket = roomSockets.latest
    expect(socket.options).toMatchObject({ name: roomName, enabled: true })
    expect(result.current.status).toMatchObject({
      connection: "paused",
      canPost: false,
    })

    // Go's read said `paused`: the room sends itself, then says so.
    openRoom([roomMessage(1, adaSeat, "Before the pause")])
    socket.says({ type: "room-status", status: "paused" })
    expect(result.current.status).toMatchObject({
      connection: "paused",
      canPost: false,
    })
    expect(result.current.messages.map((item) => item.body)).toEqual([
      "Before the pause",
    ])
    socket.says({
      type: "room-messages",
      messages: [roomMessage(2, junoSeat, "Notes from the call")],
    })
    expect(result.current.messages).toHaveLength(2)
    expect(await result.current.send("Hello?")).toBe(false)
    expect(socket.said("room-post")).toEqual([])
    // The page already knew it was paused: nothing to re-read.
    expect(queryClient.getQueryState(detail)?.isInvalidated).toBe(false)

    // Resumed elsewhere: the room is live, and the page's copy is re-read.
    mockApi(() => json(teamspace()))
    socket.says({ type: "room-status", status: "live" })
    await waitFor(() =>
      expect(queryClient.getQueryState(detail)?.isInvalidated).toBe(true)
    )
  })

  it("takes a post refused 409 teamspace_disabled as a paused room, keeping the draft and the room", async () => {
    mockApi(() => json(teamspace({ status: "disabled" })))
    const { result, queryClient } = setup(teamspace())
    const detail = teamspaceKeys.teamspace(org, "tsp_launch")
    queryClient.setQueryData(detail, teamspace())
    const socket = openRoom([roomMessage(1, adaSeat, "Before the pause")])

    let kept: Promise<boolean> | undefined
    act(() => {
      kept = result.current.send("Hello")
    })
    const posted = socket.said("room-post").at(-1)!
    socket.says({
      type: "room-posted",
      result: {
        id: posted.id,
        ok: false,
        status: 409,
        code: "teamspace_disabled",
        message: "teamspace is disabled",
      },
    })
    expect(await kept).toBe(false)
    await waitFor(() =>
      expect(result.current.status).toMatchObject({
        connection: "paused",
        canPost: false,
        sendError: "This room is paused, so your message wasn't sent.",
      })
    )
    expect(result.current.messages).toHaveLength(1)
    await waitFor(() =>
      expect(queryClient.getQueryState(detail)?.isInvalidated).toBe(true)
    )
  })

  it("stops at a room the member may no longer open, by the room's word or its close", async () => {
    mockApi(() => json(teamspace()))
    for (const turnAway of [
      (socket: typeof roomSockets.latest) =>
        socket.says({
          type: "room-status",
          status: "gone",
          message: "room not found",
        }),
      (socket: typeof roomSockets.latest) => socket.drop(roomGoneCloseCode),
    ]) {
      const { result, queryClient, unmount } = setup(teamspace())
      const detail = teamspaceKeys.teamspace(org, "tsp_launch")
      queryClient.setQueryData(detail, teamspace())
      const socket = openRoom()
      turnAway(socket)
      expect(result.current.status).toMatchObject({
        connection: "gone",
        canPost: false,
      })
      expect(socket.options.enabled).toBe(false)
      await waitFor(() =>
        expect(queryClient.getQueryState(detail)?.isInvalidated).toBe(true)
      )
      unmount()
    }
  })

  it("says so when the server does not run rooms", () => {
    const { result } = setup(teamspace())
    const socket = openRoom()
    socket.says({
      type: "room-status",
      status: "unavailable",
      message: "not configured",
    })
    expect(result.current.status.connection).toBe("unavailable")
    expect(socket.options.enabled).toBe(false)
  })

  it("says the room is reconnecting while Go cannot be read, and live once it can", () => {
    const { result } = setup(teamspace())
    const socket = openRoom()
    socket.says({ type: "room-status", status: "error" })
    expect(result.current.status).toMatchObject({
      connection: "error",
      error: "Couldn't reach the room.",
      canPost: true,
    })
    socket.says({ type: "room-status", status: "live" })
    expect(result.current.status.connection).toBe("live")
  })

  it("asks the Worker why a socket closed, since a browser is never told", async () => {
    const answers: Response[] = [new Response(null, { status: 204 })]
    const api = mockApi(({ url }) =>
      url.pathname === check ? answers.shift() : undefined
    )
    const { result } = setup(teamspace())
    const socket = openRoom()

    // Something that may pass: the socket keeps trying.
    socket.drop()
    expect(result.current.status).toMatchObject({
      connection: "error",
      error: "Couldn't reach the room.",
    })
    await waitFor(() =>
      expect(api.calls.map((call) => call.path)).toEqual([check])
    )
    expect(socket.options.enabled).toBe(true)

    // Go no longer lets the member in.
    answers.push(apiError(404, "not_found", "teamspace not found"))
    socket.drop()
    await waitFor(() => expect(result.current.status.connection).toBe("gone"))
    expect(socket.options.enabled).toBe(false)
  })

  it("says it is trying again when the socket cannot open, rather than connecting for ever", async () => {
    const api = mockApi(({ url }) =>
      url.pathname === check ? new Response(null, { status: 204 }) : undefined
    )
    const { result } = setup(teamspace())
    expect(result.current.status.connection).toBe("connecting")
    roomSockets.latest.drop(1000)
    expect(result.current.status).toMatchObject({
      connection: "error",
      error: "Couldn't reach the room.",
    })
    await waitFor(() => expect(api.calls).toHaveLength(1))
    expect(roomSockets.latest.options.enabled).toBe(true)

    openRoom()
    expect(result.current.status.connection).toBe("live")
  })

  it("stops once the Worker says the session has ended", async () => {
    mockApi(({ url }) =>
      url.pathname === "/v1/auth/refresh"
        ? new Response(null, { status: 401 })
        : apiError(401, "unauthorized", "Sign in to open this conversation.")
    )
    const { result } = setup(teamspace())
    roomSockets.latest.drop()
    await waitFor(() =>
      expect(result.current.status).toMatchObject({
        connection: "error",
        error: "Your session has ended. Sign in again to read this room.",
      })
    )
    expect(roomSockets.latest.options.enabled).toBe(false)
  })

  it("learns from a reopened socket what became of posts sent on an earlier one", async () => {
    const { result } = setup(teamspace())
    const socket = openRoom()

    const sends: Array<Promise<boolean>> = []
    act(() => {
      sends.push(result.current.send("Answered while away"))
      sends.push(result.current.send("Still out"))
      sends.push(result.current.send("Never arrived"))
    })
    const [answered, out, lost] = socket.said("room-post")
    socket.drop()
    socket.open()
    // Said on this socket, after it opened: not the snapshot's to judge.
    act(() => {
      sends.push(result.current.send("Said just now"))
    })
    const now = socket.said("room-post")[3]
    socket.says({
      type: "room-snapshot",
      messages: [roomMessage(1, memberSeat, "Answered while away")],
      pending: [out.id],
      results: [
        {
          id: answered.id,
          ok: true,
          messages: [roomMessage(1, memberSeat, "Answered while away")],
        },
      ],
    })
    expect(await sends[0]).toBe(true)
    expect(await sends[2]).toBe(false)
    await waitFor(() =>
      expect(result.current.status.outgoing.map((item) => item.body)).toEqual([
        "Still out",
        "Said just now",
      ])
    )
    expect(result.current.status.sendError).toBe(
      "Couldn't send your message. Try again."
    )
    expect(lost.body).toBe("Never arrived")

    socket.says({
      type: "room-posted",
      result: {
        id: out.id,
        ok: true,
        messages: [roomMessage(2, memberSeat, "Still out")],
      },
    })
    socket.says({
      type: "room-posted",
      result: {
        id: now.id,
        ok: true,
        messages: [roomMessage(3, memberSeat, "Said just now")],
      },
    })
    expect(await sends[1]).toBe(true)
    expect(await sends[3]).toBe(true)
  })

  it("lets a member without a seat read but not post", async () => {
    const { result } = setup(teamspace())
    const socket = roomSockets.latest
    socket.open()
    socket.state({
      seats: seats.filter((seat) => seat.user_id !== viewerId),
      here: [],
    })
    socket.says({
      type: "room-snapshot",
      messages: [],
      pending: [],
      results: [],
    })

    expect(result.current.status.connection).toBe("live")
    expect(result.current.presence.me).toBeUndefined()
    expect(result.current.status.canPost).toBe(false)
    let kept = true
    await act(async () => {
      kept = await result.current.send("Hello")
    })
    expect(kept).toBe(false)
    expect(socket.said("room-post")).toEqual([])
  })
})
