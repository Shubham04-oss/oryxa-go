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
import { RequestsActivity } from "./requests-activity"
import { clockTime } from "./direct-message-format"
import {
  ASKER,
  NOW,
  ORG,
  OWNER,
  activityEntry,
  activityPath,
  failure,
  fakeGo,
  lease,
} from "./request-card-fixtures"
import type { Route } from "./request-card-fixtures"
import type { AgentActivityEntry } from "@/lib/agents/agent-requests"

const leasePath = `/v1/organizations/${ORG}/agent-leases/ils_1`

let client: QueryClient

function renderActivity(
  routes: Record<string, Route>,
  {
    viewer = OWNER,
    onOpenRequest = vi.fn(),
  }: { viewer?: string; onOpenRequest?: (id: string) => void } = {}
) {
  const go = fakeGo(routes)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <RequestsActivity
        organizationId={ORG}
        viewerId={viewer}
        onOpenRequest={onOpenRequest}
      />
    </QueryClientProvider>
  )
  return { go, onOpenRequest }
}

// Newest first, as Go lists them: today's five, then a request from yesterday.
const entries: AgentActivityEntry[] = [
  activityEntry({ kind: "lease_revoked", at: "2026-09-29T12:25:00Z", lease }),
  activityEntry({ kind: "secret_viewed", at: "2026-09-29T12:20:00Z" }),
  activityEntry({
    kind: "answered",
    at: "2026-09-29T12:10:00Z",
    status: "completed",
  }),
  activityEntry({
    kind: "lease_given",
    at: "2026-09-29T12:02:00Z",
    lease: { ...lease, revoked_at: "2026-09-29T12:25:00Z" },
  }),
  activityEntry({
    kind: "decided",
    at: "2026-09-29T12:02:00Z",
    decision: "approved",
    consent: "access",
    decided_by: { kind: "member", user_id: OWNER, surface: "slack" },
  }),
  activityEntry({ kind: "asked", at: "2026-09-29T12:00:00Z" }),
  activityEntry({
    kind: "asked",
    at: "2026-09-28T09:00:00Z",
    request_id: "irq_old",
  }),
]

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

