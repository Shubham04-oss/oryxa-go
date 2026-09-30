import { describe, expect, it } from "vitest"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import { clockTime } from "./direct-message-format"
import {
  DENY,
  LET_MY_AGENT_ANSWER,
  REPLY_COUNTER_FROM,
  REPLY_YOURSELF,
  SEND_REPLY,
  SHARE_ANSWER,
  consentChoicePending,
  consentChoices,
  consentWords,
  decisionLine,
  denyName,
  expiresPhrase,
  hiddenNote,
  heldReason,
  isExpired,
  isOpenToDecide,
  isSummaryRuleHold,
  letMyAgentAnswerName,
  originLabel,
  readsLine,
  replyLength,
  replyProblem,
  replyYourselfName,
  requestTitle,
  shareAnswerName,
  sideOf,
  statusOf,
  waitsOnOwner,
  whenPhrase,
  winnerLine,
} from "./request-card-format"
import {
  ASKER,
  NOW,
  OWNER,
  REPLIED_AT,
  agentRequest,
  approvedRequest,
  answer,
  reader,
  repliedRequest,
  slackRequest,
} from "./request-card-fixtures"

const owner = reader(OWNER)
const asker = reader(ASKER)
const decidedAt = "2026-09-29T12:02:00Z"
const at = clockTime(decidedAt)

function decidedBy(
  request: AgentRequest,
  by: NonNullable<AgentRequest["decision"]>["decided_by"],
  decision: "approved" | "rejected" = "approved",
  consent?: "summary" | "access" | "reply"
): AgentRequest {
  return {
    ...request,
    decision: {
      decision,
      message: "",
      allowed_tools: [],
      granted_permissions: [],
      decided_at: decidedAt,
      decided_by: by,
      ...(consent ? { consent } : {}),
    },
  }
}

describe("who a request is for", () => {
  it("is the owner's when addressed to them, the asker's when they asked", () => {
    const request = slackRequest()
    expect(sideOf(request, OWNER)).toBe("owner")
    expect(sideOf(request, ASKER)).toBe("asker")
    expect(sideOf(request, "user-someone-else")).toBe("other")
  })

  it("waits on the owner to decide, or to review, and on nobody else", () => {
    const pending = slackRequest()
    const review = slackRequest({ status: "review" })

    expect(waitsOnOwner(pending, OWNER, NOW)).toBe(true)
    expect(waitsOnOwner(review, OWNER, NOW)).toBe(true)
    expect(waitsOnOwner(pending, ASKER, NOW)).toBe(false)
    expect(waitsOnOwner(approvedRequest(), OWNER, NOW)).toBe(false)
    expect(
      waitsOnOwner(
        slackRequest({ expires_at: "2026-09-29T12:10:00Z" }),
        OWNER,
        NOW
      )
    ).toBe(false)
  })

  it("knows a request past its time before Go says so", () => {
    const late = { expires_at: "2026-09-29T12:10:00Z" }

    expect(isExpired(slackRequest(late), NOW)).toBe(true)
    expect(isExpired(approvedRequest("summary", late), NOW)).toBe(true)
    expect(isExpired(slackRequest({ status: "expired" }), NOW)).toBe(true)
    expect(isExpired(slackRequest(), NOW)).toBe(false)
    expect(isExpired(slackRequest({ status: "completed", ...late }), NOW)).toBe(
      false
    )
    expect(isOpenToDecide(slackRequest(late), NOW)).toBe(false)
    expect(isOpenToDecide(slackRequest(), NOW)).toBe(true)
  })
})

