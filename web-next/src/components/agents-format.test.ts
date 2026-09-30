import { describe, expect, it } from "vitest"
import { agentsErrorMessage, timeAgo } from "./agents-format"
import { ApiError } from "@/lib/api/client"

describe("agentsErrorMessage", () => {
  const fallback = "Could not save."

  it("prefers the copy given for the refusal's code", () => {
    const error = new ApiError("agent is in use", 409, "agent_in_use")
    expect(
      agentsErrorMessage(error, fallback, { agent_in_use: "Take it off." })
    ).toBe("Take it off.")
  })

  it("gives the service's own words for a request it refused, as a sentence", () => {
    expect(
      agentsErrorMessage(
        new ApiError(
          "invalid agent team: an active primary agent is required",
          400,
          "invalid_request"
        ),
        fallback
      )
    ).toBe("An active primary agent is required.")
    expect(
      agentsErrorMessage(
        new ApiError("name is required", 400, "invalid_request"),
        fallback
      )
    ).toBe("Name is required.")
  })

  it("names a feature this server doesn't run, apart from a failure", () => {
    const unavailable = new ApiError(
      "agent teams are not configured",
      503,
      "agent_teams_unavailable"
    )
    expect(agentsErrorMessage(unavailable, fallback)).toBe(
      "This isn't set up on this server."
    )
    expect(
      agentsErrorMessage(unavailable, fallback, {
        unavailable_here: "Teams aren't set up here.",
      })
    ).toBe("Teams aren't set up here.")
    // Plain `unavailable` is auth being down: worth retrying.
    expect(
      agentsErrorMessage(new ApiError("down", 503, "unavailable"), fallback)
    ).toBe(fallback)
  })

  it("keeps internal failures and unknown errors to the fallback", () => {
    expect(
      agentsErrorMessage(
        new ApiError("internal server error", 500, "internal"),
        fallback
      )
    ).toBe(fallback)
    expect(agentsErrorMessage(new TypeError("fetch failed"), fallback)).toBe(
      fallback
    )
  })

  it("says what a missing, expired or throttled request means", () => {
    expect(
      agentsErrorMessage(new ApiError("gone", 404, "not_found"), fallback, {
        not_found: "This team no longer exists.",
      })
    ).toBe("This team no longer exists.")
    expect(
      agentsErrorMessage(new ApiError("unauthorized", 401), fallback)
    ).toBe("Your session has ended. Sign in again.")
    expect(
      agentsErrorMessage(
        new ApiError("slow down", 429, "rate_limited"),
        fallback
      )
    ).toBe("Too many requests. Try again in a minute.")
  })
})

describe("timeAgo", () => {
  const now = Date.parse("2026-09-24T12:00:00Z")

  it("reads recent times relative to now, older ones as a date", () => {
    expect(timeAgo("2026-09-24T11:59:40Z", now)).toBe("just now")
    expect(timeAgo("2026-09-24T11:55:00Z", now)).toBe("5 min ago")
    expect(timeAgo("2026-09-24T09:00:00Z", now)).toBe("3 h ago")
    expect(timeAgo("2026-09-22T12:00:00Z", now)).toBe("2 d ago")
    expect(timeAgo("2026-08-01T12:00:00Z", now)).toMatch(/2026/)
    expect(timeAgo("not a date", now)).toBe("")
  })
})
