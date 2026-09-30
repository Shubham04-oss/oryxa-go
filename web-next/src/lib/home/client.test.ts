import { afterEach, describe, expect, it, vi } from "vitest"
import { getMemberSlackStatus, listActiveAgentLeases } from "./client"

afterEach(() => vi.unstubAllGlobals())

describe("Home's member-safe metadata reads", () => {
  it("encodes organization and opaque pagination and preserves authority metadata", async () => {
    const page = {
      leases: [{ id: "lease", role: "asker", can_revoke: false }],
      observed_at: "2026-09-30T10:00:00Z",
      has_more: true,
      next_cursor: "next+opaque/",
    }
    const fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify(page), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      )
    vi.stubGlobal("fetch", fetch)
    const controller = new AbortController()
    expect(
      await listActiveAgentLeases("org/a", {
        cursor: "opaque+/=",
        signal: controller.signal,
      })
    ).toEqual(page)
    const [url, init] = fetch.mock.calls[0]
    const parsed = new URL(url, "http://localhost")
    expect(parsed.pathname).toBe("/v1/organizations/org%2Fa/agent-leases")
    expect(parsed.searchParams.get("limit")).toBe("100")
    expect(parsed.searchParams.get("cursor")).toBe("opaque+/=")
    expect(init.signal).toBe(controller.signal)
    expect(init.credentials).toBe("include")
  })

  it("reads Slack status without requesting an OAuth URL", async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ status: "ended", can_manage: false }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    )
    vi.stubGlobal("fetch", fetch)
    expect(await getMemberSlackStatus("org/a")).toEqual({
      status: "ended",
      can_manage: false,
    })
    expect(fetch.mock.calls[0][0]).toBe(
      "/v1/organizations/org%2Fa/apps/slack/status"
    )
    expect(fetch).toHaveBeenCalledOnce()
  })

  it.each([listActiveAgentLeases, getMemberSlackStatus])(
    "preserves a refused read as an error, never an empty success",
    async (read) => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(
            JSON.stringify({
              error: { code: "forbidden", message: "Forbidden" },
            }),
            { status: 403 }
          )
        )
      )
      await expect(read("org")).rejects.toMatchObject({
        status: 403,
        code: "forbidden",
      })
    }
  )
})