describe("Activity", () => {
  it("says what happened, newest first and by day", async () => {
    renderActivity({ [`GET ${activityPath}`]: () => ({ activity: entries }) })

    await screen.findByText("Raya opened the secret you shared.")
    const today = screen.getByRole("region", { name: "Today" })
    const rows = within(today)
      .getAllByRole("listitem")
      .map(
        (row) =>
          row.querySelector(".workspace-requests-activity-text")?.textContent
      )
    expect(rows).toEqual([
      "You ended Raya's access.",
      "Raya opened the secret you shared.",
      "Your agent answered Raya.",
      `You gave Raya access until ${clockTime(lease.expires_at)}.`,
      "You approved Raya's request in Slack. Access for 1 hour.",
      "Raya asked you in Slack.",
    ])
    const yesterday = screen.getByRole("region", { name: "Yesterday" })
    expect(within(yesterday).getByText("Raya asked you in Slack.")).toBeTruthy()
    // It lists names, states and times: a note says so, and no question is here.
    expect(screen.getByText(/Names, states and times only/)).toBeTruthy()
    expect(document.body.textContent).not.toContain("EU launch")
  })

  it("says the member replied themselves, and that it was their reply that reached the asker", async () => {
    renderActivity({
      [`GET ${activityPath}`]: () => ({
        activity: [
          activityEntry({
            kind: "answered",
            at: "2026-09-29T12:31:00Z",
            status: "completed",
          }),
          activityEntry({
            kind: "decided",
            at: "2026-09-29T12:31:00Z",
            decision: "approved",
            consent: "reply",
            decided_by: { kind: "member", user_id: OWNER, surface: "web" },
          }),
          activityEntry({ kind: "asked", at: "2026-09-29T12:00:00Z" }),
          // Another request, answered by the agent, is still the agent's.
          activityEntry({
            kind: "answered",
            at: "2026-09-29T12:20:00Z",
            status: "completed",
            request_id: "irq_other",
          }),
        ],
      }),
    })

    await screen.findByText("Raya asked you in Slack.")
    const rows = within(screen.getByRole("region", { name: "Today" }))
      .getAllByRole("listitem")
      .map(
        (row) =>
          row.querySelector(".workspace-requests-activity-text")?.textContent
      )
    expect(rows).toEqual([
      "Your reply reached Raya.",
      "You replied to Raya's request in Oryxa.",
      "Raya asked you in Slack.",
      "Your agent answered Raya.",
    ])
    expect(document.body.textContent).not.toMatch(/approved|Answer shared/i)
  })

  it("reads as the asker's own", async () => {
    renderActivity(
      {
        [`GET ${activityPath}`]: () => ({
          activity: [
            activityEntry({
              kind: "decided",
              role: "asker",
              with_user_id: OWNER,
              decision: "rejected",
              decided_by: { kind: "member", user_id: OWNER, surface: "slack" },
            }),
            activityEntry({
              kind: "asked",
              role: "asker",
              with_user_id: OWNER,
            }),
          ],
        }),
      },
      { viewer: ASKER }
    )

    await screen.findByText("Ada denied your request in Slack.")
    expect(screen.getByText("You asked Ada in Slack.")).toBeTruthy()
  })

  it("offers to open each request once, at its oldest row, and opens it", async () => {
    const { onOpenRequest } = renderActivity({
      [`GET ${activityPath}`]: () => ({ activity: entries }),
    })

    await screen.findByText("Raya opened the secret you shared.")
    const views = screen.getAllByRole("button", { name: "View request" })
    expect(views).toHaveLength(2)
    fireEvent.click(views[0])

    expect(onOpenRequest).toHaveBeenCalledWith("irq_slack")
  })

  it("ends the access that is still running, and reads Activity again", async () => {
    const running = { ...lease }
    const { go } = renderActivity({
      [`GET ${activityPath}`]: () => ({
        activity: [
          activityEntry({
            kind: "lease_given",
            at: "2026-09-29T12:02:00Z",
            lease: running,
          }),
        ],
      }),
      [`DELETE ${leasePath}`]: () => ({
        lease: { ...running, revoked_at: "2026-09-29T12:31:00Z" },
      }),
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "End Raya's access" })
    )

    await waitFor(() => expect(go.count("DELETE", leasePath)).toBe(1))
    await waitFor(() => expect(go.count("GET", activityPath)).toBe(2))
  })

  it("offers no end to access that has already ended", async () => {
    renderActivity({
      [`GET ${activityPath}`]: () => ({
        activity: [
          activityEntry({
            kind: "lease_given",
            at: "2026-09-29T12:02:00Z",
            lease: { ...lease, revoked_at: "2026-09-29T12:25:00Z" },
          }),
        ],
      }),
    })

    await screen.findByText(/You gave Raya access/)
    expect(screen.queryByRole("button", { name: /End .* access/ })).toBeNull()
  })

  it("offers no end to access somebody else gave", async () => {
    renderActivity(
      {
        [`GET ${activityPath}`]: () => ({
          activity: [
            activityEntry({
              kind: "lease_given",
              role: "asker",
              with_user_id: OWNER,
              at: "2026-09-29T12:01:00Z",
              lease,
            }),
          ],
        }),
      },
      { viewer: ASKER }
    )

    await screen.findByText(/Ada gave you access/)
    expect(screen.queryByRole("button", { name: /End .* access/ })).toBeNull()
  })

  it("says when the access had already ended", async () => {
    renderActivity({
      [`GET ${activityPath}`]: () => ({
        activity: [
          activityEntry({
            kind: "lease_given",
            at: "2026-09-29T12:02:00Z",
            lease,
          }),
        ],
      }),
      [`DELETE ${leasePath}`]: () => failure(404, "not_found", "not found"),
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "End Raya's access" })
    )

    expect((await screen.findByRole("alert")).textContent).toBe(
      "That access has already ended."
    )
  })

  it("says who reached the member's things, with the rule that let them", async () => {
    const at = "2026-09-29T12:15:00Z"
    const access = {
      at,
      service: "rag",
      resource_id: "cor_sales",
      actor_user_id: OWNER,
    }
    const { go } = renderActivity({
      [`GET ${activityPath}`]: () => ({
        activity: [
          activityEntry({
            kind: "accessed",
            at,
            request_id: undefined,
            access: {
              ...access,
              action: "read",
              resource_kind: "file",
              resource_id: "fil_1",
              owner_id: OWNER,
              actor_user_id: ASKER,
              actor_agent_id: "agt_raya",
              actor_agent_role: "primary",
              reason: "team_member",
            },
          }),
          activityEntry({
            kind: "accessed",
            at,
            role: "asker",
            with_user_id: undefined,
            request_id: undefined,
            access: {
              ...access,
              action: "search",
              resource_kind: "corpus",
              actor_agent_id: "agt_researcher",
              actor_agent_role: "specialist",
              reason: "grant",
            },
          }),
          activityEntry({
            kind: "accessed",
            at,
            request_id: undefined,
            access: {
              ...access,
              action: "read",
              resource_kind: "source",
              resource_id: "src_1",
              owner_id: OWNER,
              actor_user_id: ASKER,
              reason: "org_admin",
            },
          }),
        ],
      }),
      [`GET /v1/organizations/${ORG}/agents`]: () => ({
        agents: [{ id: "agt_researcher", name: "Researcher" }],
      }),
      [`GET /v1/organizations/${ORG}/corpora`]: () => ({
        corpora: [{ id: "cor_sales", name: "Sales playbooks" }],
      }),
    })

    await screen.findByText(
      "Your specialist Researcher searched Sales playbooks."
    )
    const rows = within(screen.getByRole("region", { name: "Today" }))
      .getAllByRole("listitem")
      .map(
        (row) =>
          row.querySelector(".workspace-requests-activity-text")?.textContent
      )
    expect(rows).toEqual([
      "Raya's agent read your file. · on the same team",
      "Your specialist Researcher searched Sales playbooks. · allowed by its grant",
      "An admin, Raya, opened your source. · org admin",
    ])
    // Access entries point to no request.
    expect(screen.queryByRole("button", { name: "View request" })).toBeNull()
    expect(document.body.textContent).not.toMatch(/agt_|cor_|fil_|src_/)
    expect(go.count("GET", `/v1/organizations/${ORG}/corpora`)).toBe(1)
  })

  it("says the kind when the names can't be read", async () => {
    renderActivity({
      [`GET ${activityPath}`]: () => ({
        activity: [
          activityEntry({
            kind: "accessed",
            role: "asker",
            with_user_id: undefined,
            request_id: undefined,
            access: {
              at: "2026-09-29T12:15:00Z",
              service: "rag",
              action: "search",
              resource_kind: "corpus",
              resource_id: "cor_sales",
              actor_user_id: OWNER,
              actor_agent_id: "agt_researcher",
              actor_agent_role: "specialist",
              reason: "organization",
            },
          }),
        ],
      }),
      // The agents and corpora routes are missing: a 404 each.
    })

    await screen.findByText("Your specialist searched a collection.")
    expect(
      screen.getByText("· shared with everyone", { exact: false })
    ).toBeTruthy()
  })

  it("asks for no names when nothing was accessed", async () => {
    const { go } = renderActivity({
      [`GET ${activityPath}`]: () => ({ activity: entries }),
    })

    await screen.findByText("Raya opened the secret you shared.")
    expect(go.sent()).toEqual([`GET ${activityPath}`])
  })

  it("says while it loads, and when there is nothing yet", async () => {
    renderActivity({ [`GET ${activityPath}`]: () => ({ activity: [] }) })

    expect(screen.getByRole("status").textContent).toBe("Loading activity…")
    await screen.findByText("No Slack activity yet.")
  })

  it("says why it could not be read, and tries again", async () => {
    let broken = true
    renderActivity({
      [`GET ${activityPath}`]: () =>
        broken
          ? failure(500, "internal", "internal server error")
          : { activity: entries },
    })

    expect((await screen.findByRole("alert")).textContent).toContain(
      "Could not load activity."
    )
    broken = false
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))

    await screen.findByText("Raya opened the secret you shared.")
  })

  it("says plainly where the server keeps no activity", async () => {
    renderActivity({
      [`GET ${activityPath}`]: () =>
        failure(503, "interactions_unavailable", "not configured"),
    })

    await screen.findByText("Activity isn't set up on this server.")
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull()
  })
})
