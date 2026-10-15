import { describe, expect, it } from "vitest"
import {
  clearStats,
  matchesSearch,
  rowTag,
  rowTime,
  waitWords,
} from "./requests-view-format"
import { clockTime } from "./time-format"
import {
  NOW,
  OWNER,
  agentRequest,
  answer,
  approvedRequest,
  repliedRequest,
  slackRequest,
} from "./request-card-fixtures"

const HOUR = 60 * 60_000

describe("a row's tag", () => {
  it("says how long a waiting choice has left", () => {
    const soon = slackRequest({
      expires_at: new Date(NOW + HOUR).toISOString(),
    })
    expect(rowTag(soon, true, NOW)).toBe("Expires in 1 h")
  })

  it("says an ask wants the member's own reply, or that an answer is held", () => {
    expect(rowTag(slackRequest({ answer_by: "owner" }), true, NOW)).toBe(
      "Wants your reply"
    )
    expect(
      rowTag(approvedRequest("summary", { status: "review" }), true, NOW)
    ).toBe("Answer held for you")
  })

  it("names who answered an answered one", () => {
    const byAgent = approvedRequest("summary", {
      status: "completed",
      result: answer("Done."),
    })
    expect(rowTag(byAgent, false, NOW, "Krude")).toBe("Answered by Krude")
    // Without the member's agent's name, the request's own.
    expect(rowTag(byAgent, false, NOW)).toBe("Answered by Krude")
    expect(rowTag(repliedRequest("Yes."), false, NOW, "Krude")).toBe(
      "Answered by you"
    )
  })

  it("says where the rest stand", () => {
    expect(rowTag(slackRequest({ status: "rejected" }), false, NOW)).toBe(
      "Declined"
    )
    expect(rowTag(slackRequest({ status: "expired" }), false, NOW)).toBe(
      "Expired"
    )
  })
})

describe("a row's time", () => {
  it("is the clock today, and how long ago before", () => {
    const today = new Date(NOW - 10 * 60_000).toISOString()
    expect(rowTime(today, NOW)).toBe(clockTime(today))
    expect(rowTime(new Date(NOW - 30 * HOUR).toISOString(), NOW)).toBe(
      "Yesterday"
    )
  })
})

describe("the list's search", () => {
  const nameOf = (id: string) => (id === OWNER ? "Ada Lovelace" : "Raya Patel")

  it("matches every word, in who asked, who was asked or what", () => {
    const ask = slackRequest()
    expect(matchesSearch(ask, "", nameOf)).toBe(true)
    expect(matchesSearch(ask, "raya eu", nameOf)).toBe(true)
    expect(matchesSearch(ask, "Launch PLAN", nameOf)).toBe(true)
    expect(matchesSearch(ask, "raya pricing", nameOf)).toBe(false)
    expect(matchesSearch(agentRequest(), "boundary", nameOf)).toBe(true)
  })
})

describe("the all-clear's numbers", () => {
  it("counts today's asks, who answered them and how long they waited", () => {
    const at = (minutesAgo: number) =>
      new Date(NOW - minutesAgo * 60_000).toISOString()
    const byAgent = approvedRequest("summary", {
      id: "a",
      status: "completed",
      created_at: at(20),
      result: answer("Done."),
      decision: {
        decision: "approved",
        message: "",
        allowed_tools: [],
        granted_permissions: [],
        decided_at: at(16),
        decided_by: { kind: "member", user_id: OWNER, surface: "web" },
        consent: "summary",
      },
    })
    const byYou = repliedRequest("Yes.", "decision", {
      id: "b",
      created_at: at(10),
      decision: {
        decision: "approved",
        message: "",
        allowed_tools: [],
        granted_permissions: [],
        decided_at: at(8),
        decided_by: { kind: "member", user_id: OWNER, surface: "web" },
        consent: "reply",
      },
    })
    const waiting = slackRequest({ id: "c", created_at: at(5) })
    const lastWeek = slackRequest({
      id: "d",
      created_at: new Date(NOW - 7 * 24 * HOUR).toISOString(),
    })

    const stats = clearStats([byAgent, byYou, waiting, lastWeek], OWNER, NOW)
    expect(stats).toEqual({
      asks: 3,
      byAgent: 1,
      byYou: 1,
      averageWaitMs: 3 * 60_000,
    })
    expect(waitWords(stats.averageWaitMs ?? 0)).toBe("3 min")
  })

  it("has no average wait when nothing was decided today", () => {
    expect(clearStats([slackRequest()], OWNER, NOW).averageWaitMs).toBe(
      undefined
    )
    expect(waitWords(20_000)).toBe("under a minute")
  })
})
