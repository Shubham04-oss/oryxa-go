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
import { RequestsCanvas, RequestsView } from "./requests-view"
import { clockTime } from "./direct-message-format"
import {
  ASKER,
  NOW,
  ORG,
  OWNER,
  activityEntry,
  activityPath,
  agentRequest,
  approvedRequest,
  failure,
  fakeGo,
  inboxPath,
  lease,
  requestPath,
  sentPath,
  slackRequest,
} from "./request-card-fixtures"
import type { Route } from "./request-card-fixtures"
import { requestListQueryOptions } from "./requests-view-queries"
import { AGENT_REQUESTS_POLL_MS } from "@/lib/agents/queries"
import type { AgentRequest } from "@/lib/agents/agent-requests"

let client: QueryClient

/** A request the member sent to Raya in Slack. */
const sentRequest: AgentRequest = slackRequest({
  id: "irq_sent",
  requester: { user_id: OWNER, agent_id: "agt_krude", agent_name: "Krude" },
  target: {
    user_id: ASKER,
    agent_id: "agt_nia",
    agent_name: "Nia",
    user_email: "raya@example.com",
    user_display_name: "Raya Patel",
  },
  task: "Who owns the billing service?",
})

/** An earlier request an agent filed, answered already. */
const earlier: AgentRequest = agentRequest({
  id: "irq_earlier",
  status: "completed",
  revision: 4,
  task: "Explain the service boundary.",
  created_at: "2026-09-28T10:00:00Z",
})

function renderView(
  routes: Record<string, Route>,
  props: { focusRequestId?: string | null; onFocusDone?: () => void } = {}
) {
  const go = fakeGo(routes)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <RequestsView organizationId={ORG} viewerId={OWNER} {...props} />
    </QueryClientProvider>
  )
  return go
}

/** A list route that answers what is asked for, narrowed by origin as Go does. */
function listRoute(
  served: () => AgentRequest[],
  origins: string[] = []
): Route {
  return (_call, url) => {
    const origin = url.searchParams.get("origin")
    origins.push(origin ?? "")
    return {
      requests: served().filter(
        (request) =>
          !origin || (request.origin === "slack" ? "slack" : "agent") === origin
      ),
    }
  }
}

/** Each item of a list: a row's request, or a heading. */
/** The inbox's first heading, with how many wait ("Waiting for you 1"). */
function waitingHeading(count: number, scope: HTMLElement) {
  return within(scope).getByText(
    (_, element) =>
      element?.tagName === "P" &&
      element.textContent === `Waiting for you ${count}`
  )
}

function listed(list: HTMLElement): string[] {
  return within(list)
    .getAllByRole("listitem")
    .map(
      (item) =>
        item.querySelector(".requests-row-what")?.textContent ??
        item.textContent
    )
}

/** The request open beside the lists: named, or the one at hand. */
function openedRegion(): HTMLElement {
  return screen.getByRole("region", { name: /^(Opened|Current) request$/ })
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(NOW)
})

