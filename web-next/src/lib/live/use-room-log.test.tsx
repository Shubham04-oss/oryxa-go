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
import { mergeEvents, useRoomLog } from "./use-room-log"

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
