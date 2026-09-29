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
    const order = within(list)
      .getAllByRole("listitem")
      .map(
        (item) =>
          item.querySelector(".workspace-request-task")?.textContent ??
          item.textContent
      )
    // A heading, what waits, a heading, what came before.
    expect(order).toEqual([
      "Waiting for you",
      "What is the plan for the EU launch?",
      "Earlier",
      "Explain the service boundary.",
    ])
    expect(
      within(list).getByRole("group", { name: /Choose what Krude/ })
    ).toBeTruthy()
    // The tab says how many wait.
    expect(screen.getByRole("tab", { name: "Inbox, 1 waiting" })).toBeTruthy()
  })

  it("has no headings when nothing waits", async () => {
    renderView({ [`GET ${inboxPath}`]: listRoute(() => [earlier]) })

    await screen.findByText("Explain the service boundary.")
    expect(screen.queryByText("Waiting for you")).toBeNull()
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

    const card = await screen.findByRole("article", {
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
    await screen.findByText("What is the plan for the EU launch?")
    expect(screen.getByText("Explain the service boundary.")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Slack" }))
    // A new filter is a new list, and says so while it loads.
    await screen.findByText("What is the plan for the EU launch?")
    expect(screen.queryByText("Explain the service boundary.")).toBeNull()
    expect(screen.getByRole("button", { name: "Slack" })).toHaveProperty(
      "ariaPressed",
      "true"
    )

    fireEvent.click(screen.getByRole("button", { name: "Oryxa" }))
    await screen.findByText("Explain the service boundary.")
    expect(screen.queryByText("What is the plan for the EU launch?")).toBeNull()
    expect(origins).toEqual(expect.arrayContaining(["", "slack", "agent"]))
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
      `Approved in Slack by you at ${clockTime("2026-09-29T12:02:00Z")}. Summary only.`
    )
    expect(
      screen.queryByRole("group", { name: /Choose what Krude/ })
    ).toBeNull()
    expect(screen.queryByText("Waiting for you")).toBeNull()
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
    expect(screen.getByText("Waiting for you")).toBeTruthy()

    // Answered in Slack: Go lists it first now, among the requests that
    // no longer wait.
    served = [approvedRequest("summary"), earlier]
    focusManager.setFocused(false)
    focusManager.setFocused(true)

    await screen.findByText(/Approved in Slack by you/)
    // The same card, in the same place: not moved, and not made again.
    expect(screen.getByRole("article", { name: /asked you/ })).toBe(before)
    // Nothing waits any more, so no heading says so.
    expect(screen.queryByText("Waiting for you")).toBeNull()
    expect(screen.queryByText("Earlier")).toBeNull()
    const cards = screen
      .getAllByRole("article")
      .map((card) => card.getAttribute("aria-label"))
    expect(cards).toEqual([
      "Raya Patel asked you, in Slack",
      "Raya Patel asked Krude, in Oryxa",
    ])
  })

  it("puts a request that arrives, and one that starts waiting later, above the rest", async () => {
    const answered = approvedRequest("summary", { id: "irq_answered" })
    let served: AgentRequest[] = [answered, earlier]
    renderView({ [`GET ${inboxPath}`]: listRoute(() => served) })
    await screen.findByText("Explain the service boundary.")
    expect(screen.queryByText("Waiting for you")).toBeNull()

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

    await screen.findByText("A brand new ask.")
    const list = screen.getByRole("list", { name: "Inbox" })
    const order = within(list)
      .getAllByRole("listitem")
      .map(
        (item) =>
          item.querySelector(".workspace-request-task")?.textContent ??
          item.textContent
      )
    // Both go above what was there; the answer that came back held is the
    // later change, so it is first.
    expect(order).toEqual([
      "Waiting for you",
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
    // It is not listed a second time.
    await screen.findByText("Explain the service boundary.")
    expect(
      screen.getAllByText("What is the plan for the EU launch?")
    ).toHaveLength(1)
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

    expect(screen.queryByRole("region", { name: "Opened request" })).toBeNull()
    expect(onFocusDone).toHaveBeenCalledTimes(1)
    const list = await screen.findByRole("list", { name: "Inbox" })
    await within(list).findByText("What is the plan for the EU launch?")
    expect(within(list).getByText("Waiting for you")).toBeTruthy()
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
    // The list is not asked for again by each card, nor by each time the
    // page settles: it is asked for, failed, and left.
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(go.count("GET", membersPath)).toBe(1)
    expect(screen.getAllByRole("article")).toHaveLength(2)
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
