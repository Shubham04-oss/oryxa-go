import { describe, expect, it } from "vitest"
import { recheckIn } from "./use-request-standing"
import type { AgentRequest } from "@/lib/agents/agent-requests"

const now = Date.parse("2026-09-24T12:00:00.000Z")

/** Only what a re-read looks at: a request's status and when it expires. */
function request(
  status: AgentRequest["status"],
  expiresIn: number | string
): AgentRequest {
  return {
    status,
    expires_at:
      typeof expiresIn === "string"
        ? expiresIn
        : new Date(now + expiresIn).toISOString(),
  } as AgentRequest
}

describe("when a request that can still move is read again", () => {
  it("waits until it is due to expire, and a moment more", () => {
    for (const status of ["pending", "approved", "review"] as const) {
      expect(recheckIn(request(status, 30 * 60_000), now)).toBe(
        30 * 60_000 + 1_000
      )
    }
  })

  it("never sooner than a few seconds, and never later than an hour", () => {
    expect(recheckIn(request("pending", 100), now)).toBe(5_000)
    // Already past its expiry, and Go has not yet said so.
    expect(recheckIn(request("pending", -60_000), now)).toBe(5_000)
    expect(recheckIn(request("approved", 5 * 24 * 60 * 60_000), now)).toBe(
      60 * 60_000
    )
  })

  it("does not read again one that is settled, or that has no expiry to wait for", () => {
    for (const status of [
      "rejected",
      "completed",
      "failed",
      "expired",
      "withheld",
    ] as const) {
      expect(recheckIn(request(status, 30 * 60_000), now)).toBe(false)
    }
    expect(recheckIn(request("pending", "soon"), now)).toBe(false)
    expect(recheckIn(undefined, now)).toBe(false)
  })
})
