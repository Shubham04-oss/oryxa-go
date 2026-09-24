// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { useTeamspaceRoom } from "./use-teamspace-room"
import { teamspaceKeys } from "@/lib/teamspaces/queries"
import type { Teamspace } from "@/lib/teamspaces/client"
import {
  apiError,
  deferred,
  json,
  mockApi,
  org,
  orgPath,
  roomMessage,
  seatsFor,
  teamspace,
  viewerId,
} from "@/test/teamspaces"

const roomPath = `${orgPath}/teamspaces/tsp_launch`

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

/** A room that answers reads as rooms.Service does, from `messages`. */
function liveRoom(
  space = teamspace(),
  messages = [] as ReturnType<typeof roomMessage>[]
) {
  const seats = seatsFor(space, [viewerId, "user-ada"])
  const held = [...messages]
  const api = mockApi(({ method, url }) => {
    if (url.pathname === `${roomPath}/catchup`) {
      return json({
        brief: { room_id: space.id, revision: 0, through_seq: 0 },
        messages: held,
      })
    }
    if (method === "GET" && url.pathname === `${roomPath}/messages`) {
      const after = Number(url.searchParams.get("after") ?? 0)
      return json({ messages: held.filter((item) => item.seq > after), seats })
    }
    return undefined
  })
  return { ...api, seats, held, space }
}

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("useTeamspaceRoom", () => {
  it("reads nothing when there is nothing it may open", () => {
    const api = mockApi(() => undefined)

    expect(setup(undefined).result.current.status.connection).toBe("closed")
    expect(
      setup(teamspace({ status: "disabled" })).result.current.status
    ).toMatchObject({ connection: "paused", canPost: false })
    expect(
      setup(teamspace({ is_member: false, can_manage: true })).result.current
        .status.connection
    ).toBe("not-member")

    // Reading a room marks the member present in it, so a room the member
    // may not use is never read at all.
    expect(api.calls).toEqual([])
  })

  it("reads the room's latest page, then who is here and the member's own seat", async () => {
    const room = liveRoom(teamspace(), [])
    room.held.push(roomMessage(1, room.seats[2], "Morning, everyone"))
    const { result } = setup(room.space)

    expect(result.current.status.connection).toBe("connecting")
    await waitFor(() => expect(result.current.status.connection).toBe("live"))

    expect(room.calls.map((call) => call.path)).toEqual([
      `${roomPath}/catchup`,
      `${roomPath}/messages?after=0&limit=200`,
    ])
    expect(result.current.messages.map((item) => item.body)).toEqual([
      "Morning, everyone",
    ])
    expect(result.current.presence.here.map((seat) => seat.handle)).toEqual([
      "member",
      "ada",
    ])
    expect(result.current.presence.me?.user_id).toBe(viewerId)
    expect(result.current.status.canPost).toBe(true)
  })

  it("keeps reading every 5 s from where it got to, which keeps the member present", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const room = liveRoom(teamspace(), [])
    room.held.push(roomMessage(1, room.seats[2], "First"))
    const { result } = setup(room.space)
    await waitFor(() => expect(result.current.status.connection).toBe("live"))

    room.held.push(roomMessage(2, room.seats[3], "An agent's answer"))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000)
    })

    await waitFor(() => expect(result.current.messages).toHaveLength(2))
    expect(room.calls.at(-1)?.path).toBe(
      `${roomPath}/messages?after=1&limit=200`
    )
  })

  it("sends, shows the post as outgoing until the room answers, then its replies", async () => {
    const room = liveRoom()
    const answer = deferred<Response>()
    const posts: unknown[] = []
    const reads = room.fetchMock.getMockImplementation()!
    room.fetchMock.mockImplementation(async (input, init = {}) => {
      if (init.method === "POST") {
        posts.push(JSON.parse(String(init.body)))
        return answer.promise
      }
      return reads(input, init)
    })
    const { result } = setup(room.space)
    await waitFor(() => expect(result.current.status.canPost).toBe(true))

    let sent: Promise<boolean> | undefined
    act(() => {
      sent = result.current.send("  Can someone check the rollout?  ")
    })
    expect(posts).toEqual([{ body: "Can someone check the rollout?" }])
    expect(result.current.status.outgoing).toEqual([
      expect.objectContaining({
        body: "Can someone check the rollout?",
        landed: false,
      }),
    ])

    const mine = roomMessage(1, room.seats[0], "Can someone check the rollout?")
    const reply = roomMessage(
      2,
      room.seats[3],
      "Juno here: the rollout is ready."
    )
    room.held.push(mine, reply)
    await act(async () => {
      answer.resolve(json({ messages: [mine, reply] }, 201))
      expect(await sent).toBe(true)
    })

    expect(result.current.status.outgoing).toEqual([])
    expect(result.current.status.sendError).toBeUndefined()
    expect(result.current.messages.map((item) => item.seq)).toEqual([1, 2])
  })

  it("keeps the draft when the room never got the message, and says why", async () => {
    const room = liveRoom()
    const reads = room.fetchMock.getMockImplementation()!
    room.fetchMock.mockImplementation(async (input, init = {}) =>
      init.method === "POST"
        ? apiError(500, "internal", "internal server error")
        : reads(input, init)
    )
    const { result } = setup(room.space)
    await waitFor(() => expect(result.current.status.canPost).toBe(true))

    let kept = true
    await act(async () => {
      kept = await result.current.send("Hello")
    })

    expect(kept).toBe(false)
    expect(result.current.status.sendError).toBe(
      "Couldn't send your message. Try again."
    )
    expect(result.current.status.outgoing).toEqual([])
  })

  it("counts a post that failed after it landed as said", async () => {
    const room = liveRoom()
    const reads = room.fetchMock.getMockImplementation()!
    const holder: { client?: QueryClient } = {}
    room.fetchMock.mockImplementation(async (input, init = {}) => {
      if (init.method === "POST") {
        // The room recorded the message and a poll showed it, then the
        // answer was lost (a proxy timing out while the agents were still
        // talking, say).
        room.held.push(roomMessage(1, room.seats[0], "Hello"))
        await holder.client?.refetchQueries({
          queryKey: teamspaceKeys.room(org, "tsp_launch"),
        })
        return apiError(502, "bad_gateway", "upstream unavailable")
      }
      return reads(input, init)
    })
    const { result, queryClient } = setup(room.space)
    holder.client = queryClient
    await waitFor(() => expect(result.current.status.canPost).toBe(true))

    let kept = false
    await act(async () => {
      kept = await result.current.send("Hello")
    })

    expect(kept).toBe(true)
    await waitFor(() =>
      expect(result.current.status.sendError).toBe(
        "Your message is in the room, but the agents' answers didn't come back."
      )
    )
  })

  it("marks a post landed once a read shows it, while its answer is still out", async () => {
    const room = liveRoom()
    const answer = deferred<Response>()
    const reads = room.fetchMock.getMockImplementation()!
    room.fetchMock.mockImplementation(async (input, init = {}) =>
      init.method === "POST" ? answer.promise : reads(input, init)
    )
    const { result, queryClient } = setup(room.space)
    await waitFor(() => expect(result.current.status.canPost).toBe(true))

    act(() => {
      void result.current.send("Status?")
    })
    room.held.push(roomMessage(1, room.seats[0], "Status?"))
    await act(async () => {
      await queryClient.refetchQueries({
        queryKey: teamspaceKeys.room(org, "tsp_launch"),
      })
    })

    await waitFor(() =>
      expect(result.current.status.outgoing).toEqual([
        expect.objectContaining({ body: "Status?", landed: true }),
      ])
    )
    await act(async () => {
      answer.resolve(json({ messages: [room.held[0]] }, 201))
    })
    await waitFor(() => expect(result.current.status.outgoing).toEqual([]))
  })

  it("answers a paused room's 400 as paused and re-reads the teamspace", async () => {
    const api = mockApi(({ url }) => {
      if (url.pathname.startsWith(roomPath + "/")) {
        return apiError(400, "invalid_request", "this room is paused")
      }
      if (url.pathname === roomPath)
        return json(teamspace({ status: "disabled" }))
      return json({ teamspaces: [] })
    })
    const { result, queryClient } = setup(teamspace())
    const detail = teamspaceKeys.teamspace(org, "tsp_launch")
    queryClient.setQueryData(detail, teamspace())

    await waitFor(() => expect(result.current.status.connection).toBe("paused"))
    expect(result.current.status.canPost).toBe(false)
    // The page's own copy of the teamspace is re-read, so its header and
    // gates learn it was paused.
    await waitFor(() =>
      expect(queryClient.getQueryState(detail)?.isInvalidated).toBe(true)
    )
    expect(api.calls.every((call) => call.method === "GET")).toBe(true)
  })

  it("stops reading a room the member may no longer open", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const api = mockApi(({ url }) =>
      url.pathname.startsWith(roomPath + "/")
        ? apiError(404, "not_found", "room not found")
        : json({ teamspaces: [] })
    )
    const { result } = setup(teamspace())

    await waitFor(() => expect(result.current.status.connection).toBe("gone"))
    const reads = () =>
      api.calls.filter((call) => call.path.startsWith(roomPath + "/")).length
    const before = reads()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_000)
    })
    expect(reads()).toBe(before)
  })

  it("keeps trying a room that failed for a reason that may pass", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const api = mockApi(({ url }) =>
      url.pathname.startsWith(roomPath + "/")
        ? apiError(503, "unavailable", "auth is unavailable")
        : undefined
    )
    const { result } = setup(teamspace())

    await waitFor(() => expect(result.current.status.connection).toBe("error"))
    expect(result.current.status.error).toBe("Couldn't reach the room.")
    const before = api.calls.length
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000)
    })
    expect(api.calls.length).toBeGreaterThan(before)
  })

  it("stops reading once the session has ended, and says so", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const api = mockApi(({ url }) =>
      url.pathname === "/v1/auth/refresh"
        ? new Response(null, { status: 401 })
        : apiError(401, "unauthorized", "session expired")
    )
    const { result } = setup(teamspace())

    await waitFor(() => expect(result.current.status.connection).toBe("error"))
    expect(result.current.status.error).toBe(
      "Your session has ended. Sign in again to read this room."
    )
    const before = api.calls.length
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_000)
    })
    expect(api.calls.length).toBe(before)
  })

  it("says so when the server does not run rooms", async () => {
    mockApi(() => apiError(503, "rooms_unavailable", "not configured"))
    const { result } = setup(teamspace())
    await waitFor(() =>
      expect(result.current.status.connection).toBe("unavailable")
    )
  })

  it("lets a member without a seat read but not post", async () => {
    const space = teamspace()
    const seats = seatsFor(space).filter((seat) => seat.user_id !== viewerId)
    mockApi(({ url }) =>
      url.pathname === `${roomPath}/catchup`
        ? json({ brief: { through_seq: 0 }, messages: [] })
        : json({ messages: [], seats })
    )
    const { result } = setup(space)

    await waitFor(() => expect(result.current.status.connection).toBe("live"))
    expect(result.current.presence.me).toBeUndefined()
    expect(result.current.status.canPost).toBe(false)
    let kept = true
    await act(async () => {
      kept = await result.current.send("Hello")
    })
    expect(kept).toBe(false)
  })
})
