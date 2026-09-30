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