afterEach(() => {
  cleanup()
  client.clear()
  focusManager.setFocused(undefined)
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("Inbox", () => {
  it("lists what waits on the member first, then what came before", async () => {
    renderView({
      [`GET ${inboxPath}`]: listRoute(() => [earlier, slackRequest()]),
    })

    const list = await screen.findByRole("list", { name: "Inbox" })
    await within(list).findByText("What is the plan for the EU launch?")
    // A heading, what waits, a heading, what came before.
    expect(listed(list)).toEqual([
      "Waiting for you 1",
      "What is the plan for the EU launch?",
      "Earlier",
      "Explain the service boundary.",
    ])
    // What waits opens beside the list, ready to decide.
    expect(
      await within(openedRegion()).findByRole("group", {
        name: /Choose what Krude/,
      })
    ).toBeTruthy()
    expect(
      within(list)
        .getByRole("button", { name: /What is the plan/ })
        .getAttribute("aria-current")
    ).toBe("true")
    // The tab says how many wait.
    expect(screen.getByRole("tab", { name: "Inbox, 1 waiting" })).toBeTruthy()
  })

  it("says every ask is answered beside the list when nothing waits, and opens none", async () => {
    renderView({ [`GET ${inboxPath}`]: listRoute(() => [earlier]) })

    const clear = await screen.findByRole("region", { name: "All clear" })
    expect(within(clear).getByText("Every ask answered.")).toBeTruthy()
    expect(
      screen.queryByRole("region", { name: /^(Opened|Current) request$/ })
    ).toBeNull()

    // Opening a row puts its request where the words were.
    fireEvent.click(
      screen.getByRole("button", { name: /Explain the service boundary/ })
    )
    await within(openedRegion()).findByRole("article")
    expect(screen.queryByRole("region", { name: "All clear" })).toBeNull()
  })

  it("says none wait under its heading when nothing waits", async () => {
    renderView({ [`GET ${inboxPath}`]: listRoute(() => [earlier]) })

    await screen.findByText("Explain the service boundary.")
    expect(listed(screen.getByRole("list", { name: "Inbox" }))).toEqual([
      "Waiting for you 0",
      "Earlier",
      "Explain the service boundary.",
    ])
    expect(screen.getByRole("tab", { name: "Inbox" })).toBeTruthy()
  })

  it("says while it loads, and when there is nothing", async () => {
    renderView({ [`GET ${inboxPath}`]: listRoute(() => []) })

    expect(screen.getByRole("status").textContent).toBe("Loading requests…")
    await screen.findByText("Nothing is waiting for you.")
    expect(
      screen.getByText(
        /When someone asks your agent a question, in Slack or in Oryxa/
      )
    ).toBeTruthy()
  })

  it("says why it could not be read, and reads again on Try again", async () => {
    let broken = true
    renderView({
      [`GET ${inboxPath}`]: (call, url) =>
        broken
          ? failure(500, "internal", "internal server error")
          : listRoute(() => [slackRequest()])(call, url),
    })

    expect((await screen.findByRole("alert")).textContent).toContain(
      "Could not load requests."
    )
    broken = false
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))

    await screen.findByText("What is the plan for the EU launch?")
  })

  it("says plainly where the server runs no requests", async () => {
    renderView({
      [`GET ${inboxPath}`]: () =>
        failure(
          503,
          "interactions_unavailable",
          "agent interactions are not configured"
        ),
    })

    await screen.findByText("Requests aren't set up on this server.")
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull()
  })
})

describe("Sent", () => {
  it("lists what the member asked, with nothing for them to decide", async () => {
    renderView({
      [`GET ${inboxPath}`]: listRoute(() => []),
      [`GET ${sentPath}`]: listRoute(() => [sentRequest]),
    })

    fireEvent.click(await screen.findByRole("tab", { name: "Sent" }))
    // Nothing of the member's waits on them, so nothing opens by itself.
    const row = await screen.findByRole("button", {
      name: /Raya Patel.*Who owns the billing service\?/,
    })
    expect(
      screen.queryByRole("region", { name: /^(Opened|Current) request$/ })
    ).toBeNull()
    fireEvent.click(row)

    const card = await within(openedRegion()).findByRole("article", {
      name: "You asked Raya Patel, in Slack",
    })
    expect(within(card).getByText("Who owns the billing service?")).toBeTruthy()
    expect(within(card).getByText("Waiting for Raya to decide.")).toBeTruthy()
    expect(within(card).queryByRole("group")).toBeNull()
  })

  it("says when the member has asked nobody", async () => {
    renderView({
      [`GET ${inboxPath}`]: listRoute(() => []),
      [`GET ${sentPath}`]: listRoute(() => []),
    })

    fireEvent.click(await screen.findByRole("tab", { name: "Sent" }))

    await screen.findByText("You haven't asked anyone yet.")
  })
})

