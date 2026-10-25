// @vitest-environment jsdom

import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  admit,
  forgetAdmissions,
  liveSocketUrl,
  livePath,
  parseLiveFrame,
  readLog,
} from "./client"
import type { LiveEvent } from "./client"
import {
  CLOSE_AFTER_MS,
  DEAD_AFTER_MS,
  PING_MS,
  forgetRoomLogs,
  mergeEvents,
  openRoomLogs,
  useRoomLog,
} from "./use-room-log"

const room = { kind: "teamspace", id: "tsp_1" } as const

function event(seq: number, type = "call.started"): LiveEvent {
  return {
    seq,
    at: "2026-10-14T10:00:00.000Z",
    org: "org_1",
    room,
    actor: { type: "member", id: "usr_ada" },
    run: null,
    type,
    summary: `event ${seq}`,
    data: {},
    durationMs: 0,
    private: false,
  }
}

class FakeSocket extends EventTarget {
  static all: FakeSocket[] = []
  sent: string[] = []
  constructor(readonly url: string) {
    super()
    FakeSocket.all.push(this)
  }
  send(data: string) {
    this.sent.push(data)
  }
  close() {
    this.dispatchEvent(new Event("close"))
  }
  open() {
    this.dispatchEvent(new Event("open"))
  }
  frame(data: string) {
    this.dispatchEvent(new MessageEvent("message", { data }))
  }
}

function logAnswer(events: LiveEvent[], more = false) {
  return new Response(
    JSON.stringify({ events, next: events.at(-1)?.seq ?? 0, more }),
    { status: 200, headers: { "Content-Type": "application/json" } }
  )
}

describe("the live client", () => {
  it("names a room's paths on the app's origin", () => {
    expect(livePath("org_1", room, "/call/join")).toBe(
      "/live/org_1/rooms/teamspace/tsp_1/call/join"
    )
    expect(liveSocketUrl("org_1", room, "/live", "https://app.oryxa.in")).toBe(
      "wss://app.oryxa.in/live/org_1/rooms/teamspace/tsp_1/live"
    )
  })

  it("reads an event frame and ignores the rest", () => {
    expect(
      parseLiveFrame(JSON.stringify({ type: "event", event: event(4) }))?.seq
    ).toBe(4)
    expect(parseLiveFrame("pong")).toBeUndefined()
    expect(parseLiveFrame("{nope")).toBeUndefined()
    expect(parseLiveFrame(JSON.stringify({ type: "other" }))).toBeUndefined()
  })

  it("merges events in order, once each", () => {
    expect(
      mergeEvents([event(1), event(2)], [event(2), event(3)]).map((e) => e.seq)
    ).toEqual([1, 2, 3])
    expect(mergeEvents([event(3)], [event(1)]).map((e) => e.seq)).toEqual([
      1, 3,
    ])
  })
})

describe("useRoomLog", () => {
  const fetchMock = vi.fn<typeof fetch>()

  beforeEach(async () => {
    forgetRoomLogs()
    FakeSocket.all = []
    vi.stubGlobal("WebSocket", FakeSocket)
    vi.stubGlobal("fetch", fetchMock)
    // Go admits the member once per room; these tests are about the log.
    forgetAdmissions()
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await admit("org_1", room)
    fetchMock.mockClear()
  })

  afterEach(() => {
    cleanup()
    forgetRoomLogs()
    fetchMock.mockReset()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it("reads the log, then follows the room live", async () => {
    fetchMock.mockResolvedValueOnce(logAnswer([event(1), event(2)]))
    const { result } = renderHook(() => useRoomLog("org_1", room))

    await waitFor(() => expect(FakeSocket.all).toHaveLength(1))
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
      "/live/org_1/rooms/teamspace/tsp_1/log?after=0"
    )
    const socket = FakeSocket.all[0]
    expect(socket.url).toContain("/live/org_1/rooms/teamspace/tsp_1/live")
    fetchMock.mockResolvedValueOnce(logAnswer([event(3)]))
    act(() => socket.open())
    expect(result.current.status).toBe("live")
    // What was appended before the socket opened is read once it has.
    await waitFor(() =>
      expect(String(fetchMock.mock.calls[1]?.[0])).toContain("after=2")
    )
    await waitFor(() =>
      expect(result.current.events.map((e) => e.seq)).toEqual([1, 2, 3])
    )

    act(() => socket.frame(JSON.stringify({ type: "event", event: event(3) })))
    act(() => socket.frame(JSON.stringify({ type: "event", event: event(2) })))
    expect(result.current.events.map((e) => e.seq)).toEqual([1, 2, 3])
  })

  it("reconnects after a drop and reads what it missed", async () => {
    fetchMock.mockResolvedValueOnce(logAnswer([event(1)]))
    const { result } = renderHook(() => useRoomLog("org_1", room))
    await waitFor(() => expect(FakeSocket.all).toHaveLength(1))
    // The read once the socket opens finds nothing new.
    fetchMock.mockResolvedValueOnce(logAnswer([]))
    act(() => FakeSocket.all[0]?.open())
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))

    fetchMock.mockResolvedValueOnce(logAnswer([event(2), event(3)]))
    act(() => FakeSocket.all[0]?.close())
    expect(result.current.status).toBe("reconnecting")

    await waitFor(() => expect(FakeSocket.all).toHaveLength(2), {
      timeout: 3000,
    })
    expect(String(fetchMock.mock.calls[2]?.[0])).toContain("after=1")
    expect(result.current.events.map((e) => e.seq)).toEqual([1, 2, 3])
  })

  it("stops when the room refuses the member", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          error: { code: "forbidden", message: "not a member of this room" },
        }),
        { status: 403, headers: { "Content-Type": "application/json" } }
      )
    )
    const { result } = renderHook(() => useRoomLog("org_1", room))
    await waitFor(() => expect(result.current.status).toBe("refused"))
    expect(result.current.refusal?.status).toBe(403)
    expect(FakeSocket.all).toHaveLength(0)
  })
})

