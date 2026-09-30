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
import {
  QueryClient,
  QueryClientProvider,
  focusManager,
} from "@tanstack/react-query"
import { RequestCard, RequestCardById } from "./request-card"
import { clockTime } from "./direct-message-format"
import {
  ASKER,
  NOW,
  ORG,
  OWNER,
  agentRequest,
  answer,
  approvedRequest,
  failure,
  fakeGo,
  json,
  lease,
  requestPath,
  slackRequest,
} from "./request-card-fixtures"
import type { Route } from "./request-card-fixtures"
import type { AgentLease, AgentRequest } from "@/lib/agents/agent-requests"

const requestsBase = `/v1/organizations/${ORG}/agent-requests`
const consentPath = `${requestsBase}/irq_slack/consent`
const reviewPath = `${requestsBase}/irq_slack/review`
const secretPath = `${requestsBase}/irq_slack/secret`
const decisionPath = `${requestsBase}/irq_agent/decision`
const leasePath = `/v1/organizations/${ORG}/agent-leases/ils_1`

let client: QueryClient

function renderCard(
  request: AgentRequest,
  {
    viewer = OWNER,
    routes = {},
    knownLease,
  }: {
    viewer?: string
    routes?: Record<string, Route>
    knownLease?: AgentLease
  } = {}
) {
  const go = fakeGo(routes)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const view = render(
    <QueryClientProvider client={client}>
      <RequestCard
        organizationId={ORG}
        viewerId={viewer}
        request={request}
        lease={knownLease}
      />
    </QueryClientProvider>
  )
  return { go, ...view }
}

