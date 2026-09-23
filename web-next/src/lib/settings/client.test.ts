import { afterEach, describe, expect, it, vi } from "vitest"
import {
  addTeamMember,
  changeTeamMemberRole,
  createTeam,
  getBillingSummary,
  listOrganizationConnections,
  listTeamMembers,
  listTeams,
  openBillingPortal,
  removeTeamMember,
  setTeamEmailNotices,
  startCheckout,
} from "./client"

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
  })
}

afterEach(() => vi.unstubAllGlobals())

describe("settings service contracts", () => {
  it("reads billing and starts checkout and portal with the selected organisation", async () => {
    const fetchMock = vi.fn(async (url: string) =>
      json(
        url.endsWith("/checkout")
          ? { checkout_url: "https://pay.example.test/checkout" }
          : url.endsWith("/portal")
            ? { portal_url: "https://pay.example.test/portal" }
            : { organization_id: "org-1" }
      )
    )
    vi.stubGlobal("fetch", fetchMock)
    expect((await getBillingSummary("org-1")).organization_id).toBe("org-1")
    expect(await startCheckout("org-1", "seats", 3)).toContain("checkout")
    expect(await openBillingPortal("org-1")).toContain("portal")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/billing/organizations/org-1/checkout",
      expect.objectContaining({
        method: "POST",
        body: '{"plan":"seats","quantity":3}',
      })
    )
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/billing/organizations/org-1/portal",
      expect.objectContaining({ method: "POST" })
    )
  })

  it("maps team and membership operations to the existing service", async () => {
    const fetchMock = vi.fn(async (url: string) =>
      json(
        url.endsWith("/members")
          ? { members: [] }
          : url.endsWith("/teams")
            ? { teams: [] }
            : { id: "team-1" }
      )
    )
    vi.stubGlobal("fetch", fetchMock)
    await listTeams("org-1")
    await createTeam("org-1", "Design")
    await listTeamMembers("org-1", "team-1")
    await addTeamMember("org-1", "team-1", "user-1", "member")
    await changeTeamMemberRole("org-1", "team-1", "user-1", "admin")
    await removeTeamMember("org-1", "team-1", "user-1")
    await setTeamEmailNotices("org-1", "team-1", false)
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org-1/teams/team-1/notification",
      expect.objectContaining({
        method: "PATCH",
        body: '{"notify_email":false}',
      })
    )
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org-1/teams/team-1/members/user-1",
      expect.objectContaining({ method: "DELETE" })
    )
  })

  it("reads organisation-scoped tool access", async () => {
    const fetchMock = vi.fn(async () => json({ connections: [] }))
    vi.stubGlobal("fetch", fetchMock)
    expect(await listOrganizationConnections("org/one")).toEqual([])
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org%2Fone/apps/connections?scope=organization",
      expect.objectContaining({ credentials: "include" })
    )
  })
})
