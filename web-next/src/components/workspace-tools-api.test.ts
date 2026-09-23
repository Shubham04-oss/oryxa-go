// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError, readApi } from "./workspace-tools-api"

function apiResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("readApi", () => {
  it("retries once after a 401 when the session refresh succeeds, and returns the retry's JSON", async () => {
    let attempts = 0
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, options?: RequestInit) => {
        const url = String(input)
        if (url === "/v1/apps/catalog?limit=1") {
          attempts += 1
          return attempts === 1
            ? apiResponse({ error: "expired" }, 401)
            : apiResponse({ toolkits: ["ok"] })
        }
        if (url === "/v1/auth/refresh" && options?.method === "POST") {
          return apiResponse({}, 200)
        }
        throw new Error(`Unexpected request to ${url}`)
      }
    )
    vi.stubGlobal("fetch", fetchMock)

    const data = await readApi<{ toolkits: string[] }>(
      "/v1/apps/catalog?limit=1"
    )

    expect(data).toEqual({ toolkits: ["ok"] })
    expect(attempts).toBe(2)
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/auth/refresh",
      expect.objectContaining({ method: "POST" })
    )
  })

  it("throws the shared ApiError built by requestError on a non-OK response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        apiResponse({ error: { message: "boom", code: "x" } }, 500)
      )
    )

    const error = await readApi("/v1/whatever").catch(
      (caught: unknown) => caught
    )

    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ message: "boom", status: 500, code: "x" })
  })

  it("throws when the response is not JSON, so the page can tell the Apps service is absent", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          ({
            ok: true,
            status: 200,
            json: async () => {
              throw new Error("not json")
            },
          }) as unknown as Response
      )
    )

    await expect(readApi("/v1/whatever")).rejects.toThrow(
      "The service did not return JSON."
    )
  })
})