function decidedByOwner(
  consent: "summary" | "access" | undefined,
  decision: "approved" | "rejected",
  surface = "web"
): AgentRequest {
  return slackRequest({
    status: decision === "approved" ? "approved" : "rejected",
    revision: 2,
    decision: {
      decision,
      message: "",
      allowed_tools: [],
      granted_permissions: [],
      decided_at: "2026-09-29T12:31:00Z",
      decided_by: { kind: "member", user_id: OWNER, surface },
      ...(consent ? { consent } : {}),
    },
  })
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

describe("a Slack ask, for the member it is addressed to", () => {
  it("shows who asked, the question, where from, and the three choices in plain words", async () => {
    renderCard(slackRequest())

    const card = await screen.findByRole("article", {
      name: "Raya Patel asked you, in Slack",
    })
    expect(within(card).getByText("Slack")).toBeTruthy()
    expect(
      within(card).getByText("What is the plan for the EU launch?")
    ).toBeTruthy()
    expect(within(card).getByText("Waiting for your choice.")).toBeTruthy()
    expect(
      within(card).getByText("Krude may read team knowledge.")
    ).toBeTruthy()

    const group = within(card).getByRole("group", {
      name: "Choose what Krude may share with Raya",
    })
    // The words on the buttons, in Slack's order, and what a screen reader
    // calls each: its words and whose request they are about.
    expect(
      within(group)
        .getAllByRole("button")
        .map((button) => [
          button.querySelector("strong")?.textContent,
          button.getAttribute("aria-label"),
        ])
    ).toEqual([
      ["Share answer", "Share answer to Raya's request"],
      ["Allow for 1 hour", "Allow for 1 hour for Raya's request"],
      ["Deny", "Deny Raya's request"],
    ])
    expect(
      within(group).getByText(
        "Your agent answers in its own words. It won't copy passages from your documents or share links."
      )
    ).toBeTruthy()
    expect(
      within(group).getByText(
        "Your agent may quote what it reads. Raya's follow-ups in that thread are answered for an hour."
      )
    ).toBeTruthy()
    expect(
      within(group).getByText("Nothing is shared. Raya is told you said no.")
    ).toBeTruthy()
    // One set of words: the old names of these choices are nowhere on the card.
    expect(card.textContent).not.toMatch(/Summary only|AI summary/)
    expect(card.textContent).not.toMatch(/Approve|Decline|without tools/)
  })

  it("offers the asker no choice, only where it stands", async () => {
    renderCard(slackRequest(), { viewer: ASKER })

    const card = await screen.findByRole("article", {
      name: "You asked Ada Lovelace, in Slack",
    })
    expect(within(card).getByText("Waiting for Ada to decide.")).toBeTruthy()
    expect(within(card).queryByRole("group")).toBeNull()
    expect(within(card).queryByRole("button", { name: /Deny/ })).toBeNull()
    expect(
      within(card).queryByRole("button", { name: /Share answer|Allow for/ })
    ).toBeNull()
  })

  it("records Share answer, and then says how it was decided", async () => {
    const { go } = renderCard(slackRequest(), {
      routes: {
        [`POST ${consentPath}`]: () => ({
          request: decidedByOwner("summary", "approved"),
        }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Share answer to Raya's request",
      })
    )

    await screen.findByText(
      `Approved in Oryxa by you at ${clockTime("2026-09-29T12:31:00Z")}. Answer shared.`
    )
    expect(go.calls.find((call) => call.path === consentPath)?.body).toEqual({
      choice: "summary",
    })
    expect(screen.queryByRole("group")).toBeNull()
    expect(screen.getByText("Krude is working on it…")).toBeTruthy()
  })

  it("records a denial, and no choice is left", async () => {
    renderCard(slackRequest(), {
      routes: {
        [`POST ${consentPath}`]: () => ({
          request: decidedByOwner(undefined, "rejected"),
        }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "Deny Raya's request" })
    )

    await screen.findByText(
      `Denied in Oryxa by you at ${clockTime("2026-09-29T12:31:00Z")}.`
    )
    expect(screen.queryByRole("group")).toBeNull()
  })

  it("shows the choice on its way, and no second click gets through", async () => {
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
        name: "Allow for 1 hour for Raya's request",
      })
    )

    await screen.findByText("Allowing…")
    for (const button of within(screen.getByRole("group")).getAllByRole(
      "button"
    )) {
      expect((button as HTMLButtonElement).disabled).toBe(true)
    }
    fireEvent.click(screen.getByRole("button", { name: "Deny Raya's request" }))
    answerConsent(
      json({ request: decidedByOwner("access", "approved"), lease })
    )
    await screen.findByText(/Approved in Oryxa by you/)
    expect(go.calls.filter((call) => call.path === consentPath)).toHaveLength(1)
  })

  it("says why a choice could not be recorded, and leaves the choices to try again", async () => {
    renderCard(slackRequest(), {
      routes: {
        [`POST ${consentPath}`]: () =>
          failure(503, "consent_unavailable", "slack consent is not available"),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "Deny Raya's request" })
    )

    expect((await screen.findByRole("alert")).textContent).toBe(
      "Slack consent isn't set up on this server."
    )
    expect(
      screen.getByRole("button", { name: "Deny Raya's request" })
    ).toHaveProperty("disabled", false)
  })

  it("says a request that ran out, and offers nothing", async () => {
    renderCard(slackRequest({ expires_at: "2026-09-29T12:10:00Z" }))

    await screen.findByText("Expired.")
    expect(screen.queryByRole("group")).toBeNull()
  })
})

