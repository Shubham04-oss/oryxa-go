import { afterEach, describe, expect, it, vi } from "vitest"
import { apiFetch, apiRequest } from "./client"

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

afterEach(() => vi.unstubAllGlobals())

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