describe("what a card calls things", () => {
  it("titles a Slack ask by the two people, and an agent's by the agent asked", () => {
    expect(requestTitle(slackRequest(), owner)).toBe("Raya Patel asked you")
    expect(requestTitle(slackRequest(), asker)).toBe("You asked Ada Lovelace")
    expect(requestTitle(agentRequest(), owner)).toBe("Raya Patel asked Krude")
    expect(requestTitle(agentRequest(), asker)).toBe("You asked Krude")
  })

  it("says where it was made", () => {
    expect(originLabel(slackRequest())).toBe("Slack")
    expect(originLabel(agentRequest())).toBe("Oryxa")
  })

  it("falls back to what the request itself knows of a member the list does not", () => {
    const nobody = reader(OWNER, { nameOf: () => undefined })

    expect(requestTitle(slackRequest(), nobody)).toBe("Someone asked you")
    expect(
      requestTitle(slackRequest(), reader(ASKER, { nameOf: () => undefined }))
    ).toBe("You asked Ada Lovelace")
  })

  it("puts a time as the clock today, and the day beside it otherwise", () => {
    const day = 24 * 60 * 60 * 1000

    expect(whenPhrase(decidedAt, NOW)).toBe(` at ${at}`)
    expect(whenPhrase(decidedAt, NOW + day)).toBe(` yesterday at ${at}`)
    expect(whenPhrase(decidedAt, NOW + 5 * day)).toMatch(
      new RegExp(` on .+ at ${at.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`)
    )
    expect(whenPhrase(undefined, NOW)).toBe("")
    expect(whenPhrase("not a time", NOW)).toBe("")
  })

  it("counts down what an open request has left", () => {
    const left = (minutes: number) =>
      expiresPhrase(
        slackRequest({
          expires_at: new Date(NOW + minutes * 60_000).toISOString(),
        }),
        NOW
      )

    expect(left(0.5)).toBe("Expires in under a minute.")
    expect(left(25)).toBe("Expires in 25 min.")
    expect(left(180)).toBe("Expires in 3 h.")
    expect(left(60 * 24 * 4)).toBe("Expires in 4 days.")
    expect(left(-5)).toBeUndefined()
    expect(expiresPhrase(approvedRequest(), NOW)).toBeDefined()
    expect(
      expiresPhrase(slackRequest({ status: "completed" }), NOW)
    ).toBeUndefined()
  })
})

describe("how a request was decided", () => {
  it("says where, by whom, when, and how far", () => {
    expect(decisionLine(approvedRequest("summary"), owner)).toBe(
      `Approved in Slack by you at ${at}. Answer shared.`
    )
    expect(decisionLine(approvedRequest("access"), asker)).toBe(
      `Approved in Slack by Ada at ${at}. Access for 1 hour.`
    )
    const web = decidedBy(
      slackRequest(),
      { kind: "member", user_id: OWNER, surface: "web" },
      "rejected"
    )
    expect(decisionLine(web, asker)).toBe(`Denied in Oryxa by Ada at ${at}.`)
    expect(decisionLine(slackRequest(), owner)).toBeUndefined()
  })

  it("says denied, as the button did, of a request that never asked for consent too", () => {
    const denied = decidedBy(
      agentRequest(),
      { kind: "member", user_id: OWNER, surface: "web" },
      "rejected"
    )
    expect(decisionLine(denied, owner)).toBe(`Denied in Oryxa by you at ${at}.`)
    expect(decisionLine(denied, asker)).toBe(`Denied in Oryxa by Ada at ${at}.`)
  })

  it("keeps an agent's own decline in its own words", () => {
    const declined = decidedBy(
      agentRequest(),
      { kind: "agent", agent_id: "agt" },
      "rejected"
    )
    expect(decisionLine(declined, owner)).toBe(
      `Declined by your agent at ${at}.`
    )
    expect(decisionLine(declined, asker)).toBe(
      `Declined by Ada's agent at ${at}.`
    )
  })

  it("names the access a follow-up was approved under", () => {
    const followUp = decidedBy(
      slackRequest(),
      { kind: "lease", user_id: OWNER, surface: "lease", reason: "ils_1" },
      "approved",
      "access"
    )
    expect(decisionLine(followUp, owner)).toBe(
      `Approved at ${at} under the access you gave Raya.`
    )
    expect(decisionLine(followUp, asker)).toBe(
      `Approved at ${at} under the access Ada gave you.`
    )
  })

  it("says why a bypass needed no click, and never its code", () => {
    const bypass = (reason: string) =>
      decidedBy(
        slackRequest(),
        { kind: "bypass", surface: "bypass", reason },
        "approved",
        "access"
      )
    expect(decisionLine(bypass("priority_channel"), owner)).toBe(
      `Approved at ${at} without asking you: it was asked in a high-priority channel.`
    )
    expect(decisionLine(bypass("org_chart"), owner)).toBe(
      `Approved at ${at} without asking you: Raya is above you in your organization's chart.`
    )
    expect(decisionLine(bypass("org_chart"), asker)).toBe(
      `Approved at ${at} without asking Ada: you are above Ada in your organization's chart.`
    )
    const unknown = decisionLine(bypass("something_new"), owner)
    expect(unknown).toBe(`Approved at ${at} without asking you.`)
  })

  it("says what an agent and Oryxa did in their own words", () => {
    const agent = decidedBy(agentRequest(), { kind: "agent", agent_id: "agt" })
    expect(decisionLine(agent, owner)).toBe(`Approved by your agent at ${at}.`)
    expect(decisionLine(agent, asker)).toBe(`Approved by Ada's agent at ${at}.`)
    const closed = decidedBy(
      slackRequest(),
      { kind: "system", reason: "answered_elsewhere" },
      "rejected"
    )
    expect(decisionLine(closed, owner)).toBe(
      "Another owner answered first, so Oryxa closed this one."
    )
    const system = decidedBy(slackRequest(), { kind: "system" }, "rejected")
    expect(decisionLine(system, owner)).toBe(`Closed by Oryxa at ${at}.`)
  })

  it("puts the winner of a lost race in the same words", () => {
    const request = slackRequest()

    expect(
      winnerLine(
        {
          request_id: request.id,
          status: "approved",
          revision: 2,
          decision: "approved",
          consent: "summary",
          decided_by: { kind: "member", user_id: OWNER, surface: "slack" },
          decided_at: decidedAt,
        },
        request,
        owner
      )
    ).toBe(`Approved in Slack by you at ${at}. Answer shared.`)
    expect(
      winnerLine(
        { request_id: request.id, status: "expired", revision: 2 },
        request,
        owner
      )
    ).toBe("This request expired before it was decided.")
    expect(
      winnerLine(
        { request_id: request.id, status: "rejected", revision: 2 },
        request,
        owner
      )
    ).toBe("This request was closed before it was decided.")
  })

  it("says a reply as a reply, where, by whom and when, and never as an approval", () => {
    const replied = (surface: string, consent: "reply" = "reply") =>
      decidedBy(
        slackRequest(),
        { kind: "member", user_id: OWNER, surface },
        "approved",
        consent
      )

    expect(decisionLine(replied("web"), owner)).toBe(
      `Replied in Oryxa by you at ${at}.`
    )
    expect(decisionLine(replied("slack"), asker)).toBe(
      `Replied in Slack by Ada at ${at}.`
    )
    expect(decisionLine(replied("slack"), owner)).not.toMatch(/Approved/)
    expect(
      winnerLine(
        {
          request_id: "irq_slack",
          status: "completed",
          revision: 3,
          decision: "approved",
          consent: "reply",
          decided_by: { kind: "member", user_id: OWNER, surface: "slack" },
          decided_at: decidedAt,
        },
        slackRequest(),
        owner
      )
    ).toBe(`Replied in Slack by you at ${at}.`)
    expect(consentWords("reply")).toBe("Replied in their own words.")
  })
})

