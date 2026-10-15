// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { HomeLivePill, parleyDuration } from "./home-live-pill"

const ORG = "org_live"
const NOW = Date.parse("2026-10-15T10:00:00Z")
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
const space = (id: string, name: string, extra: object = {}) => ({
  id,
  organization_id: ORG,
  team_id: "team",
  team_name: "Team",
  name,
  created_by_user_id: "u",
  organization_agent: {},
  whisperers: [],
  status: "active",
  revision: 1,
  contributions: [],
  can_manage: false,
  is_member: true,
  created_at: new Date(NOW).toISOString(),
  updated_at: new Date(NOW).toISOString(),
  ...extra,
})
const call = {
  callId: "call",
  startedAt: new Date(NOW - 18 * 60_000).toISOString(),
  startedBy: "raya",
  participants: [
    { member: "raya", name: "Raya Sen", joinedAt: "" },
    { member: "theo", name: "Theo", joinedAt: "" },
  ],
  recording: { on: false, by: null, since: null },
  agent: { present: false, name: "Krude" },
}

function stub(calls: Record<string, unknown>, spaces: unknown[]) {
  const asked: string[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost")
      asked.push(url.pathname)
      if (url.pathname === `/v1/organizations/${ORG}/teamspaces`)
        return json({ teamspaces: spaces })
      if (url.pathname.endsWith("/admit"))
        return new Response(null, { status: 204 })
      const match = /\/rooms\/teamspace\/([^/]+)\/call$/.exec(url.pathname)
      if (match) return json({ call: calls[match[1]] ?? null })
      return json({ error: { code: "not_found", message: "No route" } }, 404)
    })
  )
  return asked
}

function renderPill(onOpenTeamspace = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <HomeLivePill
        organizationId={ORG}
        enabled
        onOpenTeamspace={onOpenTeamspace}
      />
    </QueryClientProvider>
  )
  return onOpenTeamspace
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(NOW)
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

it("shows a live parley in one of the member's teamspaces, and Join opens it", async () => {
  stub({ eu: call }, [space("quiet", "Quiet room"), space("eu", "EU launch")])
  const open = renderPill()
  const pill = await screen.findByRole("status", { name: "Live parley" })
  expect(pill.textContent).toContain("Parley in EU launch · 18 min")
  fireEvent.click(
    screen.getByRole("button", { name: "Join the parley in EU launch" })
  )
  expect(open).toHaveBeenCalledWith("eu")
})

it("shows nothing when no parley is live, and never asks paused or foreign teamspaces", async () => {
  const asked = stub({}, [
    space("quiet", "Quiet room"),
    space("paused", "Paused", { status: "disabled" }),
    space("managed", "Managed only", { is_member: false, can_manage: true }),
  ])
  renderPill()
  await vi.waitFor(() =>
    expect(asked.some((path) => path.endsWith("/quiet/call"))).toBe(true)
  )
  expect(screen.queryByRole("status", { name: "Live parley" })).toBeNull()
  expect(asked.some((path) => path.includes("/paused/"))).toBe(false)
  expect(asked.some((path) => path.includes("/managed/"))).toBe(false)
})

it("says how long a parley has run", () => {
  expect(parleyDuration(new Date(NOW - 20_000).toISOString(), NOW)).toBe(
    "just started"
  )
  expect(parleyDuration(new Date(NOW - 18 * 60_000).toISOString(), NOW)).toBe(
    "18 min"
  )
  expect(parleyDuration(new Date(NOW - 65 * 60_000).toISOString(), NOW)).toBe(
    "1 h 05"
  )
})
