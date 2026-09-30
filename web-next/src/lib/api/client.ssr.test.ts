import { afterEach, describe, expect, it, vi } from "vitest"
import { apiFetch, sessionHasEnded } from "./client"
import { getContext } from "@/integrations/tanstack-query/context"

afterEach(() => vi.unstubAllGlobals())

describe("SSR session isolation", () => {
  it("never shares a refresh or terminal latch between concurrent SSR requests", async () => {
    expect(typeof window).toBe("undefined")
    const answers: Array<(response: Response) => void> = []
    const reads = new Map<string, number>()
    const fetchMock = vi.fn(async (url: string) => {
      if (url === "/v1/auth/refresh")
        return new Promise<Response>((resolve) => {
          answers.push(resolve)
        })
      const count = (reads.get(url) ?? 0) + 1
      reads.set(url, count)
      return new Response(null, { status: count === 1 ? 401 : 204 })
    })
    vi.stubGlobal("fetch", fetchMock)
    const first = apiFetch("/v1/member-one")
    const second = apiFetch("/v1/member-two")
    await vi.waitFor(() => expect(answers).toHaveLength(2))
    answers[0](new Response(null, { status: 401 }))
    answers[1](new Response(null, { status: 204 }))
    expect(
      (await Promise.all([first, second])).map((response) => response.status)
    ).toEqual([401, 204])
    expect(sessionHasEnded()).toBe(false)
    const later = apiFetch("/v1/member-three")
    await vi.waitFor(() => expect(answers).toHaveLength(3))
    answers[2](new Response(null, { status: 204 }))
    expect((await later).status).toBe(204)
  })

  it("keeps authenticated query caches private to each SSR request", () => {
    const first = getContext().queryClient
    const second = getContext().queryClient
    first.setQueryData(["auth", "session"], { user: { id: "first" } })
    expect(second.getQueryData(["auth", "session"])).toBeUndefined()
    first.clear()
    second.clear()
  })
})