describe("why an answer was held", () => {
  it("names the personal details it holds an answer for", () => {
    expect(
      heldReason("Krude", { state: "held", kinds: ["EMAIL", "PHONE"] })
    ).toBe(
      "Krude's answer includes an email address and a phone number. Review it before it's shared."
    )
  })

  it("says an answer strayed from the question, with what it carried", () => {
    expect(
      heldReason("Krude", {
        state: "held",
        kinds: ["OFF_TOPIC", "health", "compensation"],
      })
    ).toBe(
      "Krude's answer may include something the question didn't ask for (health details and pay details). Review it before it's shared."
    )
    expect(
      heldReason("Krude", { state: "held", kinds: ["OFF_TOPIC", "UNCHECKED"] })
    ).toBe(
      "Krude's answer may include something the question didn't ask for and couldn't be checked automatically. Review it before it's shared."
    )
  })

  it("says an answer could not be checked, and never shows a code it does not know", () => {
    expect(heldReason("Krude", { state: "held", kinds: ["UNCHECKED"] })).toBe(
      "Krude's answer couldn't be checked automatically. Review it before it's shared."
    )
    const strange = heldReason("Krude", {
      state: "held",
      kinds: ["UNGROUNDED_ANSWER"],
    })
    expect(strange).toBe(
      "Krude's answer includes details Oryxa flagged. Review it before it's shared."
    )
    expect(strange).not.toMatch(/UNGROUNDED/)
    expect(heldReason("Krude", undefined)).toBe(
      "Review Krude's answer before it's shared."
    )
  })

  it("says what the consent guard holds a Slack answer for, one kind at a time", () => {
    const held = (...kinds: string[]) =>
      heldReason("Krude", { state: "held", kinds })
    const review = " Review it before it's shared."

    expect(held("COPIED_FROM_SOURCE")).toBe(
      "Krude's answer copies long passages from what it read, which a summary shouldn't." +
        review
    )
    expect(held("LINK")).toBe(
      "Krude's answer includes a link, which a summary shouldn't." + review
    )
    expect(held("COPIED_FROM_SOURCE", "LINK")).toBe(
      "Krude's answer copies passages from what it read and includes a link, which a summary shouldn't." +
        review
    )
    expect(held("NO_SOURCES")).toBe(
      "Krude's answer isn't based on anything it read." + review
    )
    expect(held("UNGROUNDED")).toBe(
      "Krude's answer isn't supported by what it read." + review
    )
    expect(held("IRRELEVANT")).toBe(
      "Krude's answer doesn't answer the question." + review
    )
    expect(held("DENIED_TOPIC")).toBe(
      "Krude's answer touches a topic Oryxa doesn't share (credentials, HR or pay records, health or personal life)." +
        review
    )
  })

  it("says several findings at once, in a steady order, and never a code", () => {
    const reason = heldReason("Krude", {
      state: "held",
      kinds: ["UNCHECKED", "UNGROUNDED", "LINK", "EMAIL", "IRRELEVANT"],
    })

    expect(reason).toBe(
      "Krude's answer includes an email address, includes a link, which a summary shouldn't, isn't supported by what it read, doesn't answer the question and couldn't be checked automatically. Review it before it's shared."
    )
    for (const code of [
      "UNCHECKED",
      "UNGROUNDED",
      "LINK",
      "EMAIL",
      "IRRELEVANT",
    ]) {
      expect(reason).not.toContain(code)
    }
  })

  it("says the same findings the same way, whatever order the guard lists them in", () => {
    const held = (...kinds: string[]) =>
      heldReason("Krude", { state: "held", kinds })

    expect(held("UNGROUNDED", "NO_SOURCES")).toBe(
      held("NO_SOURCES", "UNGROUNDED")
    )
    expect(held("DENIED_TOPIC", "IRRELEVANT", "UNGROUNDED")).toBe(
      held("UNGROUNDED", "IRRELEVANT", "DENIED_TOPIC")
    )
  })

  it("knows a hold for the summary rule from any other, since masking cannot mend it", () => {
    expect(
      isSummaryRuleHold({ state: "held", kinds: ["COPIED_FROM_SOURCE"] })
    ).toBe(true)
    expect(isSummaryRuleHold({ state: "held", kinds: ["LINK", "EMAIL"] })).toBe(
      true
    )
    expect(
      isSummaryRuleHold({ state: "held", kinds: ["EMAIL", "UNGROUNDED"] })
    ).toBe(false)
    expect(isSummaryRuleHold(undefined)).toBe(false)
  })
})

