// @vitest-environment jsdom

import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { useRoomCall } from "./use-room-call"
import type { LiveEvent } from "@/lib/live/client"

const room = { kind: "teamspace", id: "tsp_1" } as const
const base = "/live/org_1/rooms/teamspace/tsp_1"

class FakeSocket extends EventTarget {
  static all: FakeSocket[] = []
  constructor(readonly url: string) {
    super()
    FakeSocket.all.push(this)
  }
  send() {}
  close() {}
  open() {
    this.dispatchEvent(new Event("open"))
  }
  frame(logged: LiveEvent) {
    this.dispatchEvent(
      new MessageEvent("message", {
        data: JSON.stringify({ type: "event", event: logged }),
      })
    )
  }
}

function event(seq: number, type: string): LiveEvent {
  return {
    seq,
    at: "2026-10-25T10:00:00.000Z",
    org: "org_1",
    room,
    actor: { type: "member", id: "usr_ada" },
    run: null,
    type,
    summary: type,
    data: {},
    durationMs: 0,
    private: false,
  }
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

function requests(path: string) {
  return fetchMock.mock.calls.filter(([url]) => String(url).includes(path))
}

beforeEach(() => {
  FakeSocket.all = []
  vi.stubGlobal("WebSocket", FakeSocket)
  vi.stubGlobal("fetch", fetchMock)
  fetchMock.mockImplementation(async (url) => {
    const target = String(url)
    if (target.endsWith("/admit")) return new Response(null, { status: 204 })
    if (target.endsWith(`${base}/call`)) return json({ call: null })
    if (target.includes(`${base}/log?after=0`))
      // A room with a call in its history.
      return json({
        events: [event(1, "call.started"), event(2, "call.ended")],
        next: 2,
        more: false,
      })
    if (target.includes(`${base}/log`))
      return json({ events: [], next: 2, more: false })
    return json({ error: { code: "not_found", message: "no" } }, 404)
  })
})

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

describe("useRoomCall on a room page (T5, T6)", () => {
  it("reads the log once and opens one socket for every hook, beside /call, and refetches /call only for news", async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    renderHook(
      () => [
        useRoomCall("org_1", room),
        useRoomCall("org_1", room),
        useRoomCall("org_1", room),
      ],
      { wrapper }
    )
    await waitFor(() => expect(FakeSocket.all).toHaveLength(1))
    act(() => FakeSocket.all[0].open())
    await waitFor(() => expect(requests("/log?")).toHaveLength(2))
    expect(requests("/log?after=0")).toHaveLength(1)
    // The old call.* events are history: no refetch for them.
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(requests(`${base}/call`)).toHaveLength(1)

    act(() => FakeSocket.all[0].frame(event(3, "call.started")))
    await waitFor(() => expect(requests(`${base}/call`)).toHaveLength(2))
    await new Promise((resolve) => setTimeout(resolve, 20))
    // Three hooks, one refetch.
    expect(requests(`${base}/call`)).toHaveLength(2)
  })
})