describe("a choice that lost the race", () => {
  const winner = {
    request_id: "irq_slack",
    status: "approved",
    revision: 2,
    decision: "approved",
    consent: "summary",
    decided_by: { kind: "member", user_id: OWNER, surface: "slack" },
    decided_at: "2026-09-29T12:02:00Z",
  }

  it("shows who won, and takes the choices away", async () => {
    const { go } = renderCard(slackRequest(), {
      routes: {
        [`POST ${consentPath}`]: () =>
          json(
            {
              error: { code: "already_decided", message: "already decided" },
              winner,
            },
            409
          ),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "Deny Raya's request" })
    )

    const lost = await screen.findByText(/Already decided\./)
    expect(lost.parentElement?.textContent).toBe(
      `Already decided. Approved in Slack by you at ${clockTime("2026-09-29T12:02:00Z")}. Answer shared.`
    )
    expect(screen.queryByRole("group")).toBeNull()
    expect(screen.queryByRole("alert")).toBeNull()
    expect(go.sent()).toEqual([`POST ${consentPath}`])
  })

  it("settles when the request changes elsewhere", async () => {
    let served: AgentRequest = slackRequest()
    fakeGo({ [`GET ${requestPath("irq_slack")}`]: () => served })
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={client}>
        <RequestCardById
          organizationId={ORG}
          viewerId={OWNER}
          requestId="irq_slack"
        />
      </QueryClientProvider>
    )
    await screen.findByRole("group", { name: /Choose what Krude may share/ })

    // Decided in Slack, then the tab comes back into view.
    served = approvedRequest("summary")
    focusManager.setFocused(false)
    focusManager.setFocused(true)

    await screen.findByText(
      `Approved in Slack by you at ${clockTime("2026-09-29T12:02:00Z")}. Answer shared.`
    )
    expect(screen.queryByRole("group", { name: /Choose what/ })).toBeNull()
    focusManager.setFocused(undefined)
  })
})