describe("what the guard hid in an answer that was shared", () => {
  const completed = (
    guard: AgentRequest["guard"],
    review?: AgentRequest["review"]
  ) =>
    approvedRequest("summary", {
      status: "completed",
      result: answer("Done."),
      guard,
      review,
    })

  it("tells the owner what the guard always masks, in words", () => {
    const note = (...kinds: string[]) =>
      hiddenNote(completed({ state: "masked", kinds }))

    expect(note("PASSWORD")).toBe("Shared with a password hidden")
    expect(note("SLACK_TOKEN", "API_KEY", "ACCESS_TOKEN", "PRIVATE_KEY")).toBe(
      "Shared with a Slack token, an API key, an access token and a private key hidden"
    )
    expect(note("AWS_ACCESS_KEY", "AWS_SECRET_KEY")).toBe(
      "Shared with an access key hidden"
    )
    expect(note("US_BANK_ACCOUNT_NUMBER", "SWIFT_CODE")).toBe(
      "Shared with bank details hidden"
    )
    expect(note("SOMETHING_NEW")).toBe(
      "Shared with other personal details hidden"
    )
    expect(note("SOMETHING_NEW")).not.toContain("SOMETHING_NEW")
  })

  it("says nothing when it hid nothing", () => {
    expect(hiddenNote(completed({ state: "clear" }))).toBeUndefined()
    expect(hiddenNote(completed(undefined))).toBeUndefined()
    expect(
      hiddenNote(completed({ state: "masked", kinds: ["UNCHECKED"] }))
    ).toBeUndefined()
  })

  it("says what sending it masked hid, and nothing of an answer the owner sent as it was", () => {
    const held = { state: "held" as const, kinds: ["EMAIL", "PHONE"] }
    const review = (action: "send" | "send_masked") => ({
      action,
      decided_by: { kind: "member", user_id: OWNER },
      decided_at: "2026-09-29T12:20:00Z",
    })

    expect(hiddenNote(completed(held, review("send_masked")))).toBe(
      "Shared with an email address and a phone number hidden"
    )
    expect(hiddenNote(completed(held, review("send")))).toBeUndefined()
    // What masking cannot touch is not something it hid.
    expect(
      hiddenNote(
        completed(
          { state: "held", kinds: ["OFF_TOPIC", "health"] },
          review("send_masked")
        )
      )
    ).toBeUndefined()
  })
})

