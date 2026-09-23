// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
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
