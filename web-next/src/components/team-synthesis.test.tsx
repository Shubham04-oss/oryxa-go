// @vitest-environment jsdom

import { cleanup, render, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  FollowUpsByTeamspace,
  TeamSynthesis,
  groupFollowUps,
} from "./team-synthesis"
import type { OrganizationFollowUp } from "@/lib/agents/organization-agent"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function followUp(
  id: string,
  teamspace: string,
  task: string
): OrganizationFollowUp {
  return {
    id,
    organization_id: "org_1",
    synthesis_revision: 2,
    source_entry_id: "e1",
    teamspace_id: teamspace,
    task,
    rationale: "Because.",
    status: "proposed",
    created_at: new Date().toISOString(),
  }
}

const followUps = [
  followUp("f1", "tsp_eu", "Book the pen test retest"),
  followUp("f2", "tsp_billing", "Retry failed renewals"),
  followUp("f3", "tsp_eu", "Ask legal for sign-off"),
]

const fetchMock = vi.fn<typeof fetch>()

function renderWith(node: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
}

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock)
})

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

describe("follow-ups by teamspace", () => {
  it("groups them by the teamspace they came from, in order", () => {
    expect(
      groupFollowUps(followUps).map((group) => [
        group.teamspaceId,
        group.followUps.map((item) => item.id),
      ])
    ).toEqual([
      ["tsp_eu", ["f1", "f3"]],
      ["tsp_billing", ["f2"]],
    ])
  })

  it("shows each teamspace's under its name", async () => {
    fetchMock.mockImplementation(async (url) =>
      String(url).includes("follow-ups")
        ? json({ follow_ups: followUps })
        : json({
            teamspaces: [
              { id: "tsp_eu", name: "EU launch" },
              { id: "tsp_billing", name: "Billing retries" },
            ],
          })
    )
    renderWith(<FollowUpsByTeamspace organizationId="org_1" />)
    const eu = await screen.findByRole("region", {
      name: "Follow-ups from EU launch",
    })
    expect(within(eu).getAllByRole("listitem")).toHaveLength(2)
    expect(
      screen.getByRole("region", { name: "Follow-ups from Billing retries" })
    ).toBeTruthy()
  })
})

describe("TeamSynthesis", () => {
  it("shows the team's summary and only its own follow-ups", async () => {
    fetchMock.mockImplementation(async (url) =>
      String(url).includes("follow-ups")
        ? json({ follow_ups: followUps })
        : json({
            organization_id: "org_1",
            teamspace_id: "tsp_eu",
            agent_id: "agt_org",
            revision: 2,
            summary: "Launch moves to 4 November.",
            decisions: ["Data stays in Frankfurt"],
            risks: [],
            open_questions: ["Who signs for legal?"],
            updated_at: new Date().toISOString(),
          })
    )
    renderWith(<TeamSynthesis organizationId="org_1" teamspaceId="tsp_eu" />)
    const panel = await screen.findByRole("region", { name: "Team summary" })
    expect(within(panel).getByText("Launch moves to 4 November.")).toBeTruthy()
    expect(within(panel).getByText("Data stays in Frankfurt")).toBeTruthy()
    expect(within(panel).queryByText("Risks")).toBeNull()
    expect(
      await within(panel).findByText("Book the pen test retest")
    ).toBeTruthy()
    expect(within(panel).queryByText("Retry failed renewals")).toBeNull()
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
      "/v1/organizations/org_1/teamspaces/tsp_eu/synthesis"
    )
  })

  it("is hidden from anyone who may not read it", async () => {
    fetchMock.mockImplementation(async () =>
      json({ error: { code: "forbidden", message: "not on this team" } }, 403)
    )
    renderWith(<TeamSynthesis organizationId="org_1" teamspaceId="tsp_eu" />)
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByRole("region", { name: "Team summary" })).toBeNull()
    expect(screen.queryByText(/Couldn't load/)).toBeNull()
  })
})