describe("the origin filter", () => {
  it("narrows a list to Slack, or to agents in Oryxa, by asking Go", async () => {
    const origins: string[] = []
    renderView({
      [`GET ${inboxPath}`]: listRoute(
        () => [slackRequest(), agentRequest({ id: "irq_agent" })],
        origins
      ),
    })
    await screen.findAllByText("What is the plan for the EU launch?")
    expect(screen.getAllByText("Explain the service boundary.")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Slack" }))
    // A new filter is a new list, and says so while it loads.
    await screen.findAllByText("What is the plan for the EU launch?")
    expect(screen.queryByText("Explain the service boundary.")).toBeNull()
    expect(screen.getByRole("button", { name: "Slack" })).toHaveProperty(
      "ariaPressed",
      "true"
    )

    fireEvent.click(screen.getByRole("button", { name: "Oryxa" }))
    await screen.findAllByText("Explain the service boundary.")
    expect(screen.queryByText("What is the plan for the EU launch?")).toBeNull()
    expect(origins).toEqual(expect.arrayContaining(["", "slack", "agent"]))
  })

  it("narrows an older server's mixed list and keeps legacy requests under Oryxa", async () => {
    renderView({
      // Older Go returns the whole list even when origin is requested.
      [`GET ${inboxPath}`]: () => ({
        requests: [slackRequest(), agentRequest({ origin: undefined })],
      }),
    })
    await screen.findAllByText("What is the plan for the EU launch?")
    expect(screen.getAllByText("Explain the service boundary.")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Slack" }))
    await screen.findAllByText("What is the plan for the EU launch?")
    expect(screen.queryByText("Explain the service boundary.")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Oryxa" }))
    await screen.findAllByText("Explain the service boundary.")
    expect(screen.queryByText("What is the plan for the EU launch?")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "All" }))
    await screen.findAllByText("What is the plan for the EU launch?")
    expect(screen.getAllByText("Explain the service boundary.")).toBeTruthy()
  })

  it("says what an empty filtered list is not", async () => {
    renderView({
      [`GET ${inboxPath}`]: listRoute(() => [agentRequest()]),
    })
    await screen.findByText("Explain the service boundary.")

    fireEvent.click(screen.getByRole("button", { name: "Slack" }))

    await screen.findByText("No requests from Slack.")
  })

  it("is not offered on Activity, which is Slack's alone", async () => {
    renderView({
      [`GET ${inboxPath}`]: listRoute(() => []),
      [`GET ${activityPath}`]: () => ({ activity: [] }),
    })

    fireEvent.click(await screen.findByRole("tab", { name: "Activity" }))

    await screen.findByText("No Slack activity yet.")
    expect(
      screen.queryByRole("group", { name: "Where requests were made" })
    ).toBeNull()
  })
})

describe("a request decided elsewhere", () => {
  it("settles when the member comes back to the tab", async () => {
    let served: AgentRequest[] = [slackRequest()]
    renderView({ [`GET ${inboxPath}`]: listRoute(() => served) })
    await screen.findByRole("group", { name: /Choose what Krude/ })

    // Approved in Slack; then the tab is in view again.
    served = [approvedRequest("summary")]
    focusManager.setFocused(false)
    focusManager.setFocused(true)

    await screen.findByText(
      `Approved in Slack by you at ${clockTime("2026-09-29T12:02:00Z")}. Answer shared.`
    )
    expect(
      screen.queryByRole("group", { name: /Choose what Krude/ })
    ).toBeNull()
    expect(waitingHeading(0, document.body)).toBeTruthy()
    expect(screen.getByRole("tab", { name: "Inbox" })).toBeTruthy()
  })

  it("re-reads on focus and on a timer, and never takes the lists for fresh", () => {
    const options = requestListQueryOptions(ORG, "inbox", "slack")

    expect(options.refetchOnWindowFocus).toBe("always")
    expect(options.refetchInterval).toBe(AGENT_REQUESTS_POLL_MS)
    expect(options.staleTime).toBe(0)
    expect(options.refetchIntervalInBackground).toBeUndefined()
  })
})

describe("a list that keeps each request where it was", () => {
  it("leaves the card the member just answered where it is, and says nothing waits once nothing does", async () => {
    let served: AgentRequest[] = [slackRequest(), earlier]
    renderView({ [`GET ${inboxPath}`]: listRoute(() => served) })
    const before = await screen.findByRole("article", { name: /asked you/ })
    expect(waitingHeading(1, document.body)).toBeTruthy()

    // Answered in Slack: Go lists it first now, among the requests that
    // no longer wait.
    served = [approvedRequest("summary"), earlier]
    focusManager.setFocused(false)
    focusManager.setFocused(true)

    await screen.findByText(/Approved in Slack by you/)
    // The same card stays open, not made again, and its row keeps its place.
    expect(screen.getByRole("article", { name: /asked you/ })).toBe(before)
    // Nothing waits any more, and the heading says so.
    expect(listed(screen.getByRole("list", { name: "Inbox" }))).toEqual([
      "Waiting for you 0",
      "What is the plan for the EU launch?",
      "Earlier",
      "Explain the service boundary.",
    ])
  })

  it("puts a request that arrives, and one that starts waiting later, above the rest", async () => {
    const answered = approvedRequest("summary", { id: "irq_answered" })
    let served: AgentRequest[] = [answered, earlier]
    renderView({ [`GET ${inboxPath}`]: listRoute(() => served) })
    await screen.findAllByText("Explain the service boundary.")
    expect(waitingHeading(0, document.body)).toBeTruthy()

    // Its answer was held for review, and a new ask came in besides.
    served = [
      slackRequest({ id: "irq_new", task: "A brand new ask." }),
      {
        ...answered,
        status: "review",
        revision: 3,
        guard: { state: "held", kinds: ["EMAIL"] },
        held: {
          result: {
            status: "completed",
            summary: "Held text.",
            content: "",
            tools_used: [],
            artifacts: [],
            completed_at: "2026-09-29T12:10:00Z",
          },
        },
      },
      earlier,
    ]
    focusManager.setFocused(false)
    focusManager.setFocused(true)

    const list = screen.getByRole("list", { name: "Inbox" })
    await within(list).findByText("A brand new ask.")
    // Both go above what was there; the answer that came back held is the
    // later change, so it is first.
    expect(listed(list)).toEqual([
      "Waiting for you 2",
      "What is the plan for the EU launch?",
      "A brand new ask.",
      "Earlier",
      "Explain the service boundary.",
    ])
  })
})

describe("the tabs", () => {
  it("move with the arrow keys, and Activity is one of them", async () => {
    renderView({
      [`GET ${inboxPath}`]: listRoute(() => []),
      [`GET ${sentPath}`]: listRoute(() => []),
      [`GET ${activityPath}`]: () => ({ activity: [] }),
    })
    const inbox = await screen.findByRole("tab", { name: "Inbox" })
    inbox.focus()

    fireEvent.keyDown(inbox, { key: "ArrowRight" })
    expect(screen.getByRole("tab", { name: "Sent" })).toHaveProperty(
      "ariaSelected",
      "true"
    )
    fireEvent.keyDown(screen.getByRole("tab", { name: "Sent" }), {
      key: "ArrowRight",
    })
    await screen.findByText("No Slack activity yet.")
    expect(document.activeElement).toBe(
      screen.getByRole("tab", { name: "Activity" })
    )
    fireEvent.keyDown(screen.getByRole("tab", { name: "Activity" }), {
      key: "ArrowRight",
    })
    expect(screen.getByRole("tab", { name: "Inbox" })).toHaveProperty(
      "ariaSelected",
      "true"
    )
    fireEvent.keyDown(screen.getByRole("tab", { name: "Inbox" }), {
      key: "End",
    })
    expect(screen.getByRole("tab", { name: "Activity" })).toHaveProperty(
      "ariaSelected",
      "true"
    )
    expect(screen.getByRole("tabpanel").getAttribute("aria-labelledby")).toBe(
      "requests-tab-activity"
    )
  })
})

describe("a request a link or a note named", () => {
  const inboxWith = () => listRoute(() => [slackRequest(), earlier])

  it("opens first, above the lists, and once", async () => {
    renderView(
      {
        [`GET ${inboxPath}`]: inboxWith(),
        [`GET ${requestPath("irq_slack")}`]: () => slackRequest(),
      },
      { focusRequestId: "irq_slack" }
    )

    const opened = await screen.findByRole("region", { name: "Opened request" })
    const card = await within(opened).findByRole("article", {
      name: /asked you/,
    })
    await waitFor(() => expect(document.activeElement).toBe(card))
    // Its row stays in the list, marked as the one open.
    const list = await screen.findByRole("list", { name: "Inbox" })
    await within(list).findByText("Explain the service boundary.")
    expect(
      within(list)
        .getByRole("button", { name: /What is the plan/ })
        .getAttribute("aria-current")
    ).toBe("true")
  })

  it("goes back to the lists, and says so to whoever opened it", async () => {
    const onFocusDone = vi.fn()
    renderView(
      {
        [`GET ${inboxPath}`]: inboxWith(),
        [`GET ${requestPath("irq_slack")}`]: () => slackRequest(),
      },
      { focusRequestId: "irq_slack", onFocusDone }
    )
    await screen.findByRole("region", { name: "Opened request" })

    fireEvent.click(
      screen.getByRole("button", { name: "Back to all requests" })
    )

    // No longer named: the way back goes, and whoever opened it is told.
    expect(
      screen.queryByRole("button", { name: "Back to all requests" })
    ).toBeNull()
    expect(onFocusDone).toHaveBeenCalledTimes(1)
    const list = await screen.findByRole("list", { name: "Inbox" })
    await within(list).findByText("What is the plan for the EU launch?")
    expect(waitingHeading(1, list)).toBeTruthy()
  })

  it("says plainly when it cannot be found", async () => {
    renderView(
      { [`GET ${inboxPath}`]: inboxWith() },
      { focusRequestId: "irq_gone" }
    )

    await screen.findByText(
      "This request can't be found. It may not be yours, or it may be gone."
    )
  })

  it("opens from a row of Activity", async () => {
    renderView({
      [`GET ${inboxPath}`]: listRoute(() => []),
      [`GET ${activityPath}`]: () => ({
        activity: [activityEntry({ kind: "asked" })],
      }),
      [`GET ${requestPath("irq_slack")}`]: () => slackRequest(),
    })

    fireEvent.click(await screen.findByRole("tab", { name: "Activity" }))
    fireEvent.click(await screen.findByRole("button", { name: "View request" }))

    const opened = await screen.findByRole("region", { name: "Opened request" })
    await within(opened).findByRole("article", { name: /asked you/ })
  })
})

describe("access the member gave", () => {
  it("shows a card the access it holds, as Activity lists it", async () => {
    const chosen = approvedRequest("access")
    const go = renderView({
      [`GET ${inboxPath}`]: listRoute(() => [chosen]),
      [`GET ${activityPath}`]: () => ({
        activity: [
          activityEntry({
            kind: "lease_given",
            at: "2026-09-29T12:02:00Z",
            lease,
          }),
        ],
      }),
    })

    // Nothing waits, so the member opens it.
    fireEvent.click(
      await screen.findByRole("button", { name: /What is the plan/ })
    )
    await screen.findByText(
      `Raya's follow-ups in that Slack thread are answered without asking you until ${clockTime(lease.expires_at)}.`
    )
    expect(
      screen.getByRole("button", { name: "End Raya's access" })
    ).toBeTruthy()
    expect(go.count("GET", activityPath)).toBe(1)
  })

  it("does not read Activity for requests that cannot hold access", async () => {
    const go = renderView({
      [`GET ${inboxPath}`]: listRoute(() => [
        approvedRequest("summary"),
        earlier,
      ]),
      [`GET ${activityPath}`]: () => ({ activity: [] }),
    })

    await screen.findByText("Explain the service boundary.")
    expect(go.count("GET", activityPath)).toBe(0)
  })
})

describe("when the members list cannot be read", () => {
  it("still shows the requests, with the names they hold, and asks for the list once", async () => {
    const membersPath = `/v1/organizations/${ORG}/members`
    const go = renderView({
      [`GET ${membersPath}`]: () => failure(500, "internal"),
      [`GET ${inboxPath}`]: listRoute(() => [
        slackRequest(),
        agentRequest({ id: "irq_agent" }),
      ]),
    })

    await screen.findByRole("article", { name: "Someone asked you, in Slack" })
    // Every request has its row, named by what it holds.
    // The list is not asked for again by each card, nor by each time the
    // page settles: it is asked for, failed, and left.
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(go.count("GET", membersPath)).toBe(1)
    const list = screen.getByRole("list", { name: "Inbox" })
    expect(within(list).getAllByRole("button")).toHaveLength(2)
  })
})

describe("the page as the shell shows it", () => {
  it("says a preview has no requests, and reads nothing", () => {
    const go = fakeGo({})
    client = new QueryClient()
    render(
      <QueryClientProvider client={client}>
        <RequestsCanvas />
      </QueryClientProvider>
    )

    expect(
      screen.getByText("Your requests appear here once you're signed in.")
    ).toBeTruthy()
    expect(go.calls).toHaveLength(0)
  })

  it("shows the member's requests once there is a member", async () => {
    fakeGo({ [`GET ${inboxPath}`]: listRoute(() => [slackRequest()]) })
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={client}>
        <RequestsCanvas organizationId={ORG} viewerId={OWNER} />
      </QueryClientProvider>
    )

    await screen.findByText("What is the plan for the EU launch?")
  })
})