describe("where a request stands", () => {
  it("waits for the owner's choice, and for the owner to the asker", () => {
    expect(statusOf(slackRequest(), owner)).toEqual({
      tone: "waiting",
      text: "Waiting for your choice.",
    })
    expect(statusOf(agentRequest(), owner).text).toBe(
      "Waiting for you to approve."
    )
    expect(statusOf(slackRequest(), asker)).toEqual({
      tone: "waiting",
      text: "Waiting for Ada to decide.",
    })
  })

  it("works on it once approved, and says how it was approved beneath", () => {
    expect(statusOf(approvedRequest("summary"), owner)).toEqual({
      tone: "working",
      text: "Krude is working on it…",
      detail: `Approved in Slack by you at ${at}. Answer shared.`,
    })
    expect(statusOf(approvedRequest("access"), asker).text).toBe(
      "Ada's agent is working on it…"
    )
  })

  it("holds an answer for the owner and says only that to the asker", () => {
    const held = approvedRequest("summary", {
      status: "review",
      guard: { state: "held", kinds: ["EMAIL"] },
    })

    expect(statusOf(held, owner).text).toBe(
      "Krude's answer includes an email address. Review it before it's shared."
    )
    expect(statusOf(held, asker).text).toBe(
      "Waiting for Ada to review the answer."
    )
    expect(JSON.stringify(statusOf(held, asker))).not.toMatch(/email/i)
  })

  it("says it was answered, and what the guard hid to the owner alone", () => {
    const done = approvedRequest("summary", {
      status: "completed",
      result: answer("Launch is on 12 October."),
      guard: { state: "masked", kinds: ["PASSWORD"] },
    })

    expect(statusOf(done, owner)).toEqual({
      tone: "done",
      text: "Answered.",
      detail: `Approved in Slack by you at ${at}. Answer shared. Shared with a password hidden.`,
    })
    expect(statusOf(done, asker).detail).toBe(
      `Approved in Slack by Ada at ${at}. Answer shared.`
    )
  })

  it("says a denial as the decision, and an expiry as one", () => {
    const denied = decidedBy(
      slackRequest({ status: "rejected" }),
      { kind: "member", user_id: OWNER, surface: "web" },
      "rejected"
    )
    expect(statusOf(denied, asker)).toEqual({
      tone: "ended",
      text: `Denied in Oryxa by Ada at ${at}.`,
    })
    const late = slackRequest({ expires_at: "2026-09-29T12:10:00Z" })
    expect(statusOf(late, owner)).toEqual({ tone: "ended", text: "Expired." })
    expect(statusOf(late, asker).text).toBe("Expired before Ada decided.")
    expect(
      statusOf(
        approvedRequest("summary", { expires_at: "2026-09-29T12:10:00Z" }),
        asker
      ).text
    ).toBe("Expired before it was answered.")
  })

  it("says who kept an answer back, and why Oryxa did", () => {
    const withheld = (review: NonNullable<AgentRequest["review"]>) =>
      approvedRequest("summary", { status: "withheld", review })

    const byOwner = withheld({
      action: "withhold",
      decided_by: { kind: "member", user_id: OWNER },
      decided_at: decidedAt,
    })
    expect(statusOf(byOwner, owner).text).toBe(
      "You chose not to share Krude's answer."
    )
    expect(statusOf(byOwner, asker).text).toBe("Not shared by Ada.")
    const expired = withheld({
      action: "withhold",
      decided_by: { kind: "system" },
      decided_at: decidedAt,
      reason: "expired before review",
    })
    expect(statusOf(expired, owner).text).toBe(
      "Expired before you reviewed it."
    )
    expect(statusOf(expired, asker).text).toBe("Expired.")
    const oryxa = withheld({
      action: "withhold",
      decided_by: { kind: "system" },
      decided_at: decidedAt,
      reason: "Oryxa withheld this answer because …",
    })
    expect(statusOf(oryxa, owner).text).toBe("Withheld by Oryxa.")
  })

  it("says what approving lets the owner's agent read, to the owner alone", () => {
    expect(readsLine(slackRequest(), owner)).toBe(
      "Krude may read team knowledge."
    )
    expect(readsLine(slackRequest(), asker)).toBeUndefined()
    expect(
      readsLine(slackRequest({ requested_tools: [] }), owner)
    ).toBeUndefined()
  })

  it("waits for a reply, not a choice, when the ask needs the owner's own judgment", () => {
    const first = (request: AgentRequest) => ({
      ...request,
      answer_by: "owner" as const,
    })

    expect(statusOf(first(slackRequest()), owner)).toEqual({
      tone: "waiting",
      text: "Waiting for your reply.",
    })
    expect(statusOf(first(agentRequest()), owner).text).toBe(
      "Waiting for your reply."
    )
    expect(statusOf(first(slackRequest()), asker)).toEqual({
      tone: "waiting",
      text: "Waiting for Ada to reply.",
    })
    expect(statusOf(first(agentRequest()), asker).text).toBe(
      "Waiting for Ada to reply."
    )
    // The agent is to answer, said or not, and nothing changes.
    for (const answer_by of ["agent", undefined] as const) {
      const request = slackRequest({ answer_by })
      expect(statusOf(request, owner).text).toBe("Waiting for your choice.")
      expect(statusOf(request, asker).text).toBe("Waiting for Ada to decide.")
    }
    // Once it has been let through, it is worked on as any request is.
    expect(statusOf(first(approvedRequest("summary")), asker).text).toBe(
      "Ada's agent is working on it…"
    )
    const late = first(slackRequest({ expires_at: "2026-09-29T12:10:00Z" }))
    expect(statusOf(late, asker).text).toBe("Expired before Ada replied.")
    expect(statusOf(late, owner).text).toBe("Expired.")
  })

  it("says who replied, where and when, in place of Answered", () => {
    const done = repliedRequest("Launch is on 12 October.")
    const sent = `Sent in Oryxa at ${clockTime(REPLIED_AT)}.`

    expect(statusOf(done, owner)).toEqual({
      tone: "done",
      text: "You replied.",
      detail: sent,
    })
    expect(statusOf(done, asker)).toEqual({
      tone: "done",
      text: "Ada replied.",
      detail: sent,
    })
  })

  it("tells the owner their reply took the place of a held answer, and not the asker", () => {
    const replaced = repliedRequest("Call me instead.", "review")
    const when = clockTime(REPLIED_AT)

    expect(statusOf(replaced, owner).detail).toBe(
      `Sent in Oryxa at ${when}, in place of Krude's answer.`
    )
    expect(statusOf(replaced, asker).detail).toBe(`Sent in Oryxa at ${when}.`)
    expect(statusOf(replaced, asker).text).toBe("Ada replied.")
  })

  it("adds what the guard hid to the owner's reply, and keeps it from the asker", () => {
    const masked = repliedRequest("The password is hunter2.", "decision", {
      guard: { state: "masked", kinds: ["PASSWORD"] },
    })

    expect(statusOf(masked, owner).detail).toBe(
      `Sent in Oryxa at ${clockTime(REPLIED_AT)}. Shared with a password hidden.`
    )
    expect(statusOf(masked, asker).detail).toBe(
      `Sent in Oryxa at ${clockTime(REPLIED_AT)}.`
    )
  })

  it("names where a reply was sent from, and says nothing it does not know", () => {
    const from = (surface?: string) => {
      const done = repliedRequest("Yes.")
      return statusOf(
        {
          ...done,
          decision: {
            ...done.decision!,
            decided_by: { kind: "member", user_id: OWNER, surface },
          },
        },
        asker
      ).detail
    }
    const when = clockTime(REPLIED_AT)

    expect(from("slack")).toBe(`Sent in Slack at ${when}.`)
    expect(from(undefined)).toBe(`Sent at ${when}.`)
    // Without a time or a place there is nothing to say of it.
    const bare = repliedRequest("Yes.")
    expect(
      statusOf(
        {
          ...bare,
          decision: {
            ...bare.decision!,
            decided_at: "",
            decided_by: undefined,
          },
        },
        asker
      )
    ).toEqual({ tone: "done", text: "Ada replied." })
  })

  it("still says Answered for an answer the agent wrote, whoever was to answer", () => {
    const done = approvedRequest("summary", {
      answer_by: "owner",
      status: "completed",
      result: answer("Launch is on 12 October."),
    })

    expect(statusOf(done, owner).text).toBe("Answered.")
    expect(statusOf(done, asker).text).toBe("Answered.")
  })
})

