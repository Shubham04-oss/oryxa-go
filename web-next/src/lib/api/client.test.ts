import { afterEach, describe, expect, it, vi } from "vitest"
import { apiFetch, apiRequest } from "./client"

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("apiFetch with a JSON body", () => {
  it("stringifies the body and sets Content-Type", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ ok: true }))
    vi.stubGlobal("fetch", fetchMock)

    await apiFetch("/v1/things", { body: { a: 1 } })

    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/things",
      expect.objectContaining({
        body: JSON.stringify({ a: 1 }),
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        method: "POST",
      })
    )
  })

  it("sends no body or Content-Type for a bodyless GET", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}))
    vi.stubGlobal("fetch", fetchMock)

    await apiFetch("/v1/things")

    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/things",
      expect.objectContaining({
        body: undefined,
        headers: undefined,
        method: "GET",
      })
    )
  })

  it("refreshes an expired session once and retries with the same JSON body", async () => {
    const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => {
      if (url === "/v1/auth/refresh") return new Response(null, { status: 204 })
      return jsonResponse({ ok: true })
    })
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 401 }))
    vi.stubGlobal("fetch", fetchMock)

    const response = await apiFetch("/v1/things", { body: { a: 1 } })

    expect(response.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/v1/things")
    expect(fetchMock.mock.calls[1]?.[0]).toBe("/v1/auth/refresh")
    expect(fetchMock.mock.calls[2]?.[0]).toBe("/v1/things")
    expect(fetchMock.mock.calls[2]?.[1]).toEqual(
      expect.objectContaining({ body: JSON.stringify({ a: 1 }) })
    )
  })

  it("does not retry when refresh itself is not asked for", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 401 }))
    vi.stubGlobal("fetch", fetchMock)

    const response = await apiFetch("/v1/things", { refreshSession: false })

    expect(response.status).toBe(401)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe("apiFetch when the session refresh never answers", () => {
  /**
   * A fetch that behaves as the platform's does: a request that is aborted
   * rejects. Requests answer 401 until the session is refreshed, and
   * `/v1/auth/refresh` answers only when the test says so, or never.
   */
  function stubApi() {
    let refreshed = false
    const answers: Array<() => void> = []
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url !== "/v1/auth/refresh") {
        return refreshed
          ? jsonResponse({ ok: true })
          : new Response(null, { status: 401 })
      }
      const signal = init?.signal
      return new Promise<Response>((resolve, reject) => {
        signal?.addEventListener("abort", () => reject(signal.reason))
        answers.push(() => {
          refreshed = true
          resolve(new Response(null, { status: 204 }))
        })
      })
    })
    vi.stubGlobal("fetch", fetchMock)
    return {
      callsTo: (path: string) =>
        fetchMock.mock.calls.filter(([url]) => url === path),
      answerRefresh: (attempt: number) => answers[attempt](),
    }
  }

  const settle = () => new Promise((resolve) => setTimeout(resolve, 0))
  const timeoutError = () => new DOMException("timed out", "TimeoutError")

  it("gives the refresh request a timeout", async () => {
    const timeout = vi.spyOn(AbortSignal, "timeout")
    const api = stubApi()

    const pending = apiFetch("/v1/things")
    await vi.waitFor(() =>
      expect(api.callsTo("/v1/auth/refresh")).toHaveLength(1)
    )
    api.answerRefresh(0)
    await pending

    expect(timeout).toHaveBeenCalledTimes(1)
    expect(timeout.mock.calls[0][0]).toBeGreaterThan(0)
    const [, init] = api.callsTo("/v1/auth/refresh")[0]
    expect(init?.signal).toBe(timeout.mock.results[0].value)
  })

  it("releases every request waiting on it, each with the 401 it got, once it times out", async () => {
    const timedOut = new AbortController()
    vi.spyOn(AbortSignal, "timeout").mockReturnValueOnce(timedOut.signal)
    const api = stubApi()

    const first = apiFetch("/v1/things")
    const second = apiFetch("/v1/others")
    // Both meet a 401 and wait on the one refresh: it is asked for once.
    await vi.waitFor(() => {
      expect(api.callsTo("/v1/things")).toHaveLength(1)
      expect(api.callsTo("/v1/others")).toHaveLength(1)
    })
    await settle()
    expect(api.callsTo("/v1/auth/refresh")).toHaveLength(1)

    timedOut.abort(timeoutError())

    expect((await first).status).toBe(401)
    expect((await second).status).toBe(401)
    // A refresh that failed is no session to retry on.
    expect(api.callsTo("/v1/things")).toHaveLength(1)
    expect(api.callsTo("/v1/others")).toHaveLength(1)
  })

  it("starts a fresh refresh on the next 401 instead of waiting on the one that timed out", async () => {
    const timedOut = new AbortController()
    vi.spyOn(AbortSignal, "timeout").mockReturnValueOnce(timedOut.signal)
    const api = stubApi()

    const stuck = apiFetch("/v1/things")
    await vi.waitFor(() =>
      expect(api.callsTo("/v1/auth/refresh")).toHaveLength(1)
    )
    timedOut.abort(timeoutError())
    expect((await stuck).status).toBe(401)

    // The shared promise was cleared, so this request asks for a refresh of
    // its own; answered this time, it refreshes the session and retries.
    const next = apiFetch("/v1/things")
    await vi.waitFor(() =>
      expect(api.callsTo("/v1/auth/refresh")).toHaveLength(2)
    )
    api.answerRefresh(1)
    const response = await next
    expect(response.status).toBe(200)
    expect(await response.json()).toEqual({ ok: true })
  })
})

