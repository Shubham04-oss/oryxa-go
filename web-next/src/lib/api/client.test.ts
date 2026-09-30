// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  ApiError,
  apiFetch,
  apiReadRetryDelay,
  apiRequest,
  resumeSessionAfterSignIn,
  retryApiRead,
  sessionHasEnded,
} from "./client"
import {
  getAuthConfig,
  getSession,
  googleSignIn,
  signIn,
} from "@/lib/auth/client"

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
  resumeSessionAfterSignIn()
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
    expect(init?.signal?.aborted).toBe(false)
  })

  it("releases every request with the transient timeout instead of a terminal 401", async () => {
    const timedOut = new AbortController()
    vi.spyOn(AbortSignal, "timeout").mockReturnValueOnce(timedOut.signal)
    const api = stubApi()

    const first = apiFetch("/v1/things")
    const second = apiFetch("/v1/others")
    const results = Promise.allSettled([first, second])
    // Both meet a 401 and wait on the one refresh: it is asked for once.
    await vi.waitFor(() => {
      expect(api.callsTo("/v1/things")).toHaveLength(1)
      expect(api.callsTo("/v1/others")).toHaveLength(1)
    })
    await settle()
    expect(api.callsTo("/v1/auth/refresh")).toHaveLength(1)

    timedOut.abort(timeoutError())

    expect(await results).toEqual([
      { status: "rejected", reason: timedOut.signal.reason },
      { status: "rejected", reason: timedOut.signal.reason },
    ])
    expect(sessionHasEnded()).toBe(false)
    // A refresh that failed is no session to retry on.
    expect(api.callsTo("/v1/things")).toHaveLength(1)
    expect(api.callsTo("/v1/others")).toHaveLength(1)
  })

  it("starts a fresh refresh on the next 401 instead of waiting on the one that timed out", async () => {
    const timedOut = new AbortController()
    vi.spyOn(AbortSignal, "timeout").mockReturnValueOnce(timedOut.signal)
    const api = stubApi()

    const stuck = apiFetch("/v1/things")
    const result = expect(stuck).rejects.toMatchObject({ name: "TimeoutError" })
    await vi.waitFor(() =>
      expect(api.callsTo("/v1/auth/refresh")).toHaveLength(1)
    )
    timedOut.abort(timeoutError())
    await result

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

describe("the browser's terminal session", () => {
  const credentials = { email: "a@example.com", password: "correct-password" }

  it("singleflights concurrent 401s, then refuses every protected poll and focus read locally", async () => {
    let answerRefresh!: (response: Response) => void
    const fetchMock = vi.fn(async (url: string) =>
      url === "/v1/auth/refresh"
        ? new Promise<Response>((resolve) => {
            answerRefresh = resolve
          })
        : new Response(null, { status: 401 })
    )
    vi.stubGlobal("fetch", fetchMock)
    const requests = [apiFetch("/v1/threads"), apiFetch("/v1/auth/me")]
    await vi.waitFor(() => expect(answerRefresh).toBeTypeOf("function"))
    expect(
      fetchMock.mock.calls.filter(([url]) => url === "/v1/auth/refresh")
    ).toHaveLength(1)
    answerRefresh(new Response(null, { status: 401 }))
    expect(
      (await Promise.all(requests)).map((response) => response.status)
    ).toEqual([401, 401])
    expect(sessionHasEnded()).toBe(true)

    const calls = fetchMock.mock.calls.length
    for (let poll = 0; poll < 10; poll++) {
      window.dispatchEvent(new Event("focus"))
      await expect(getSession()).rejects.toMatchObject({
        status: 401,
        code: "session_ended",
      })
      expect((await apiFetch("/v1/threads")).status).toBe(401)
    }
    expect(fetchMock).toHaveBeenCalledTimes(calls)
  })

  it("allows public auth but resumes protected work only after successful sign-in", async () => {
    let loginSucceeds = false
    const fetchMock = vi.fn(async (url: string) => {
      if (url === "/v1/auth/config")
        return jsonResponse({ identity_providers: ["Google"] })
      if (url === "/v1/auth/password/login")
        return new Response(null, { status: loginSucceeds ? 204 : 401 })
      if (url === "/v1/auth/refresh") return new Response(null, { status: 401 })
      return loginSucceeds
        ? jsonResponse({ ok: true })
        : new Response(null, { status: 401 })
    })
    vi.stubGlobal("fetch", fetchMock)
    await apiFetch("/v1/threads")
    expect(sessionHasEnded()).toBe(true)
    expect(await getAuthConfig()).toEqual({ identity_providers: ["Google"] })
    await expect(signIn(credentials)).rejects.toMatchObject({ status: 401 })
    expect(sessionHasEnded()).toBe(true)
    loginSucceeds = true
    await signIn(credentials)
    expect(sessionHasEnded()).toBe(false)
    expect((await apiFetch("/v1/threads")).status).toBe(200)
  })

  it("does not reset the latch when Google sign-in starts", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(null, { status: 401 }))
    )
    await apiFetch("/v1/threads")
    const original = window.location
    const win = window as unknown as { location?: Location }
    delete win.location
    const assign = vi.fn()
    win.location = {
      ...original,
      origin: "https://app.example.com",
      assign,
    }
    try {
      googleSignIn("/?org=org-1")
      expect(assign).toHaveBeenCalledOnce()
      expect(sessionHasEnded()).toBe(true)
    } finally {
      delete win.location
      win.location = original
    }
  })

  it.each([401, 200])(
    "cancels an old session's late %s after successful sign-in",
    async (status) => {
      let answerOld!: (response: Response) => void
      const fetchMock = vi.fn(async (url: string) => {
        if (url === "/v1/old")
          return new Promise<Response>((resolve) => {
            answerOld = resolve
          })
        if (url === "/v1/auth/password/login")
          return new Response(null, { status: 204 })
        return jsonResponse({ current: true })
      })
      vi.stubGlobal("fetch", fetchMock)
      const old = apiRequest("/v1/old")
      const cancelled = expect(old).rejects.toMatchObject({
        name: "AbortError",
      })
      await signIn(credentials)
      answerOld(jsonResponse({ private: "old session" }, status))
      await cancelled
      expect(sessionHasEnded()).toBe(false)
      expect(
        fetchMock.mock.calls.filter(([url]) => url === "/v1/auth/refresh")
      ).toHaveLength(0)
      await expect(apiRequest("/v1/current")).resolves.toEqual({
        current: true,
      })
    }
  )

  it("an obsolete refresh 401 cannot re-latch or clear the new generation's refresh", async () => {
    const refreshAnswers: Array<(response: Response) => void> = []
    let refreshed = false
    const fetchMock = vi.fn(async (url: string) => {
      if (url === "/v1/auth/password/login")
        return new Response(null, { status: 204 })
      if (url === "/v1/auth/refresh")
        return new Promise<Response>((resolve) => {
          refreshAnswers.push(resolve)
        })
      return refreshed
        ? jsonResponse({ current: true })
        : new Response(null, { status: 401 })
    })
    vi.stubGlobal("fetch", fetchMock)
    const old = apiRequest("/v1/old")
    const cancelled = expect(old).rejects.toMatchObject({ name: "AbortError" })
    await vi.waitFor(() => expect(refreshAnswers).toHaveLength(1))
    await signIn(credentials)
    const current = apiRequest("/v1/current")
    await vi.waitFor(() => expect(refreshAnswers).toHaveLength(2))
    refreshAnswers[0](new Response(null, { status: 401 }))
    await cancelled
    expect(sessionHasEnded()).toBe(false)
    const concurrent = apiRequest("/v1/another")
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(refreshAnswers).toHaveLength(2)
    refreshed = true
    refreshAnswers[1](new Response(null, { status: 204 }))
    expect(await Promise.all([current, concurrent])).toEqual([
      { current: true },
      { current: true },
    ])
  })

  it("cancels a late refresh-and-retry success from the old session", async () => {
    let reads = 0
    let answerRetry!: (response: Response) => void
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        if (url === "/v1/auth/password/login" || url === "/v1/auth/refresh")
          return new Response(null, { status: 204 })
        if (++reads === 1) return new Response(null, { status: 401 })
        return new Promise<Response>((resolve) => {
          answerRetry = resolve
        })
      })
    )
    const old = apiRequest("/v1/old")
    const cancelled = expect(old).rejects.toMatchObject({ name: "AbortError" })
    await vi.waitFor(() => expect(answerRetry).toBeTypeOf("function"))
    await signIn(credentials)
    answerRetry(jsonResponse({ private: "old session" }))
    await cancelled
    expect(sessionHasEnded()).toBe(false)
  })

  it.each([200, 401])(
    "cancels an old %s whose response body finishes after sign-in",
    async (status) => {
      let answerBody!: (body: unknown) => void
      const response = jsonResponse({}, status)
      const decode = vi.spyOn(response, "json").mockImplementation(
        () =>
          new Promise((resolve) => {
            answerBody = resolve
          })
      )
      vi.stubGlobal(
        "fetch",
        vi.fn(async (url: string) => {
          if (url === "/v1/auth/password/login" || url === "/v1/auth/refresh")
            return new Response(null, { status: 204 })
          return response
        })
      )
      const old = apiRequest("/v1/old")
      const cancelled = expect(old).rejects.toMatchObject({
        name: "AbortError",
      })
      await vi.waitFor(() => expect(decode).toHaveBeenCalledOnce())
      await signIn(credentials)
      answerBody(
        status === 200
          ? { private: "old session" }
          : { error: "old session expired" }
      )
      await cancelled
      expect(sessionHasEnded()).toBe(false)
    }
  )

  it("does not publish a private body that finishes after another read ends the session", async () => {
    let answerBody!: (body: unknown) => void
    const response = jsonResponse({})
    const decode = vi.spyOn(response, "json").mockImplementation(
      () =>
        new Promise((resolve) => {
          answerBody = resolve
        })
    )
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) =>
        url === "/v1/old" ? response : new Response(null, { status: 401 })
      )
    )
    const old = apiRequest("/v1/old")
    const ended = expect(old).rejects.toMatchObject({
      status: 401,
      code: "session_ended",
    })
    await vi.waitFor(() => expect(decode).toHaveBeenCalledOnce())
    await apiFetch("/v1/expiry")
    answerBody({ private: "old session" })
    await ended
  })

  it.each(["network", "server"])(
    "propagates a transient refresh %s failure without ending the session",
    async (failure) => {
      const networkError = new TypeError("Failed to fetch")
      let failed = true
      vi.stubGlobal(
        "fetch",
        vi.fn(async (url: string) => {
          if (url === "/v1/auth/refresh") {
            if (!failed) return new Response(null, { status: 204 })
            if (failure === "network") throw networkError
            return jsonResponse({ error: "temporarily unavailable" }, 503)
          }
          return failed
            ? new Response(null, { status: 401 })
            : jsonResponse({ ok: true })
        })
      )
      await expect(apiRequest("/v1/threads")).rejects.toMatchObject(
        failure === "network"
          ? { name: "TypeError", message: "Failed to fetch" }
          : { status: 503, message: "temporarily unavailable" }
      )
      expect(sessionHasEnded()).toBe(false)
      failed = false
      await expect(apiRequest("/v1/threads")).resolves.toEqual({ ok: true })
    }
  )

  it("only a refresh 401 ends the session, not a protected refusal after a successful refresh", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async (url: string) =>
          new Response(null, { status: url === "/v1/auth/refresh" ? 204 : 401 })
      )
    )
    expect((await apiFetch("/v1/threads")).status).toBe(401)
    expect(sessionHasEnded()).toBe(false)
  })

  it("bounds transient retries with backoff and does not retry refusals or stale requests", () => {
    expect(
      [0, 1, 2].map((count) => retryApiRead(count, new TypeError("offline")))
    ).toEqual([true, true, false])
    expect(retryApiRead(0, new ApiError("unavailable", 503))).toBe(true)
    expect(retryApiRead(0, new ApiError("refused", 401))).toBe(false)
    expect(retryApiRead(0, new DOMException("old session", "AbortError"))).toBe(
      false
    )
    expect([0, 1, 2, 10].map(apiReadRetryDelay)).toEqual([
      1000, 2000, 4000, 30000,
    ])
  })

  it("a fresh document starts with its own session verification, not another window's latch", async () => {
    const fetchMock = vi.fn(
      async (_url: string) => new Response(null, { status: 401 })
    )
    vi.stubGlobal("fetch", fetchMock)
    await apiFetch("/v1/threads")
    expect(sessionHasEnded()).toBe(true)
    vi.stubGlobal("window", {})
    expect(sessionHasEnded()).toBe(false)
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ user: { id: "verified" }, organizations: [] })
    )
    expect((await getSession()).user.id).toBe("verified")
    expect(fetchMock.mock.calls.at(-1)?.[0]).toBe("/v1/auth/me")
  })
})