describe("the owner's choices", () => {
  it("come in Slack's order, and each says what it lets the agent do", () => {
    expect(consentChoices.map((choice) => choice.choice)).toEqual([
      "summary",
      "access",
      "deny",
    ])
    expect(consentChoices.map((choice) => choice.label)).toEqual([
      "Share answer",
      "Allow for 1 hour",
      "Deny",
    ])
    expect(consentChoices[0].detail("Raya")).toMatch(/own words/)
    expect(consentChoices[1].detail("Raya")).toBe(
      "Your agent may quote what it reads. Raya's follow-ups in that thread are answered for an hour."
    )
    expect(consentChoices[2].detail("Raya")).toBe(
      "Nothing is shared. Raya is told you said no."
    )
  })

  it("are named to a screen reader by their words and whose request they are about", () => {
    expect(consentChoices.map((choice) => choice.name("Raya"))).toEqual([
      "Share answer to Raya's request",
      "Allow for 1 hour for Raya's request",
      "Deny Raya's request",
    ])
    // The two choices of a request filed in Oryxa are the same words.
    expect(shareAnswerName("Raya")).toBe(consentChoices[0].name("Raya"))
    expect(denyName("Raya")).toBe(consentChoices[2].name("Raya"))
    expect(SHARE_ANSWER).toBe(consentChoices[0].label)
    expect(DENY).toBe(consentChoices[2].label)
    // Someone who asks of themselves is "you", not "you's".
    expect(denyName("you")).toBe("Deny your request")
  })

  it("say the pending choice in the words of its button", () => {
    expect(
      consentChoices.map((choice) => consentChoicePending(choice.choice))
    ).toEqual(["Sharing…", "Allowing…", "Denying…"])
  })
})

