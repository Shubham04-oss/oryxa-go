// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  MARK_READ_DELAY_MS,
  MARK_READ_RETRY_MS,
  useMarkTeamspaceRead,
} from "./use-teamspace-read"
import { teamspaceKeys } from "@/lib/teamspaces/queries"
import type { Teamspace } from "@/lib/teamspaces/client"

const tracked = {
  id: "tsp_1",
  organization_id: "org_1",
  is_member: true,
  last_read_seq: 40,
  unread_count: 2,
  last_message: { seq: 42 },
} as unknown as Teamspace

const fetchMock = vi.fn<typeof fetch>()
let visibility: DocumentVisibilityState = "visible"

beforeEach(() => {
  vi.useFakeTimers()
  visibility = "visible"
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => visibility,
  })
  vi.stubGlobal("fetch", fetchMock)
  fetchMock.mockImplementation(async (_url, init) => {
    const { seq } = JSON.parse(String(init?.body)) as { seq: number }
    return new Response(JSON.stringify({ last_read_seq: seq }), {
      headers: { "Content-Type": "application/json" },
    })
  })
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

function setup(space: Teamspace, seenSeq: number | undefined) {
  const queryClient = new QueryClient()
  queryClient.setQueryData(teamspaceKeys.list("org_1"), [space])
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  )
  const hook = renderHook(
    ({ seen }: { seen: number | undefined }) =>
      useMarkTeamspaceRead(space, seen),
    { wrapper, initialProps: { seen: seenSeq } }
  )
  return { queryClient, hook }
}

function marks() {
  return fetchMock.mock.calls.map(([url, init]) => [
    String(url),
    init?.method,
    init?.body,
  ])
}

describe("useMarkTeamspaceRead", () => {
  it("marks the room read through what was seen, after a moment, and clears the count at once", async () => {
    const { queryClient } = setup(tracked, 42)
    expect(fetchMock).not.toHaveBeenCalled()
    await act(() => vi.advanceTimersByTimeAsync(MARK_READ_DELAY_MS))
    expect(marks()).toEqual([
      ["/v1/organizations/org_1/teamspaces/tsp_1/read", "POST", '{"seq":42}'],
    ])
    expect(
      queryClient.getQueryData<Teamspace[]>(teamspaceKeys.list("org_1"))?.[0]
    ).toMatchObject({ last_read_seq: 42, unread_count: 0 })
  })

  it("only moves forward", async () => {
    const { hook } = setup(tracked, 42)
    await act(() => vi.advanceTimersByTimeAsync(MARK_READ_DELAY_MS))
    hook.rerender({ seen: 41 })
    await act(() => vi.advanceTimersByTimeAsync(MARK_READ_DELAY_MS))
    hook.rerender({ seen: 43 })
    await act(() => vi.advanceTimersByTimeAsync(MARK_READ_DELAY_MS))
    expect(marks().map(([, , body]) => body)).toEqual([
      '{"seq":42}',
      '{"seq":43}',
    ])
  })

  it("waits while the tab is hidden", async () => {
    visibility = "hidden"
    setup(tracked, 42)
    await act(() => vi.advanceTimersByTimeAsync(MARK_READ_DELAY_MS * 3))
    expect(fetchMock).not.toHaveBeenCalled()
    visibility = "visible"
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"))
    })
    await act(() => vi.advanceTimersByTimeAsync(MARK_READ_DELAY_MS))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("sends nothing for a teamspace without read cursors, or when nothing is seen", async () => {
    setup({ ...tracked, last_read_seq: undefined, unread_count: undefined }, 42)
    setup({ ...tracked, is_member: false }, 42)
    setup(tracked, undefined)
    await act(() => vi.advanceTimersByTimeAsync(MARK_READ_DELAY_MS * 3))
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("does not mark what is already read", async () => {
    setup({ ...tracked, last_read_seq: 42, unread_count: 0 }, 42)
    await act(() => vi.advanceTimersByTimeAsync(MARK_READ_DELAY_MS * 3))
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("drops a list read in flight, so it cannot bring the count back", async () => {
    const { queryClient } = setup(tracked, 42)
    let answer: (spaces: Teamspace[]) => void = () => {}
    void queryClient
      .fetchQuery({
        queryKey: teamspaceKeys.list("org_1"),
        queryFn: () =>
          new Promise<Teamspace[]>((resolve) => {
            answer = resolve
          }),
        staleTime: 0,
      })
      .catch(() => {})
    await act(() => vi.advanceTimersByTimeAsync(MARK_READ_DELAY_MS))
    await act(async () => answer([tracked]))
    expect(
      queryClient.getQueryData<Teamspace[]>(teamspaceKeys.list("org_1"))?.[0]
    ).toMatchObject({ last_read_seq: 42, unread_count: 0 })
  })

  it("tries a failed mark again after a wait, not at once", async () => {
    fetchMock.mockImplementationOnce(
      async () =>
        new Response(JSON.stringify({ error: { code: "x", message: "x" } }), {
          status: 503,
          headers: { "Content-Type": "application/json" },
        })
    )
    setup(tracked, 42)
    await act(() => vi.advanceTimersByTimeAsync(MARK_READ_DELAY_MS))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await act(() => vi.advanceTimersByTimeAsync(MARK_READ_RETRY_MS - 100))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await act(() => vi.advanceTimersByTimeAsync(200))
    expect(marks().map(([, , body]) => body)).toEqual([
      '{"seq":42}',
      '{"seq":42}',
    ])
  })
})
