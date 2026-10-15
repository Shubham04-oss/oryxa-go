// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactElement } from "react"
import { WorkspaceBillingSettings } from "./workspace-billing-settings"
import { WorkspaceTeamsSettings } from "./workspace-teams-settings"
import { WorkspaceToolAccessSettings } from "./workspace-tool-access-settings"

const organization = { id: "org-1", name: "Oryxa", slug: "oryxa" }

function show(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
  })
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("connected settings sections", () => {
  it("uses billing entitlements and only offers checkout when allowed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) =>
        json(
          url.endsWith("/plans")
            ? {
                plans: [{ key: "team", member_limit: 10 }],
                unpaid: { member_limit: 2 },
              }
            : {
                can_manage: false,
                organization_id: "org-1",
                entitlements: {
                  plan: "unpaid",
                  status: "unpaid",
                  member_limit: 2,
                  grace_ends_at: null,
                },
                subscription: null,
              }
        )
      )
    )
    show(
      <WorkspaceBillingSettings organization={organization} preview={false} />
    )
    expect(await screen.findByText("Up to 2 members")).toBeTruthy()
    expect(
      screen.getByText("An organisation owner can choose a plan.")
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Choose" })).toBeNull()
  })

  it("shows the plan as one card with renewal and seats, and Change plan opens the portal", async () => {
    const assign = vi.fn()
    vi.stubGlobal("location", { ...window.location, assign })
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) =>
      json(
        url.endsWith("/plans")
          ? { plans: [{ key: "team", member_limit: 10 }], unpaid: {} }
          : url.endsWith("/members")
            ? {
                members: [1, 2, 3].map((n) => ({
                  organization_id: "org-1",
                  user_id: `u${n}`,
                  email: `u${n}@example.com`,
                  role: "member",
                  joined_at: "2026-09-01T00:00:00Z",
                })),
              }
            : url.endsWith("/portal") && init?.method === "POST"
              ? { portal_url: "https://billing.example/portal" }
              : {
                  can_manage: true,
                  organization_id: "org-1",
                  entitlements: {
                    plan: "team",
                    status: "active",
                    member_limit: 10,
                    grace_ends_at: null,
                  },
                  subscription: {
                    plan: "team",
                    status: "active",
                    quantity: 1,
                    renews_at: "2026-11-01T00:00:00Z",
                    ends_at: null,
                  },
                }
      )
    )
    vi.stubGlobal("fetch", fetchMock)
    show(
      <WorkspaceBillingSettings organization={organization} preview={false} />
    )
    expect(await screen.findByText("Renews 1 November 2026")).toBeTruthy()
    expect(await screen.findByText("3 of 10 seats")).toBeTruthy()
    expect(
      screen
        .getByRole("meter", { name: "Seats used" })
        .getAttribute("aria-valuenow")
    ).toBe("3")
    // A subscription changes plan in the portal; no checkout list.
    expect(screen.queryByRole("region", { name: "Plans" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Change plan" }))
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith("https://billing.example/portal")
    )
  })

  it("creates a real team through the teams service", async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) =>
      json(
        url.endsWith("/teams") && init?.method === "POST"
          ? {
              id: "team-1",
              name: "Design",
              organization_id: "org-1",
              can_manage: true,
            }
          : url.endsWith("/teams")
            ? { teams: [] }
            : url.endsWith("/members")
              ? { members: [] }
              : {
                  team: { id: "team-1", name: "Design" },
                  membership: null,
                  can_manage: true,
                }
      )
    )
    vi.stubGlobal("fetch", fetchMock)
    show(
      <WorkspaceTeamsSettings
        organization={organization}
        role="admin"
        preview={false}
      />
    )
    await screen.findByText("No teams yet.")
    fireEvent.click(screen.getByRole("button", { name: "New team" }))
    const dialog = await screen.findByRole("dialog", { name: "New team" })
    // A dialog footer: Cancel, then the confirm on the right.
    expect(
      within(dialog)
        .getAllByRole("button")
        .map((button) => button.textContent)
        .slice(-2)
    ).toEqual(["Cancel", "Create"])
    fireEvent.change(screen.getByPlaceholderText("Team name"), {
      target: { value: "Design" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Create" }))
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/v1/organizations/org-1/teams",
        expect.objectContaining({ method: "POST", body: '{"name":"Design"}' })
      )
    )
  })

  it("commits a team member's role only on an explicit choice, and re-syncs the team detail so a self-demotion drops the admin controls", async () => {
    // "me" starts as the team's admin, so `can_manage` is true and the
    // detail query gates rename/add-member/delete-team on it. Demoting
    // "me" to member must make Go's next `can_manage` false, and the page
    // must go re-read it instead of trusting the value it already has.
    let detailRequests = 0
    let role: "admin" | "member" = "admin"
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/v1/organizations/org-1/teams" && init?.method === "GET") {
        return json({
          teams: [
            {
              id: "team-1",
              name: "Design",
              organization_id: "org-1",
              role: "admin",
              can_manage: true,
            },
          ],
        })
      }
      if (
        url === "/v1/organizations/org-1/teams/team-1" &&
        init?.method === "GET"
      ) {
        detailRequests += 1
        const canManage = detailRequests === 1
        return json({
          team: {
            id: "team-1",
            name: "Design",
            organization_id: "org-1",
            can_manage: canManage,
          },
          membership: { user_id: "me", email: "me@example.com", role },
          can_manage: canManage,
        })
      }
      if (
        url === "/v1/organizations/org-1/teams/team-1/members" &&
        init?.method === "GET"
      ) {
        return json({
          members: [
            {
              user_id: "me",
              email: "me@example.com",
              display_name: "Me",
              role,
              notify_email: false,
            },
          ],
        })
      }
      if (
        url === "/v1/organizations/org-1/teams/team-1/members/me" &&
        init?.method === "PATCH"
      ) {
        role = "member"
        return json({ user_id: "me", email: "me@example.com", role })
      }
      if (url === "/v1/organizations/org-1/members" && init?.method === "GET") {
        return json({ members: [] })
      }
      throw new Error(`unexpected fetch: ${init?.method} ${url}`)
    })
    vi.stubGlobal("fetch", fetchMock)
    show(
      <WorkspaceTeamsSettings
        organization={organization}
        role="admin"
        preview={false}
      />
    )

    fireEvent.click(await screen.findByRole("button", { name: /Design/ }))
    await screen.findByText("Manage this team and its members.")

    fireEvent.click(await screen.findByRole("button", { name: "Role for Me" }))
    // Browsing the open menu with the keyboard must not commit anything.
    fireEvent.keyDown(screen.getByRole("menuitemradio", { name: "Admin" }), {
      key: "ArrowDown",
    })
    fireEvent.keyDown(screen.getByRole("menuitemradio", { name: "Member" }), {
      key: "ArrowDown",
    })
    expect(fetchMock.mock.calls.some(([, i]) => i?.method === "PATCH")).toBe(
      false
    )

    fireEvent.click(screen.getByRole("menuitemradio", { name: "Member" }))
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/v1/organizations/org-1/teams/team-1/members/me",
        expect.objectContaining({
          method: "PATCH",
          body: '{"role":"member"}',
        })
      )
    )
    expect(
      fetchMock.mock.calls.filter(([, i]) => i?.method === "PATCH")
    ).toHaveLength(1)

    // The stale `can_manage: true` must not survive the demotion: the
    // detail query is re-read, and every admin-only control it gates
    // disappears without a reload.
    await waitFor(() =>
      expect(screen.getByText("Your place in this team.")).toBeTruthy()
    )
    expect(screen.queryByRole("button", { name: "Delete team" })).toBeNull()
    expect(screen.queryByLabelText("Add a member")).toBeNull()
    expect(detailRequests).toBeGreaterThan(1)
  })

  it("groups organisation connections without exposing admin data to members", async () => {
    const fetchMock = vi.fn(async () =>
      json({
        connections: [
          { id: "a", toolkit: "github", member_id: "one", status: "active" },
          { id: "b", toolkit: "github", member_id: "two", status: "expired" },
        ],
      })
    )
    vi.stubGlobal("fetch", fetchMock)
    show(
      <WorkspaceToolAccessSettings
        organization={organization}
        role="admin"
        preview={false}
        onOpenTools={() => {}}
      />
    )
    expect(
      await screen.findByText("2 members connected · 1 active")
    ).toBeTruthy()
    cleanup()
    fetchMock.mockClear()
    show(
      <WorkspaceToolAccessSettings
        organization={organization}
        role="member"
        preview={false}
        onOpenTools={() => {}}
      />
    )
    expect(screen.getByText(/owners and admins can see/)).toBeTruthy()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
