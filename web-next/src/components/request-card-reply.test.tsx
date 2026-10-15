// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { RequestCard } from "./request-card"
import { clockTime } from "./time-format"
import {
  ASKER,
  NOW,
  ORG,
  OWNER,
  REPLIED_AT,
  agentRequest,
  answer,
  approvedRequest,
  failure,
  fakeGo,
  json,
  repliedRequest,
  slackRequest,
} from "./request-card-fixtures"
import type { Route } from "./request-card-fixtures"
import type { AgentRequest } from "@/lib/agents/agent-requests"

const requestsBase = `/v1/organizations/${ORG}/agent-requests`
const replyPath = `${requestsBase}/irq_slack/reply`
const agentReplyPath = `${requestsBase}/irq_agent/reply`
const consentPath = `${requestsBase}/irq_slack/consent`
const decisionPath = `${requestsBase}/irq_agent/decision`
const reviewPath = `${requestsBase}/irq_slack/review`

let client: QueryClient

function renderCard(
  request: AgentRequest,
  {
    viewer = OWNER,
    routes = {},
  }: { viewer?: string; routes?: Record<string, Route> } = {}
) {
  const go = fakeGo(routes)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const view = render(
    <QueryClientProvider client={client}>
      <RequestCard organizationId={ORG} viewerId={viewer} request={request} />
    </QueryClientProvider>
  )
  return { go, ...view }
}

/** What the card's reply routes answer when the reply is taken. */
function taken(request: AgentRequest): Route {
  return () => ({ request })
}

const held = (): AgentRequest =>
  approvedRequest("summary", {
    status: "review",
    revision: 3,
    guard: { state: "held", kinds: ["PHONE"] },
    held: {
      result: answer("Call Priya on 555 0100."),
      masked_result: answer("Call Priya on {PHONE}."),
    },
  })

const fieldName = "Reply yourself to Raya's request"

function field(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: fieldName })
}

function write(text: string) {
  fireEvent.change(field(), { target: { value: text } })
}

/** The words on the buttons in `scope`, in order: a choice's by its label. */
function buttons(scope: HTMLElement = document.body) {
  return within(scope)
    .getAllByRole("button")
    .map(
      (button) =>
        button.querySelector("strong")?.textContent ?? button.textContent
    )
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(NOW)
})