describe("replying yourself", () => {
  it("has its words, and names its buttons by whose request they are about", () => {
    expect(REPLY_YOURSELF).toBe("Reply yourself")
    expect(SEND_REPLY).toBe("Send reply")
    expect(LET_MY_AGENT_ANSWER).toBe("Let my agent answer")
    expect(replyYourselfName("Raya")).toBe("Reply yourself to Raya's request")
    expect(letMyAgentAnswerName("Raya")).toBe(
      "Let my agent answer Raya's request"
    )
    // The visible words are in each name, for whoever speaks them.
    expect(replyYourselfName("Raya")).toContain(REPLY_YOURSELF)
    expect(letMyAgentAnswerName("Raya")).toContain(LET_MY_AGENT_ANSWER)
    // Someone who asks of themselves is "you", not "you's".
    expect(replyYourselfName("you")).toBe("Reply yourself to your request")
  })

  it("counts characters as Go does: trimmed, in code points", () => {
    expect(replyLength("")).toBe(0)
    expect(replyLength("  \n\t ")).toBe(0)
    expect(replyLength("  Yes.  ")).toBe(4)
    expect(replyLength("😀😀")).toBe(2)
    expect(replyLength("é".normalize("NFD"))).toBe(2)
  })

  it("takes one to 4,000 characters of trimmed text, and says what is wrong otherwise", () => {
    expect(replyProblem("")).toBe("Write your reply first.")
    expect(replyProblem("   ")).toBe("Write your reply first.")
    expect(replyProblem("a")).toBeUndefined()
    expect(replyProblem("a".repeat(4000))).toBeUndefined()
    expect(replyProblem(`  ${"a".repeat(4000)}  `)).toBeUndefined()
    expect(replyProblem("😀".repeat(4000))).toBeUndefined()
    expect(replyProblem("a".repeat(4001))).toBe(
      "That is too long: a reply holds at most 4,000 characters."
    )
  })

  it("shows how many characters it has from the last 500", () => {
    expect(REPLY_COUNTER_FROM).toBe(3500)
  })
})