describe("access the owner gave", () => {
  it("shows the lease the choice gave, and ends it", async () => {
    const { go } = renderCard(slackRequest(), {
      routes: {
        [`POST ${consentPath}`]: () => ({
          request: decidedByOwner("access", "approved"),
          lease,
        }),
        [`DELETE ${leasePath}`]: () => ({
          lease: { ...lease, revoked_at: "2026-09-29T12:40:00Z" },
        }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Allow for 1 hour for Raya's request",
      })
    )

    await screen.findByText(
      `Raya's follow-ups in that Slack thread are answered without asking you until ${clockTime(lease.expires_at)}.`
    )
    fireEvent.click(screen.getByRole("button", { name: "End Raya's access" }))

    await screen.findByText(
      "You ended Raya's access. Their next question in that thread asks you again."
    )
    expect(go.count("DELETE", leasePath)).toBe(1)
    expect(screen.queryByRole("button", { name: /End Raya/ })).toBeNull()
  })

  it("ends the access a follow-up was approved under, by the lease it names", async () => {
    const followUp: AgentRequest = slackRequest({
      status: "approved",
      revision: 2,
      decision: {
        decision: "approved",
        message: "",
        allowed_tools: [],
        granted_permissions: [],
        decided_at: "2026-09-29T12:20:00Z",
        decided_by: {
          kind: "lease",
          user_id: OWNER,
          surface: "lease",
          reason: "ils_1",
        },
        consent: "access",
      },
    })
    const { go } = renderCard(followUp, {
      routes: {
        [`DELETE ${leasePath}`]: () => ({
          lease: { ...lease, revoked_at: "2026-09-29T12:40:00Z" },
        }),
      },
    })

    await screen.findByText(
      `Approved at ${clockTime("2026-09-29T12:20:00Z")} under the access you gave Raya.`
    )
    fireEvent.click(
      await screen.findByRole("button", { name: "End Raya's access" })
    )

    await screen.findByText(/You ended Raya's access/)
    expect(go.count("DELETE", leasePath)).toBe(1)
  })

  it("does not offer to end access that has already ended", async () => {
    renderCard(approvedRequest("access"), {
      knownLease: { ...lease, revoked_at: "2026-09-29T12:20:00Z" },
    })

    await screen.findByText(/You ended Raya's access/)
    expect(screen.queryByRole("button", { name: /End Raya/ })).toBeNull()
  })

  it("says so when the access had already ended", async () => {
    renderCard(approvedRequest("access"), {
      knownLease: lease,
      routes: {
        [`DELETE ${leasePath}`]: () =>
          failure(404, "not_found", "resource not found"),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "End Raya's access" })
    )

    expect((await screen.findByRole("alert")).textContent).toBe(
      "That access has already ended."
    )
  })
})

describe("a request an agent filed in Oryxa, which has two choices", () => {
  const sharedByOwner = (): AgentRequest => ({
    ...agentRequest(),
    status: "approved",
    revision: 2,
    decision: {
      decision: "approved",
      message: "Approved in Oryxa.",
      allowed_tools: ["knowledge"],
      granted_permissions: [],
      decided_at: "2026-09-29T12:31:00Z",
      decided_by: { kind: "member", user_id: OWNER, surface: "web" },
    },
  })

  it("offers Share answer and Deny, and nothing between them", async () => {
    renderCard(agentRequest())

    expect(await screen.findByText("Waiting for you to approve.")).toBeTruthy()
    expect(screen.queryByRole("group")).toBeNull()
    // What approving lets the agent read is said once, above the choices.
    expect(screen.getByText("Krude may read team knowledge.")).toBeTruthy()
    expect(
      screen
        .getAllByRole("button")
        .map((button) => [
          button.textContent,
          button.getAttribute("aria-label"),
        ])
    ).toEqual([
      ["Add a note", null],
      ["Deny", "Deny Raya's request"],
      ["Share answer", "Share answer to Raya's request"],
    ])
    expect(
      screen
        .getByRole("button", { name: "Share answer to Raya's request" })
        .getAttribute("data-primary")
    ).toBe("true")
  })

  it("shares the answer with the tools it asked for, and says where it was decided", async () => {
    const { go } = renderCard(agentRequest(), {
      routes: { [`POST ${decisionPath}`]: sharedByOwner },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Share answer to Raya's request",
      })
    )

    await screen.findByText(
      `Approved in Oryxa by you at ${clockTime("2026-09-29T12:31:00Z")}.`
    )
    expect(go.calls.find((call) => call.path === decisionPath)?.body).toEqual({
      decision: "approved",
      message: "Approved in Oryxa.",
      allowed_tools: ["knowledge"],
    })
  })

  it("denies with a note", async () => {
    const { go } = renderCard(agentRequest(), {
      routes: {
        [`POST ${decisionPath}`]: () => ({
          ...agentRequest(),
          status: "rejected",
          revision: 2,
          decision: {
            decision: "rejected",
            message: "Not this week.",
            allowed_tools: [],
            granted_permissions: [],
            decided_at: "2026-09-29T12:31:00Z",
            decided_by: { kind: "member", user_id: OWNER, surface: "web" },
          },
        }),
      },
    })

    fireEvent.click(await screen.findByRole("button", { name: "Add a note" }))
    fireEvent.change(
      screen.getByPlaceholderText("A note for Raya (optional)"),
      {
        target: { value: "Not this week." },
      }
    )
    fireEvent.click(screen.getByRole("button", { name: "Deny Raya's request" }))

    await screen.findByText(
      `Denied in Oryxa by you at ${clockTime("2026-09-29T12:31:00Z")}.`
    )
    expect(go.calls.find((call) => call.path === decisionPath)?.body).toEqual({
      decision: "rejected",
      message: "Not this week.",
    })
  })

  it("says where it was denied when there is no note to say", async () => {
    const { go } = renderCard(agentRequest(), {
      routes: {
        [`POST ${decisionPath}`]: () => ({
          ...agentRequest(),
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

  it("shows the choice on its way, and no second click gets through", async () => {
    let answerDecision: (response: Response) => void = () => {}
    const { go } = renderCard(agentRequest(), {
      routes: {
        [`POST ${decisionPath}`]: () =>
          new Promise<Response>((resolve) => {
            answerDecision = resolve
          }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Share answer to Raya's request",
      })
    )

    await screen.findByText("Sharing…")
    for (const name of [/Deny/, /Share answer/]) {
      expect(
        screen.getByRole<HTMLButtonElement>("button", { name }).disabled
      ).toBe(true)
    }
    fireEvent.click(screen.getByRole("button", { name: "Deny Raya's request" }))
    answerDecision(json(sharedByOwner()))
    await screen.findByText(/Approved in Oryxa by you/)
    expect(go.calls.filter((call) => call.path === decisionPath)).toHaveLength(
      1
    )
  })

  it("never offers an approval without tools, nor says what one means", async () => {
    for (const requested_tools of [["knowledge"], ["apps", "knowledge"], []]) {
      const { unmount } = renderCard(agentRequest({ requested_tools }))

      await screen.findByRole("button", {
        name: "Share answer to Raya's request",
      })
      expect(screen.queryByRole("button", { name: /without tools/ })).toBeNull()
      expect(screen.queryByRole("button", { name: /Approve/ })).toBeNull()
      expect(screen.queryByText(/can’t look anything up/)).toBeNull()
      expect(document.body.textContent).not.toMatch(/without tools/i)
      unmount()
    }
  })

  it("allows no tool for a request that asked for none", async () => {
    const { go } = renderCard(agentRequest({ requested_tools: [] }), {
      routes: {
        [`POST ${decisionPath}`]: () => ({
          ...agentRequest({ requested_tools: [] }),
          status: "approved",
          revision: 2,
        }),
      },
    })

    const share = await screen.findByRole("button", {
      name: "Share answer to Raya's request",
    })
    // The same two choices as any request has, and no line on what it may read.
    expect(screen.queryByText(/may read/)).toBeNull()
    fireEvent.click(share)

    await screen.findByText("Krude is working on it…")
    expect(go.calls.find((call) => call.path === decisionPath)?.body).toEqual({
      decision: "approved",
      message: "Approved in Oryxa.",
      allowed_tools: [],
    })
  })

  it("offers the asker no choice", async () => {
    renderCard(agentRequest(), { viewer: ASKER })

    await screen.findByText("Waiting for Ada to decide.")
    expect(
      screen.queryByRole("button", { name: /Share answer|Deny/ })
    ).toBeNull()
    expect(screen.queryByRole("button", { name: /Approve/ })).toBeNull()
  })

  it("says why a decision could not be recorded", async () => {
    renderCard(agentRequest(), {
      routes: {
        [`POST ${decisionPath}`]: () =>
          failure(409, "invalid_state", "decided already"),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "Deny Raya's request" })
    )

    expect((await screen.findByRole("alert")).textContent).toBe(
      "This request was decided already, or expired."
    )
  })
})

describe("an answer held for review", () => {
  const held = (masked = true): AgentRequest =>
    approvedRequest("summary", {
      status: "review",
      revision: 3,
      guard: { state: "held", kinds: ["PHONE"] },
      held: {
        result: answer("Call Priya on 555 0100."),
        ...(masked ? { masked_result: answer("Call Priya on {PHONE}.") } : {}),
      },
    })

  it("shows the owner why, the answer, and what Send masked shares", async () => {
    renderCard(held())

    expect(
      await screen.findByText(
        "Krude's answer includes a phone number. Review it before it's shared."
      )
    ).toBeTruthy()
    expect(screen.getByText("Call Priya on 555 0100.")).toBeTruthy()
    expect(screen.getByText("Send masked shares:")).toBeTruthy()
    expect(screen.getByText("Call Priya on {PHONE}.")).toBeTruthy()
    for (const name of [
      "Don't send Krude's answer to Raya",
      "Send Krude's answer to Raya",
      "Send Krude's answer to Raya, masked",
    ]) {
      expect(screen.getByRole("button", { name })).toBeTruthy()
    }
  })

  it("sends it masked, and the card says it was answered", async () => {
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
    expect(go.calls.find((call) => call.path === reviewPath)?.body).toEqual({
      action: "send_masked",
    })
    expect(screen.queryByText("Send masked shares:")).toBeNull()
    expect(screen.getByText("What was shared")).toBeTruthy()
  })

  it("offers no masked version of an answer that could not be checked", async () => {
    renderCard(
      approvedRequest("summary", {
        status: "review",
        revision: 3,
        guard: { state: "held", kinds: ["UNCHECKED"] },
        held: { result: answer("Some answer.") },
      })
    )

    await screen.findByText(
      "Krude's answer couldn't be checked automatically. Review it before it's shared."
    )
    expect(screen.queryByText("Send masked shares:")).toBeNull()
    expect(screen.queryByRole("button", { name: /masked/ })).toBeNull()
    expect(
      screen.getByRole("button", { name: /^Send Krude's answer/ })
    ).toBeTruthy()
  })

  it("offers no masked version of an answer that broke the summary rule, whatever Go carries", async () => {
    renderCard(
      approvedRequest("summary", {
        status: "review",
        revision: 3,
        guard: { state: "held", kinds: ["COPIED_FROM_SOURCE"] },
        held: {
          result: answer("A long passage, word for word."),
          masked_result: answer("A long passage, word for word."),
        },
      })
    )

    await screen.findByText(
      "Krude's answer copies long passages from what it read, which a summary shouldn't. Review it before it's shared."
    )
    expect(screen.getByText("A long passage, word for word.")).toBeTruthy()
    expect(screen.queryByText("Send masked shares:")).toBeNull()
    expect(screen.queryByRole("button", { name: /masked/ })).toBeNull()
    // It is sent as it was held, or withheld.
    expect(
      screen.getByRole("button", { name: "Send Krude's answer to Raya" })
    ).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "Don't send Krude's answer to Raya" })
    ).toBeTruthy()
  })

  it("says an answer the reads do not support, in words", async () => {
    renderCard(
      approvedRequest("summary", {
        status: "review",
        revision: 3,
        guard: { state: "held", kinds: ["UNGROUNDED", "NO_SOURCES"] },
        held: { result: answer("A confident answer.") },
      })
    )

    expect(
      await screen.findByText(
        "Krude's answer isn't based on anything it read and isn't supported by what it read. Review it before it's shared."
      )
    ).toBeTruthy()
    expect(document.body.textContent).not.toMatch(/UNGROUNDED|NO_SOURCES/)
  })

  it("withholds it, and says why when the review was refused", async () => {
    renderCard(held(), {
      routes: {
        [`POST ${reviewPath}`]: () =>
          failure(409, "request_expired", "agent request has expired"),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Don't send Krude's answer to Raya",
      })
    )

    expect((await screen.findByRole("alert")).textContent).toBe(
      "This request expired before its answer was reviewed."
    )
  })

  it("tells the asker only that it waits, and never the answer", async () => {
    const { container } = renderCard(held(), { viewer: ASKER })

    await screen.findByText("Waiting for Ada to review the answer.")
    expect(container.textContent).not.toMatch(/priya/i)
    expect(container.textContent).not.toMatch(/phone/i)
    expect(screen.queryByRole("button", { name: /Send/ })).toBeNull()
  })

  it("says when the held answer did not come with the request", async () => {
    renderCard(
      approvedRequest("summary", {
        status: "review",
        revision: 3,
        guard: { state: "held", kinds: ["PHONE"] },
      })
    )

    await screen.findByText("The held answer didn't come with this request.")
    expect(screen.getByRole("button", { name: "Load it again" })).toBeTruthy()
    expect(
      screen.getByRole("button", { name: "Don't send Krude's answer to Raya" })
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: /^Send Krude's/ })).toBeNull()
  })
})

describe("one-time secrets", () => {
  const secretValue = "hunter2-correct-horse"

  function cachedText() {
    return JSON.stringify([
      client
        .getQueryCache()
        .getAll()
        .map((query) => query.state.data),
      client
        .getMutationCache()
        .getAll()
        .map((mutation) => [mutation.state.variables, mutation.state.data]),
    ])
  }

  it("is shared by the owner, who pastes it in themself", async () => {
    const { go } = renderCard(approvedRequest("access"), {
      routes: {
        [`PUT ${secretPath}`]: () => ({
          request_id: "irq_slack",
          expires_at: "2026-09-29T13:00:00Z",
        }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "Share a secret" })
    )
    const field = screen.getByLabelText("The secret to share with Raya")
    expect((field as HTMLInputElement).type).toBe("password")
    fireEvent.change(field, { target: { value: secretValue } })
    fireEvent.change(screen.getByLabelText("Can be opened for"), {
      target: { value: "1800" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Share with Raya" }))

    await screen.findByText(
      `Shared. Raya can open it once, until ${clockTime("2026-09-29T13:00:00Z")}.`
    )
    expect(go.calls.find((call) => call.path === secretPath)?.body).toEqual({
      value: secretValue,
      ttl_seconds: 1800,
    })
    // The field went with the form, and no cache holds what was in it.
    expect(screen.queryByDisplayValue(secretValue)).toBeNull()
    expect(cachedText()).not.toContain(secretValue)
  })

  it("is refused empty, and refused pasted across lines", async () => {
    const { go } = renderCard(approvedRequest("access"))

    fireEvent.click(
      await screen.findByRole("button", { name: "Share a secret" })
    )
    fireEvent.click(screen.getByRole("button", { name: "Share with Raya" }))
    expect((await screen.findByRole("alert")).textContent).toBe(
      "Paste the secret first."
    )

    fireEvent.paste(screen.getByLabelText("The secret to share with Raya"), {
      clipboardData: {
        getData: () => "-----BEGIN KEY-----\nabc\n-----END KEY-----",
      },
    })
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toMatch(
        /more than one line/
      )
    )
    expect(go.calls.some((call) => call.path === secretPath)).toBe(false)
  })

  it("keeps what was typed when sharing fails, so it can be tried again", async () => {
    renderCard(approvedRequest("access"), {
      routes: {
        [`PUT ${secretPath}`]: () =>
          failure(503, "secrets_unavailable", "not kept here"),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "Share a secret" })
    )
    fireEvent.change(screen.getByLabelText("The secret to share with Raya"), {
      target: { value: secretValue },
    })
    fireEvent.click(screen.getByRole("button", { name: "Share with Raya" }))

    expect((await screen.findByRole("alert")).textContent).toBe(
      "One-time secrets aren't kept on this server."
    )
    expect(
      screen.getByLabelText("The secret to share with Raya")
    ).toHaveProperty("value", secretValue)
  })

  it("is taken back by the owner", async () => {
    const { go } = renderCard(approvedRequest("access"), {
      routes: {
        [`DELETE ${secretPath}`]: () => new Response(null, { status: 204 }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "Take back a shared secret" })
    )

    await screen.findByText("Taken back. Raya can't open it.")
    expect(go.count("DELETE", secretPath)).toBe(1)
  })

  it("tells the owner when the asker opened it", async () => {
    renderCard(
      approvedRequest("access", { secret_viewed_at: "2026-09-29T12:20:00Z" })
    )

    await screen.findByText(
      `Raya opened it at ${clockTime("2026-09-29T12:20:00Z")}. It can't be opened again.`
    )
  })

  it("is offered only for a request approved with access, and only to its parties", async () => {
    const summary = renderCard(approvedRequest("summary"))
    await screen.findByRole("article")
    expect(screen.queryByRole("button", { name: "Share a secret" })).toBeNull()
    summary.unmount()

    renderCard(approvedRequest("access"), { viewer: ASKER })
    await screen.findByRole("article")
    expect(screen.queryByRole("button", { name: "Share a secret" })).toBeNull()
    expect(screen.getByRole("button", { name: "Open the secret" })).toBeTruthy()
  })

  it("is opened once by the asker, shown, and gone when they are done", async () => {
    const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) }
    Object.defineProperty(navigator, "clipboard", {
      value: clipboard,
      configurable: true,
    })
    const { go } = renderCard(approvedRequest("access"), {
      viewer: ASKER,
      routes: {
        [`GET ${secretPath}`]: () => ({
          value: secretValue,
          expires_at: "2026-09-29T13:00:00Z",
        }),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "Open the secret" })
    )
    // It says it is once, before it does anything.
    await screen.findByText(/Open it now\? It is shown once/)
    expect(go.count("GET", secretPath)).toBe(0)
    fireEvent.click(screen.getByRole("button", { name: "Open it once" }))

    const shown = await screen.findByDisplayValue(secretValue)
    expect((shown as HTMLInputElement).readOnly).toBe(true)
    expect(screen.getByText(/This was the one time/)).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Copy the secret" }))
    await waitFor(() =>
      expect(clipboard.writeText).toHaveBeenCalledWith(secretValue)
    )
    expect(cachedText()).not.toContain(secretValue)

    fireEvent.click(screen.getByRole("button", { name: "Done" }))

    await screen.findByText("You opened it. It can't be opened again.")
    expect(screen.queryByDisplayValue(secretValue)).toBeNull()
    expect(document.body.textContent).not.toContain(secretValue)
    expect(cachedText()).not.toContain(secretValue)
    expect(go.count("GET", secretPath)).toBe(1)
  })

  it("says plainly that there is nothing to open once it is gone", async () => {
    const { go } = renderCard(approvedRequest("access"), {
      viewer: ASKER,
      routes: {
        [`GET ${secretPath}`]: () =>
          failure(410, "secret_gone", "already opened"),
      },
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "Open the secret" })
    )
    fireEvent.click(await screen.findByRole("button", { name: "Open it once" }))

    await screen.findByText(
      "There is no secret to open. It was already opened, taken back or has expired, or it was never shared."
    )
    expect(go.count("GET", secretPath)).toBe(1)
  })

  it("says when it was opened, on the asker's own card", async () => {
    renderCard(
      approvedRequest("access", { secret_viewed_at: "2026-09-29T12:20:00Z" }),
      { viewer: ASKER }
    )

    await screen.findByText(
      `You opened a secret at ${clockTime("2026-09-29T12:20:00Z")}. It can't be opened again.`
    )
  })
})

describe("what was answered", () => {
  it("shows the answer the asker was given", async () => {
    renderCard(
      approvedRequest("summary", {
        status: "completed",
        revision: 3,
        result: answer("Launch is on 12 October."),
      }),
      { viewer: ASKER }
    )

    await screen.findByText("Answered.")
    expect(screen.getByText("The answer")).toBeTruthy()
    expect(screen.getByText("Launch is on 12 October.")).toBeTruthy()
  })

  it("says who made a request an agent filed, and why", async () => {
    renderCard(agentRequest())

    const card = await screen.findByRole("article", {
      name: "Raya Patel asked Krude, in Oryxa",
    })
    expect(within(card).getByText(/Oryxa/)).toBeTruthy()
    expect(within(card).getByText(/Architecture/)).toBeTruthy()
  })
})

describe("a request read by its id", () => {
  function renderById(routes: Record<string, Route>) {
    fakeGo(routes)
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    return render(
      <QueryClientProvider client={client}>
        <RequestCardById
          organizationId={ORG}
          viewerId={OWNER}
          requestId="irq_slack"
        />
      </QueryClientProvider>
    )
  }

  it("says while it loads, and then shows the card", async () => {
    renderById({ [`GET ${requestPath("irq_slack")}`]: () => slackRequest() })

    expect(screen.getByRole("status").textContent).toBe("Loading the request…")
    await screen.findByRole("article", { name: /asked you/ })
  })

  it("says plainly when it cannot be found", async () => {
    renderById({})

    expect(
      await screen.findByText(
        "This request can't be found. It may not be yours, or it may be gone."
      )
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull()
  })

  it("offers to try again when it could not be read", async () => {
    let fail = true
    renderById({
      [`GET ${requestPath("irq_slack")}`]: () =>
        fail
          ? failure(500, "internal", "internal server error")
          : slackRequest(),
    })

    await screen.findByRole("alert")
    fail = false
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))

    await screen.findByRole("article", { name: /asked you/ })
  })
})

// A card given a request is only a view of it: nothing about the request
// leaves the page except through the calls above.
describe("the card's own wording", () => {
  it("dates itself by the clock it is given", async () => {
    renderCard(slackRequest())

    const time = await screen.findByText(clockTime("2026-09-29T12:00:00Z"))
    expect(time.tagName).toBe("TIME")
    expect(time.getAttribute("datetime")).toBe("2026-09-29T12:00:00Z")
  })
})