describe("one subscription per room (T5, T6, T12)", () => {
  const fetchMock = vi.fn<typeof fetch>()

  beforeEach(async () => {
    forgetRoomLogs()
    FakeSocket.all = []
    vi.stubGlobal("WebSocket", FakeSocket)
    vi.stubGlobal("fetch", fetchMock)
    forgetAdmissions()
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await admit("org_1", room)
    fetchMock.mockClear()
  })

  afterEach(() => {
    cleanup()
    forgetRoomLogs()
    fetchMock.mockReset()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it("shares one read and one socket between every hook on a room", async () => {
    fetchMock.mockImplementation(async () => logAnswer([]))
    fetchMock.mockResolvedValueOnce(logAnswer([event(1), event(2)]))
    const { result } = renderHook(() => [
      useRoomLog("org_1", room),
      useRoomLog("org_1", { kind: "teamspace", id: "tsp_1" }),
      useRoomLog("org_1", room),
    ])
    await waitFor(() => expect(FakeSocket.all).toHaveLength(1))
    const logReads = () =>
      fetchMock.mock.calls.filter(([url]) => String(url).includes("/log?"))
    expect(logReads()).toHaveLength(1)
    act(() => FakeSocket.all[0]?.open())
    await waitFor(() => expect(logReads()).toHaveLength(2))
    for (const log of result.current) {
      expect(log.events.map((e) => e.seq)).toEqual([1, 2])
      expect(log.status).toBe("live")
      expect(log.history).toBe(2)
    }
    act(() =>
      FakeSocket.all[0]?.frame(
        JSON.stringify({ type: "event", event: event(3) })
      )
    )
    for (const log of result.current) {
      expect(log.events.map((e) => e.seq)).toEqual([1, 2, 3])
    }
    expect(openRoomLogs()).toBe(1)
  })

  it("shows a room's events at once when it is opened again, and reads only what is new", async () => {
    fetchMock.mockImplementation(async () => logAnswer([]))
    fetchMock.mockResolvedValueOnce(logAnswer([event(1), event(2)]))
    const first = renderHook(() => useRoomLog("org_1", room))
    await waitFor(() => expect(first.result.current.events).toHaveLength(2))
    first.unmount()

    const again = renderHook(() => useRoomLog("org_1", room))
    expect(again.result.current.events.map((e) => e.seq)).toEqual([1, 2])
    // The socket was kept for the next hook: nothing is read from the start.
    expect(FakeSocket.all).toHaveLength(1)
    expect(
      fetchMock.mock.calls.filter(([url]) => String(url).includes("after=0"))
    ).toHaveLength(1)
  })

  it("closes a room nobody follows after a while", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    fetchMock.mockImplementation(async () => logAnswer([]))
    const hook = renderHook(() => useRoomLog("org_1", room))
    await waitFor(() => expect(FakeSocket.all).toHaveLength(1))
    hook.unmount()
    expect(openRoomLogs()).toBe(1)
    act(() => vi.advanceTimersByTime(CLOSE_AFTER_MS + 1))
    expect(openRoomLogs()).toBe(0)
  })

  it("counts a socket that stops answering pings as dead and opens another", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    fetchMock.mockImplementation(async () => logAnswer([]))
    const { result } = renderHook(() => useRoomLog("org_1", room))
    await waitFor(() => expect(FakeSocket.all).toHaveLength(1))
    act(() => FakeSocket.all[0]?.open())
    expect(result.current.status).toBe("live")

    act(() => vi.advanceTimersByTime(PING_MS))
    expect(FakeSocket.all[0]?.sent).toEqual(["ping"])
    act(() => FakeSocket.all[0]?.frame("pong"))
    // Pongs keep it alive; silence past the limit does not.
    act(() => vi.advanceTimersByTime(DEAD_AFTER_MS + PING_MS))
    expect(result.current.status).toBe("reconnecting")
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000)
    })
    await waitFor(() => expect(FakeSocket.all).toHaveLength(2))
  })

  it("survives a ping on a socket that can no longer send", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    fetchMock.mockImplementation(async () => logAnswer([]))
    const { result } = renderHook(() => useRoomLog("org_1", room))
    await waitFor(() => expect(FakeSocket.all).toHaveLength(1))
    const socket = FakeSocket.all[0]
    socket.send = () => {
      throw new Error("InvalidStateError")
    }
    act(() => socket.open())
    act(() => vi.advanceTimersByTime(PING_MS))
    expect(result.current.status).toBe("reconnecting")
  })

  it("reads what it missed when the page comes back online", async () => {
    fetchMock.mockImplementation(async () => logAnswer([]))
    fetchMock.mockResolvedValueOnce(logAnswer([event(1)]))
    const { result } = renderHook(() => useRoomLog("org_1", room))
    await waitFor(() => expect(FakeSocket.all).toHaveLength(1))
    act(() => FakeSocket.all[0]?.open())
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter(([url]) => String(url).includes("/log?"))
      ).toHaveLength(2)
    )
    fetchMock.mockResolvedValueOnce(logAnswer([event(2)]))
    act(() => {
      window.dispatchEvent(new Event("online"))
    })
    await waitFor(() =>
      expect(result.current.events.map((e) => e.seq)).toEqual([1, 2])
    )
  })
})