afterEach(() => {
  cleanup()
  client.clear()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("Reply yourself, under a request's own choices", () => {
  it("is the third of a Slack ask's four choices, before Deny", async () => {
    renderCard(slackRequest())

    const group = await screen.findByRole("group", {
      name: "Choose what Krude may share with Raya",
    })
    const reply = screen.getByRole("button", {
      name: "Reply yourself to Raya's request",
    })
    expect(group.contains(reply)).toBe(true)
    expect(buttons(group)).toEqual([
      "Share answer",
      "Allow for 1 hour",
      "Reply yourself",
      "Deny",
    ])
    expect(reply.getAttribute("aria-expanded")).toBe("false")
    // It says what it does, and the field is closed.
    expect(
      document.getElementById(reply.getAttribute("aria-describedby") ?? "")
        ?.textContent
    ).toBe("Raya gets your words as you write them. Krude stays out.")
    expect(screen.queryByRole("textbox")).toBeNull()
  })

  it("opens a focused field that says what it does, with Send reply and Cancel", async () => {
    renderCard(slackRequest())

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )

    expect(document.activeElement).toBe(field())
    expect(field().placeholder).toBe("Your reply to Raya")
    expect(
      screen.getByText(
        "Raya gets your words as you write them. Your agent doesn't run."
      )
    ).toBeTruthy()
    expect(screen.getByRole("button", { name: "Send reply" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "Cancel" })).toBeTruthy()
    // The choices stay where they were.
    expect(
      screen.getByRole("button", { name: "Deny Raya's request" })
    ).toBeTruthy()
    // The choice that opened it says so.
    expect(
      screen
        .getByRole("button", { name: "Reply yourself to Raya's request" })
        .getAttribute("aria-expanded")
    ).toBe("true")
    // It is on the field by name, and the field is told what it says.
    expect(field().getAttribute("aria-describedby")).toBeTruthy()
  })

  it("closes on Cancel, drops what was written and gives focus back", async () => {
    renderCard(slackRequest())

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("Half a thought")
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))

    const quiet = screen.getByRole("button", {
      name: "Reply yourself to Raya's request",
    })
    expect(screen.queryByRole("textbox")).toBeNull()
    // Focus goes back to the choice that opened it.
    expect(document.activeElement).toBe(quiet)
    // Opened again, it starts empty.
    fireEvent.click(quiet)
    expect(field().value).toBe("")
  })

  it("sends the trimmed words, and the card settles to what was replied, with no choice made", async () => {
    const { go } = renderCard(slackRequest(), {
      routes: {
        [`POST ${replyPath}`]: taken(repliedRequest("Yes, go ahead.")),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("  Yes, go ahead.  \n")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    await screen.findByText("You replied.")
    expect(go.calls.find((call) => call.path === replyPath)?.body).toEqual({
      text: "Yes, go ahead.",
    })
    // Their agent never ran and no choice was made: the reply was all.
    expect(go.sent()).toEqual([`POST ${replyPath}`])
    expect(screen.getByText("Yes, go ahead.")).toBeTruthy()
    expect(
      screen.getByText(`Sent in Oryxa at ${clockTime(REPLIED_AT)}.`)
    ).toBeTruthy()
    // Nothing is left to choose, and the card takes focus, as it does after a choice.
    expect(screen.queryByRole("textbox")).toBeNull()
    expect(screen.queryByRole("group")).toBeNull()
    expect(screen.queryByRole("button")).toBeNull()
    expect(document.activeElement).toBe(screen.getByRole("article"))
  })

  it("sends with Command or Control and Enter, and Enter alone is a new line", async () => {
    const { go } = renderCard(slackRequest(), {
      routes: { [`POST ${replyPath}`]: taken(repliedRequest("Shipped.")) },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("Shipped.")
    fireEvent.keyDown(field(), { key: "Enter" })
    expect(go.count("POST", replyPath)).toBe(0)
    fireEvent.keyDown(field(), { key: "Enter", metaKey: true })

    await screen.findByText("You replied.")
    expect(go.count("POST", replyPath)).toBe(1)
  })

  it("is offered under a request an agent filed, between Share answer and Deny", async () => {
    const answered = {
      ...repliedRequest("Not before Friday."),
      id: "irq_agent",
      origin: "agent" as const,
      requested_permissions: [],
    }
    const { go } = renderCard(agentRequest(), {
      routes: { [`POST ${agentReplyPath}`]: taken(answered) },
    })

    expect(await screen.findByText("Waiting for you to approve.")).toBeTruthy()
    expect(buttons()).toEqual([
      "Add a note",
      "Share answer",
      "Reply yourself",
      "Deny",
    ])
    fireEvent.click(
      screen.getByRole("button", { name: "Reply yourself to Raya's request" })
    )
    write("Not before Friday.")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    await screen.findByText("You replied.")
    expect(go.sent()).toEqual([`POST ${agentReplyPath}`])
    expect(go.calls.find((call) => call.path === agentReplyPath)?.body).toEqual(
      { text: "Not before Friday." }
    )
  })

  it("is offered for an answer held for review, and replaces it", async () => {
    const { go } = renderCard(held(), {
      routes: {
        [`POST ${replyPath}`]: taken(
          repliedRequest("Call me instead.", "review")
        ),
      },
    })

    await screen.findByText(
      "Krude's answer includes a phone number. Review it before it's shared."
    )
    // The review's own choices come first, and Reply yourself after them.
    expect(buttons()).toEqual([
      "Don't send",
      "Send",
      "Send masked",
      "Reply yourself",
    ])
    fireEvent.click(
      screen.getByRole("button", { name: "Reply yourself to Raya's request" })
    )
    // It says it takes the held answer's place, not that the agent does not run.
    expect(
      screen.getByText("Raya gets your words instead of Krude's answer.")
    ).toBeTruthy()
    expect(screen.queryByText(/doesn't run/)).toBeNull()
    write("Call me instead.")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    await screen.findByText("You replied.")
    expect(go.sent()).toEqual([`POST ${replyPath}`])
    expect(go.calls.find((call) => call.path === replyPath)?.body).toEqual({
      text: "Call me instead.",
    })
    // What was held is gone with the review, and the reply is what stands.
    expect(screen.queryByText("Call Priya on 555 0100.")).toBeNull()
    expect(screen.queryByText("Send masked shares:")).toBeNull()
    expect(screen.getByText("Call me instead.")).toBeTruthy()
    expect(
      screen.getByText(
        `Sent in Oryxa at ${clockTime(REPLIED_AT)}, in place of Krude's answer.`
      )
    ).toBeTruthy()
  })

  it("is offered to nobody but the owner, and not once the request is closed", async () => {
    const asker = renderCard(slackRequest(), { viewer: ASKER })
    await screen.findByRole("article")
    expect(screen.queryByRole("button", { name: /Reply yourself/ })).toBeNull()
    asker.unmount()

    const heldAsker = renderCard(held(), { viewer: ASKER })
    await screen.findByRole("article")
    expect(screen.queryByRole("button", { name: /Reply yourself/ })).toBeNull()
    heldAsker.unmount()

    const ran = renderCard(slackRequest({ expires_at: "2026-09-29T12:10:00Z" }))
    await screen.findByText("Expired.")
    expect(screen.queryByRole("button", { name: /Reply yourself/ })).toBeNull()
    ran.unmount()

    renderCard(approvedRequest("summary"))
    await screen.findByText("Krude is working on it…")
    expect(screen.queryByRole("button", { name: /Reply yourself/ })).toBeNull()
  })
})

describe("an ask that needs the owner's own reply", () => {
  const first = (overrides: Partial<AgentRequest> = {}) =>
    slackRequest({ answer_by: "owner", ...overrides })

  it("leads with the open field and Send reply, then Let my agent answer, then Deny", async () => {
    renderCard(first())

    expect(await screen.findByText("Waiting for your reply.")).toBeTruthy()
    // Open from the start, but it takes no focus: a desk may hold several.
    expect(field().value).toBe("")
    expect(document.activeElement).not.toBe(field())
    // Nothing else says "Reply yourself" here, so the empty field does.
    expect(field().placeholder).toBe("Reply yourself to Raya")
    const card = screen.getByRole("article")
    expect(buttons(card)).toEqual(["Send reply", "Let my agent answer", "Deny"])
    expect(
      within(card)
        .getAllByRole("button")
        .map((button) => button.getAttribute("aria-label"))
    ).toEqual([
      null,
      "Let my agent answer Raya's request",
      "Deny Raya's request",
    ])
    expect(
      screen
        .getByRole("button", { name: "Send reply" })
        .getAttribute("data-primary")
    ).toBe("true")
    expect(
      screen
        .getByRole("button", { name: "Let my agent answer Raya's request" })
        .getAttribute("data-primary")
    ).toBeNull()
    // No Cancel: there is nothing to go back to, and no quieter way in.
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull()
    expect(screen.queryByRole("button", { name: /Reply yourself/ })).toBeNull()
    // The three of a Slack ask's consent choices are not offered.
    expect(screen.queryByRole("group")).toBeNull()
    expect(
      screen.queryByRole("button", { name: /Share answer|Allow for 1 hour/ })
    ).toBeNull()
    // What letting their agent answer means is said, and tied to its button.
    const agent = screen.getByRole("button", {
      name: "Let my agent answer Raya's request",
    })
    const detail = document.getElementById(
      agent.getAttribute("aria-describedby") ?? ""
    )
    expect(detail?.textContent).toBe(
      "Let my agent answer: it replies in its own words, without copying passages from your documents or sharing links."
    )
  })

  it("sends the reply as the primary action", async () => {
    const { go } = renderCard(first(), {
      routes: { [`POST ${replyPath}`]: taken(repliedRequest("Approved, 5k.")) },
    })

    await screen.findByText("Waiting for your reply.")
    write("Approved, 5k.")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    await screen.findByText("You replied.")
    expect(go.sent()).toEqual([`POST ${replyPath}`])
    expect(go.calls.find((call) => call.path === replyPath)?.body).toEqual({
      text: "Approved, 5k.",
    })
    expect(screen.getByText("Approved, 5k.")).toBeTruthy()
  })

  it("lets their agent answer a Slack ask with the Share answer choice", async () => {
    const { go } = renderCard(first(), {
      routes: {
        [`POST ${consentPath}`]: () => ({
          request: approvedRequest("summary", { answer_by: "owner" }),
        }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Let my agent answer Raya's request",
      })
    )

    await screen.findByText("Krude is working on it…")
    expect(go.calls.find((call) => call.path === consentPath)?.body).toEqual({
      choice: "summary",
    })
    expect(go.count("POST", replyPath)).toBe(0)
    expect(screen.queryByRole("textbox")).toBeNull()
  })

  it("denies a Slack ask", async () => {
    const { go } = renderCard(first(), {
      routes: {
        [`POST ${consentPath}`]: () => ({
          request: first({ status: "rejected", revision: 2 }),
        }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "Deny Raya's request" })
    )

    await screen.findByText("Denied.")
    expect(go.calls.find((call) => call.path === consentPath)?.body).toEqual({
      choice: "deny",
    })
  })

  it("lets their agent answer a request an agent filed with the tools it asked for, and has no note", async () => {
    const filed = agentRequest({ answer_by: "owner" })
    const { go } = renderCard(filed, {
      routes: {
        [`POST ${decisionPath}`]: () => ({
          ...filed,
          status: "approved",
          revision: 2,
        }),
      },
    })

    await screen.findByText("Waiting for your reply.")
    expect(
      screen.getByRole("textbox", { name: "Reply yourself to Raya's request" })
    ).toBeTruthy()
    expect(buttons()).toEqual(["Send reply", "Let my agent answer", "Deny"])
    // The reply is where the owner says why: a second field would be confusing.
    expect(screen.queryByRole("button", { name: "Add a note" })).toBeNull()
    expect(screen.queryByPlaceholderText(/A note for Raya/)).toBeNull()
    // What approving lets the agent read is still said, above.
    expect(screen.getByText("Krude may read team knowledge.")).toBeTruthy()

    fireEvent.click(
      screen.getByRole("button", { name: "Let my agent answer Raya's request" })
    )

    await screen.findByText("Krude is working on it…")
    expect(go.calls.find((call) => call.path === decisionPath)?.body).toEqual({
      decision: "approved",
      message: "Approved in Oryxa.",
      allowed_tools: ["knowledge"],
    })
  })

  it("denies a request an agent filed, without a reason of its own", async () => {
    const filed = agentRequest({ answer_by: "owner" })
    const { go } = renderCard(filed, {
      routes: {
        [`POST ${decisionPath}`]: () => ({
          ...filed,
          status: "rejected",
          revision: 2,
        }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "Deny Raya's request" })
    )

    await screen.findByText("Denied.")
    expect(go.calls.find((call) => call.path === decisionPath)?.body).toEqual({
      decision: "rejected",
      message: "Denied in Oryxa.",
    })
  })

  it("shows the choice that is on its way, and no second click gets through", async () => {
    let answerConsent: (response: Response) => void = () => {}
    const { go } = renderCard(first(), {
      routes: {
        [`POST ${consentPath}`]: () =>
          new Promise<Response>((resolve) => {
            answerConsent = resolve
          }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Let my agent answer Raya's request",
      })
    )

    await screen.findByText("Asking your agent…")
    for (const button of screen.getAllByRole<HTMLButtonElement>("button")) {
      expect(button.disabled).toBe(true)
    }
    fireEvent.click(screen.getByRole("button", { name: "Deny Raya's request" }))
    answerConsent(
      json({ request: approvedRequest("summary", { answer_by: "owner" }) })
    )
    await screen.findByText("Krude is working on it…")
    expect(go.count("POST", consentPath)).toBe(1)
  })

  it("tells the asker they wait for a reply, and offers them nothing", async () => {
    renderCard(first(), { viewer: ASKER })

    expect(await screen.findByText("Waiting for Ada to reply.")).toBeTruthy()
    expect(screen.queryByText("Waiting for Ada to decide.")).toBeNull()
    expect(screen.queryByRole("textbox")).toBeNull()
    expect(screen.queryByRole("button")).toBeNull()
  })

  it("says it ran out before the owner replied", async () => {
    const late = first({ expires_at: "2026-09-29T12:10:00Z" })
    const asker = renderCard(late, { viewer: ASKER })
    expect(await screen.findByText("Expired before Ada replied.")).toBeTruthy()
    asker.unmount()

    renderCard(late)
    await screen.findByText("Expired.")
    expect(screen.queryByRole("textbox")).toBeNull()
    expect(screen.queryByRole("button")).toBeNull()
  })

  it("keeps the four keyed choices when the agent answers or nothing is said", async () => {
    for (const answer_by of ["agent", undefined] as const) {
      const { unmount } = renderCard(slackRequest({ answer_by }))

      expect(await screen.findByText("Waiting for your choice.")).toBeTruthy()
      const group = screen.getByRole("group", {
        name: "Choose what Krude may share with Raya",
      })
      expect(
        within(group)
          .getAllByRole("button")
          .map((button) => button.querySelector("strong")?.textContent)
      ).toEqual(["Share answer", "Allow for 1 hour", "Reply yourself", "Deny"])
      expect(screen.queryByRole("textbox")).toBeNull()
      expect(
        screen.getByRole("button", { name: "Reply yourself to Raya's request" })
      ).toBeTruthy()
      expect(screen.queryByText("Let my agent answer")).toBeNull()
      unmount()
    }
  })

  it("keeps an agent-filed request's Share answer and Deny when it says the agent answers", async () => {
    renderCard(agentRequest({ answer_by: "agent" }))

    expect(await screen.findByText("Waiting for you to approve.")).toBeTruthy()
    expect(buttons()).toEqual([
      "Add a note",
      "Share answer",
      "Reply yourself",
      "Deny",
    ])
  })

  it("falls back to the quieter option for a held answer, whoever was to answer", async () => {
    renderCard({ ...held(), answer_by: "owner" })

    await screen.findByText(
      "Krude's answer includes a phone number. Review it before it's shared."
    )
    expect(buttons()).toEqual([
      "Don't send",
      "Send",
      "Send masked",
      "Reply yourself",
    ])
    expect(screen.queryByRole("textbox")).toBeNull()
  })

  it("is settled, as a choice is, once the owner chose to let their agent answer", async () => {
    renderCard(approvedRequest("summary", { answer_by: "owner" }))

    await screen.findByText("Krude is working on it…")
    expect(screen.queryByRole("textbox")).toBeNull()
    expect(screen.queryByRole("button")).toBeNull()
  })
})

describe("what was replied", () => {
  it("tells the owner they replied, and shows their words as they wrote them", async () => {
    renderCard(
      repliedRequest("We launch on **Oct 12**.\n\n- pending legal\n- no slip")
    )

    expect(await screen.findByText("You replied.")).toBeTruthy()
    expect(screen.queryByText("Answered.")).toBeNull()
    // Never the AI's labels: these are their own words.
    expect(screen.queryByText("What was shared")).toBeNull()
    expect(screen.queryByText("The answer")).toBeNull()
    // Plain text with its line breaks, never read as Markdown.
    const words = screen.getByText(/We launch on/)
    expect(words.textContent).toBe(
      "We launch on **Oct 12**.\n\n- pending legal\n- no slip"
    )
    expect(words.getAttribute("data-by")).toBe("owner")
    expect(words.querySelector("strong, li, ul")).toBeNull()
    expect(screen.queryByRole("button")).toBeNull()
  })

  it("tells the asker who replied, where and when, in place of the agent's label", async () => {
    renderCard(repliedRequest("Launch is on 12 October."), { viewer: ASKER })

    expect(await screen.findByText("Ada replied.")).toBeTruthy()
    expect(
      screen.getByText(`Sent in Oryxa at ${clockTime(REPLIED_AT)}.`)
    ).toBeTruthy()
    expect(screen.getByText("Launch is on 12 October.")).toBeTruthy()
    expect(screen.queryByText("Answered.")).toBeNull()
    expect(screen.queryByText("The answer")).toBeNull()
    expect(screen.queryByText("What was shared")).toBeNull()
    expect(screen.queryByText(/agent/i)).toBeNull()
  })

  it("says where a reply made in Slack was sent", async () => {
    const inSlack = repliedRequest("On it.")
    renderCard(
      {
        ...inSlack,
        decision: {
          ...inSlack.decision!,
          decided_by: { kind: "member", user_id: OWNER, surface: "slack" },
        },
      },
      { viewer: ASKER }
    )

    expect(
      await screen.findByText(`Sent in Slack at ${clockTime(REPLIED_AT)}.`)
    ).toBeTruthy()
  })

  it("tells only the owner that a reply took the place of their agent's held answer", async () => {
    const replaced = repliedRequest("Call me instead.", "review")
    const owner = renderCard(replaced)
    expect(await screen.findByText("You replied.")).toBeTruthy()
    expect(
      screen.getByText(
        `Sent in Oryxa at ${clockTime(REPLIED_AT)}, in place of Krude's answer.`
      )
    ).toBeTruthy()
    owner.unmount()

    renderCard(replaced, { viewer: ASKER })
    expect(await screen.findByText("Ada replied.")).toBeTruthy()
    expect(
      screen.getByText(`Sent in Oryxa at ${clockTime(REPLIED_AT)}.`)
    ).toBeTruthy()
    expect(screen.queryByText(/in place of/)).toBeNull()
    expect(screen.queryByText(/Krude/)).toBeNull()
  })

  it("tells the owner what the guard hid from their reply, and not the asker", async () => {
    const masked = repliedRequest("The password is hunter2.", "decision", {
      guard: { state: "masked", kinds: ["PASSWORD"] },
    })
    const owner = renderCard(masked)
    expect(
      await screen.findByText(
        `Sent in Oryxa at ${clockTime(REPLIED_AT)}. Shared with a password hidden.`
      )
    ).toBeTruthy()
    owner.unmount()

    renderCard(masked, { viewer: ASKER })
    expect(
      await screen.findByText(`Sent in Oryxa at ${clockTime(REPLIED_AT)}.`)
    ).toBeTruthy()
    expect(screen.queryByText(/hidden/)).toBeNull()
  })

  it("reads the decision alone when Go sends no mark on the answer", async () => {
    const request = repliedRequest("Done.")
    renderCard({
      ...request,
      result: { ...request.result!, written_by_owner: undefined },
    })

    expect(await screen.findByText("You replied.")).toBeTruthy()
  })
})

describe("a reply that lost the race", () => {
  const winner = {
    request_id: "irq_slack",
    status: "completed",
    revision: 3,
    decision: "approved",
    consent: "summary",
    decided_by: { kind: "member", user_id: OWNER, surface: "slack" },
    decided_at: "2026-09-29T12:02:00Z",
  }
  const lost =
    (body: unknown = { winner }): Route =>
    () =>
      json(
        {
          error: { code: "already_decided", message: "already decided" },
          ...(body as object),
        },
        409
      )

  async function sendLosing(route: Route, request = slackRequest()) {
    const view = renderCard(request, {
      routes: { [`POST ${replyPath}`]: route },
    })
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("I will take it from here.")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))
    return view
  }

  it("shows who won, as a choice does, and takes the field away", async () => {
    const { go } = await sendLosing(lost())

    const banner = await screen.findByText(/Already decided\./)
    expect(banner.parentElement?.textContent).toBe(
      `Already decided. Approved in Slack by you at ${clockTime("2026-09-29T12:02:00Z")}. Answer shared.`
    )
    expect(screen.queryByRole("textbox")).toBeNull()
    expect(screen.queryByRole("button")).toBeNull()
    expect(screen.queryByRole("alert")).toBeNull()
    expect(go.sent()).toEqual([`POST ${replyPath}`])
  })

  it("says so when the winner replied", async () => {
    await sendLosing(
      lost({
        winner: {
          ...winner,
          consent: "reply",
          decided_by: { kind: "member", user_id: OWNER, surface: "web" },
        },
      })
    )

    const banner = await screen.findByText(/Already decided\./)
    expect(banner.parentElement?.textContent).toBe(
      `Already decided. Replied in Oryxa by you at ${clockTime("2026-09-29T12:02:00Z")}.`
    )
  })

  it("keeps what was written, to copy, as it could not be sent", async () => {
    await sendLosing(lost())

    await screen.findByText(/Already decided\./)
    expect(
      screen.getByText(
        "Your reply wasn't sent. It's here if you want to copy it."
      )
    ).toBeTruthy()
    expect(screen.getByText("I will take it from here.")).toBeTruthy()
  })

  it("still says it lost when Go names no winner", async () => {
    await sendLosing(lost({}))

    const banner = await screen.findByText(/Already decided\./)
    expect(banner.parentElement?.textContent).toBe(
      "Already decided. Another click got there first."
    )
  })

  it("says a held answer was reviewed first, as a review that lost does", async () => {
    const { go } = renderCard(held(), {
      routes: { [`POST ${replyPath}`]: lost() },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("Sending this instead.")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "This answer was reviewed already, or has expired."
    )
    // The winner's record is of the decision, which says nothing of the review.
    expect(screen.queryByText(/Already decided\./)).toBeNull()
    expect(field().value).toBe("Sending this instead.")
    expect(go.sent()).toEqual([`POST ${replyPath}`])
  })

  it("keeps words that cannot be sent once the request is decided elsewhere", async () => {
    const { rerender } = renderCard(slackRequest())

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("A careful answer I wrote.")
    // Decided in Slack while they wrote: the request arrives settled.
    rerender(
      <QueryClientProvider client={client}>
        <RequestCard
          organizationId={ORG}
          viewerId={OWNER}
          request={approvedRequest("summary")}
        />
      </QueryClientProvider>
    )

    expect(await screen.findByText("Krude is working on it…")).toBeTruthy()
    expect(screen.queryByRole("textbox")).toBeNull()
    expect(screen.getByText("A careful answer I wrote.")).toBeTruthy()
    expect(
      screen.getByText(
        "Your reply wasn't sent. It's here if you want to copy it."
      )
    ).toBeTruthy()
  })

  it("does not bring back words the owner gave up for another choice", async () => {
    renderCard(slackRequest(), {
      routes: {
        [`POST ${consentPath}`]: () => ({
          request: approvedRequest("summary"),
        }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("Never mind, let the agent.")
    fireEvent.click(
      screen.getByRole("button", { name: "Share answer to Raya's request" })
    )

    await screen.findByText("Krude is working on it…")
    expect(screen.queryByText("Never mind, let the agent.")).toBeNull()
    expect(screen.queryByText(/wasn't sent/)).toBeNull()
  })
})

describe("what cannot be sent", () => {
  async function open(
    request = slackRequest(),
    routes: Record<string, Route> = {}
  ) {
    const view = renderCard(request, { routes })
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    return view
  }

  it("is refused empty, before anything is asked, with the field marked", async () => {
    const { go } = await open()

    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "Write your reply first."
    )
    expect(go.sent()).toEqual([])
    expect(field().getAttribute("aria-invalid")).toBe("true")
    expect(document.activeElement).toBe(field())

    // Writing clears what was said of the empty field.
    write("Yes.")
    expect(screen.queryByRole("alert")).toBeNull()
    expect(field().getAttribute("aria-invalid")).toBeNull()
  })

  it("is refused when it is only white space", async () => {
    const { go } = await open()

    write("   \n\t  ")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "Write your reply first."
    )
    expect(go.sent()).toEqual([])
  })

  it("is refused over 4,000 characters, and says so as the limit is passed", async () => {
    const { go } = await open()

    write("a".repeat(4001))
    const count = screen.getByText("4001 / 4000")
    expect(count.getAttribute("data-over")).toBe("true")
    expect(field().getAttribute("aria-invalid")).toBe("true")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "That is too long: a reply holds at most 4,000 characters."
    )
    expect(go.sent()).toEqual([])
  })

  it("takes exactly 4,000 characters, counting them as Go does", async () => {
    const reply = "😀".repeat(4000)
    const { go } = await open(slackRequest(), {
      [`POST ${replyPath}`]: taken(repliedRequest(reply)),
    })

    write(`  ${reply}  `)
    // Characters, not UTF-16 units, and not the white space Go trims.
    expect(screen.getByText("4000 / 4000").getAttribute("data-over")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    await screen.findByText("You replied.")
    expect(
      (
        go.calls.find((call) => call.path === replyPath)?.body as {
          text: string
        }
      ).text
    ).toBe(reply)
  })

  it("counts characters only near the limit", async () => {
    await open()

    write("A short reply.")
    expect(screen.queryByText(/ \/ 4000/)).toBeNull()
    write("a".repeat(3499))
    expect(screen.queryByText(/ \/ 4000/)).toBeNull()
    write("a".repeat(3500))
    expect(screen.getByText("3500 / 4000")).toBeTruthy()
    // The count is read with the field.
    const described = field().getAttribute("aria-describedby") ?? ""
    expect(
      described
        .split(" ")
        .map((id) => document.getElementById(id)?.textContent)
        .join("|")
    ).toContain("3500 / 4000")
    write("a".repeat(10))
    expect(screen.queryByText(/ \/ 4000/)).toBeNull()
  })
})

describe("a reply that could not be sent", () => {
  it("keeps what was written, says why, and sends again", async () => {
    let fail = true
    const { go } = renderCard(slackRequest(), {
      routes: {
        [`POST ${replyPath}`]: () =>
          fail
            ? failure(500, "internal", "internal server error")
            : { request: repliedRequest("Keep this draft.") },
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("Keep this draft.")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "Couldn't send your reply. Try again."
    )
    // The draft is still there, editable, and focus is back in the field.
    expect(field().value).toBe("Keep this draft.")
    expect(field().readOnly).toBe(false)
    expect(document.activeElement).toBe(field())
    expect(
      screen.getByRole<HTMLButtonElement>("button", { name: "Send reply" })
        .disabled
    ).toBe(false)
    expect(screen.queryByText(/wasn't sent/)).toBeNull()

    fail = false
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    await screen.findByText("You replied.")
    expect(go.count("POST", replyPath)).toBe(2)
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("keeps the reply of an ask that leads with it, too", async () => {
    renderCard(slackRequest({ answer_by: "owner" }), {
      routes: {
        [`POST ${replyPath}`]: () => failure(503, "unavailable", "try later"),
      },
    })

    await screen.findByText("Waiting for your reply.")
    write("A reply that must not be lost.")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "Couldn't send your reply. Try again."
    )
    expect(field().value).toBe("A reply that must not be lost.")
    expect(buttons(screen.getByRole("article"))).toEqual([
      "Send reply",
      "Let my agent answer",
      "Deny",
    ])
  })

  it("says why a request that ran out cannot be replied to", async () => {
    renderCard(slackRequest(), {
      routes: {
        [`POST ${replyPath}`]: () =>
          failure(409, "request_expired", "agent request has expired"),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("Too late?")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "This request expired before you replied."
    )
    expect(field().value).toBe("Too late?")
  })

  it("says what Go said of a reply it would not take", async () => {
    renderCard(slackRequest(), {
      routes: {
        [`POST ${replyPath}`]: () =>
          failure(400, "invalid_request", "invalid reply: text is too long"),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("Words.")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "Text is too long."
    )
  })

  it("shows the reply on its way, and no second send gets through", async () => {
    let answerReply: (response: Response) => void = () => {}
    const { go } = renderCard(slackRequest(), {
      routes: {
        [`POST ${replyPath}`]: () =>
          new Promise<Response>((resolve) => {
            answerReply = resolve
          }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("On its way.")
    fireEvent.click(screen.getByRole("button", { name: "Send reply" }))

    await screen.findByText("Sending…")
    // Nothing else can be pressed, and the words cannot change while they go.
    for (const button of screen.getAllByRole<HTMLButtonElement>("button")) {
      expect(button.disabled).toBe(true)
    }
    expect(field().readOnly).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "Deny Raya's request" }))
    fireEvent.keyDown(field(), { key: "Enter", ctrlKey: true })
    answerReply(json({ request: repliedRequest("On its way.") }))

    await screen.findByText("You replied.")
    expect(go.sent()).toEqual([`POST ${replyPath}`])
  })

  it("does not send while another choice is on its way", async () => {
    let answerConsent: (response: Response) => void = () => {}
    const { go } = renderCard(slackRequest(), {
      routes: {
        [`POST ${consentPath}`]: () =>
          new Promise<Response>((resolve) => {
            answerConsent = resolve
          }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Reply yourself to Raya's request",
      })
    )
    write("Words.")
    fireEvent.click(screen.getByRole("button", { name: "Deny Raya's request" }))
    await waitFor(() =>
      expect(
        screen.getByRole<HTMLButtonElement>("button", { name: "Send reply" })
          .disabled
      ).toBe(true)
    )
    fireEvent.keyDown(field(), { key: "Enter", metaKey: true })
    answerConsent(
      json({ request: slackRequest({ status: "rejected", revision: 2 }) })
    )

    await screen.findByText("Denied.")
    expect(go.count("POST", replyPath)).toBe(0)
  })
})

describe("the review's own choices, beside a reply", () => {
  it("still sends an answer as it was held", async () => {
    const { go } = renderCard(held(), {
      routes: {
        [`POST ${reviewPath}`]: () => ({
          ...held(),
          status: "completed",
          revision: 4,
          held: undefined,
          result: answer("Call Priya on {PHONE}."),
        }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Send Krude's answer to Raya, masked",
      })
    )

    await screen.findByText("Answered.")
    expect(go.count("POST", reviewPath)).toBe(1)
    expect(go.count("POST", replyPath)).toBe(0)
    expect(screen.queryByRole("button", { name: /Reply yourself/ })).toBeNull()
  })
})