describe("apiFetch with a FormData body", () => {
  it("passes the FormData through untouched, with no Content-Type header", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) =>
      jsonResponse({ ok: true })
    )
    vi.stubGlobal("fetch", fetchMock)
    const form = new FormData()
    form.set("file", new File(["hello"], "notes.txt", { type: "text/plain" }))

    await apiFetch("/v1/uploads", { body: form })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe("/v1/uploads")
    expect(init.body).toBe(form)
    expect(init.headers).toBeUndefined()
    expect(init.method).toBe("POST")
    expect(init.credentials).toBe("include")
  })

  it("re-sends the same FormData instance on a 401 refresh-and-retry", async () => {
    const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => {
      if (url === "/v1/auth/refresh") return new Response(null, { status: 204 })
      return jsonResponse({ id: "file_1" })
    })
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 401 }))
    vi.stubGlobal("fetch", fetchMock)
    const form = new FormData()
    form.set("file", new File(["hello"], "notes.txt", { type: "text/plain" }))

    const response = await apiFetch("/v1/uploads", { body: form })

    expect(response.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    // The same FormData instance is reused rather than rebuilt or consumed:
    // both the failed attempt and the retry read the caller's one object.
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ body: form })
    expect(fetchMock.mock.calls[2]?.[1]).toMatchObject({ body: form })
  })
})

describe("apiRequest", () => {
  it("parses a JSON response and throws ApiError on failure", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ value: 42 }))
    vi.stubGlobal("fetch", fetchMock)
    await expect(apiRequest<{ value: number }>("/v1/things")).resolves.toEqual({
      value: 42,
    })
  })

  it("returns undefined for a response with no JSON body, such as a 204", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal("fetch", fetchMock)
    await expect(
      apiRequest("/v1/things", { method: "DELETE" })
    ).resolves.toBeUndefined()
  })

  it("rejects with the parsed error on a non-OK response", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(
        { error: { code: "not_found", message: "resource not found" } },
        404
      )
    )
    vi.stubGlobal("fetch", fetchMock)
    await expect(apiRequest("/v1/things/missing")).rejects.toMatchObject({
      status: 404,
      code: "not_found",
      message: "resource not found",
    })
  })
})