describe("admission", () => {
  const fetchMock = vi.fn<typeof fetch>()

  beforeEach(() => {
    forgetAdmissions()
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    fetchMock.mockReset()
    vi.unstubAllGlobals()
  })

  it("asks Go once per room, before the edge", async () => {
    fetchMock.mockImplementation(async (url) =>
      String(url).endsWith("/admit")
        ? new Response(null, { status: 204 })
        : new Response(JSON.stringify({ events: [], next: 0, more: false }), {
            headers: { "Content-Type": "application/json" },
          })
    )
    await readLog("org_1", room)
    await readLog("org_1", room)
    const urls = fetchMock.mock.calls.map(([url]) => String(url))
    expect(urls[0]).toContain(
      "/v1/organizations/org_1/edge/rooms/teamspace/tsp_1/admit"
    )
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("POST")
    expect(urls.filter((url) => url.endsWith("/admit"))).toHaveLength(1)
    expect(urls.filter((url) => url.includes("/log"))).toHaveLength(2)
  })

  it("lets the edge decide when Go refuses, and asks again after the edge refuses", async () => {
    let edgeAnswer = 403
    fetchMock.mockImplementation(async (url) => {
      if (String(url).endsWith("/admit")) {
        return new Response(
          JSON.stringify({ error: { code: "not_found", message: "no" } }),
          {
            status: 404,
            headers: { "Content-Type": "application/json" },
          }
        )
      }
      return new Response(
        JSON.stringify(
          edgeAnswer === 200
            ? { events: [], next: 0, more: false }
            : {
                error: {
                  code: "forbidden",
                  message: "not a member of this room",
                },
              }
        ),
        { status: edgeAnswer, headers: { "Content-Type": "application/json" } }
      )
    })
    await expect(readLog("org_1", room)).rejects.toMatchObject({ status: 403 })
    edgeAnswer = 200
    await readLog("org_1", room)
    const admits = fetchMock.mock.calls.filter(([url]) =>
      String(url).endsWith("/admit")
    )
    expect(admits).toHaveLength(2)
  })
})