describe("the board's Requests", () => {
  it("tags each row with what it waits on, or who answered it", async () => {
    renderView({
      [`GET ${inboxPath}`]: listRoute(() => [
        slackRequest({ expires_at: new Date(NOW + 3600_000).toISOString() }),
        slackRequest({
          id: "irq_reply",
          answer_by: "owner",
          task: "Can we move the launch?",
        }),
        earlier,
      ]),
    })

    const list = await screen.findByRole("list", { name: "Inbox" })
    await within(list).findByText("What is the plan for the EU launch?")
    const tags = Array.from(list.querySelectorAll(".requests-row-tag")).map(
      (tag) => tag.textContent
    )
    expect(tags).toEqual([
      "Expires in 1 h",
      "Wants your reply",
      "Answered by Krude",
    ])
    // The time is the clock, today.
    expect(
      within(list).getAllByText(clockTime("2026-09-29T12:00:00Z"))
    ).toHaveLength(2)
  })

  it("searches the list it shows, and says when nothing matches", async () => {
    renderView({
      [`GET ${inboxPath}`]: listRoute(() => [slackRequest(), earlier]),
    })
    const list = await screen.findByRole("list", { name: "Inbox" })
    await within(list).findByText("What is the plan for the EU launch?")

    fireEvent.click(screen.getByRole("button", { name: "Search requests" }))
    const field = screen.getByRole("searchbox", { name: "Search requests" })
    expect(document.activeElement).toBe(field)
    fireEvent.change(field, { target: { value: "boundary" } })
    expect(
      listed(screen.getByRole("list", { name: "Inbox" })).filter(
        (item) => !item.startsWith("Waiting") && item !== "Earlier"
      )
    ).toEqual(["Explain the service boundary."])
    fireEvent.change(field, { target: { value: "pricing" } })
    expect(screen.getByText("Nothing matches “pricing”.")).toBeTruthy()

    // Escape closes it, and the list is whole again.
    fireEvent.keyDown(field, { key: "Escape" })
    expect(screen.queryByRole("searchbox")).toBeNull()
    expect(
      await screen.findByText("What is the plan for the EU launch?", {
        selector: ".requests-row-what",
      })
    ).toBeTruthy()
  })

  it("says what today held when every ask is answered", async () => {
    renderView({
      [`GET ${inboxPath}`]: listRoute(() => [
        approvedRequest("summary", {
          status: "completed",
          created_at: new Date(NOW - 10 * 60_000).toISOString(),
          decision: {
            decision: "approved",
            message: "",
            allowed_tools: [],
            granted_permissions: [],
            decided_at: new Date(NOW - 6 * 60_000).toISOString(),
            decided_by: { kind: "member", user_id: OWNER, surface: "web" },
            consent: "summary",
          },
        }),
      ]),
    })

    const clear = await screen.findByRole("region", { name: "All clear" })
    expect(
      within(clear).getByText(
        "Nothing is waiting for you. The next one opens here."
      )
    ).toBeTruthy()
    const today = within(clear).getByLabelText("Today")
    expect(
      Array.from(today.querySelectorAll("div")).map((stat) => stat.textContent)
    ).toEqual(["asks today1", "by your agent1", "by you0", "average wait4 min"])
  })

  it("picks the open request's choices with S, A, R and D, and never while typing", async () => {
    const go = renderView({
      [`GET ${inboxPath}`]: listRoute(() => [slackRequest()]),
      [`POST ${requestPath("irq_slack")}/consent`]: () => ({
        request: approvedRequest("access"),
      }),
    })

    const group = await screen.findByRole("group", {
      name: /Choose what Krude/,
    })
    expect(
      within(group)
        .getAllByRole("button")
        .map((button) => button.getAttribute("aria-keyshortcuts"))
    ).toEqual(["S", "A", "R", "D"])

    // R opens the field; typing an "a" there is a letter, not a choice.
    fireEvent.keyDown(document.body, { key: "r" })
    const field = await screen.findByRole("textbox", {
      name: "Reply yourself to Raya's request",
    })
    fireEvent.keyDown(field, { key: "a" })
    expect(go.count("POST", `${requestPath("irq_slack")}/consent`)).toBe(0)

    // A from the page allows for an hour.
    fireEvent.keyDown(document.body, { key: "a" })
    await waitFor(() =>
      expect(go.count("POST", `${requestPath("irq_slack")}/consent`)).toBe(1)
    )
    expect(
      go.calls.find((call) => call.path.endsWith("/consent"))?.body
    ).toEqual({ choice: "access" })
  })
})
