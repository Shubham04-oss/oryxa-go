// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  acceptReceivedInvitation,
  changeMemberRole,
  getInvitations,
  getMembers,
  getReceivedInvitations,
  revokeInvitation,
  sendInvitation,
} from "@/lib/auth/invitations"
import { PeopleManagement } from "./people-management"

vi.mock("@/lib/auth/invitations", () => ({
  acceptReceivedInvitation: vi.fn(),
  changeMemberRole: vi.fn(),
  getInvitations: vi.fn(),
  getMembers: vi.fn(),
  getReceivedInvitations: vi.fn(),
  revokeInvitation: vi.fn(),
  sendInvitation: vi.fn(),
}))

function renderPeople(role: "owner" | "admin" | "member") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <PeopleManagement organizationId="org-1" role={role} />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  vi.mocked(getReceivedInvitations).mockResolvedValue([])
  vi.mocked(getMembers).mockResolvedValue([
    {
      organization_id: "org-1",
      user_id: "person-1",
      email: "avery@example.com",
      display_name: "Avery Morgan",
      role: "owner",
      joined_at: "2026-09-20T00:00:00Z",
    },
  ])
  vi.mocked(getInvitations).mockResolvedValue([
    {
      id: "inv-1",
      organization_id: "org-1",
      email: "jordan@example.com",
      role: "member",
      status: "pending",
      created_at: "2026-09-20T00:00:00Z",
      expires_at: "2099-09-30T00:00:00Z",
    },
  ])
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe("PeopleManagement", () => {
  it("lets owners invite admins and revoke pending invitations", async () => {
    vi.mocked(sendInvitation).mockResolvedValue({
      id: "inv-2",
      organization_id: "org-1",
      email: "sam@example.com",
      role: "admin",
      status: "pending",
      created_at: "2026-09-23T00:00:00Z",
      expires_at: "2099-09-30T00:00:00Z",
    })
    vi.mocked(revokeInvitation).mockResolvedValue()
    renderPeople("owner")

    await screen.findByText("Avery Morgan")
    await screen.findByText("jordan@example.com")
    fireEvent.change(screen.getByRole("textbox", { name: "Email addresses" }), {
      target: { value: "sam@example.com, kim@example.com" },
    })
    fireEvent.click(screen.getByRole("combobox", { name: "Role" }))
    const admin = await screen.findByRole("option", { name: "Admin" })
    fireEvent.pointerDown(admin)
    fireEvent.click(admin)
    fireEvent.click(screen.getByRole("button", { name: "Send invite" }))
    // One invitation per address, in order.
    await waitFor(() =>
      expect(vi.mocked(sendInvitation).mock.calls).toEqual([
        ["org-1", "sam@example.com", "admin"],
        ["org-1", "kim@example.com", "admin"],
      ])
    )

    fireEvent.click(
      screen.getByRole("button", {
        name: "Revoke invitation for jordan@example.com",
      })
    )
    await waitFor(() =>
      expect(revokeInvitation).toHaveBeenCalledWith("org-1", "inv-1")
    )
  })

  it("limits admins to member invitations", async () => {
    renderPeople("admin")
    await screen.findByText("jordan@example.com")
    expect(screen.queryByRole("option", { name: "Admin" })).toBeNull()
  })

  it("shows members but no invitation controls to a regular member", async () => {
    renderPeople("member")
    await screen.findByText("Avery Morgan")
    expect(screen.queryByRole("button", { name: "Send invite" })).toBeNull()
    expect(getInvitations).not.toHaveBeenCalled()
    expect(
      screen.getByText("Workspace admins manage invitations.")
    ).toBeTruthy()
  })

  it("shows invitations received by existing members", async () => {
    vi.mocked(getReceivedInvitations).mockResolvedValue([
      {
        id: "inv-next",
        organization_id: "org-next",
        organization_name: "Launch team",
        role: "member",
        created_at: "2026-09-20T00:00:00Z",
        expires_at: "2099-09-30T00:00:00Z",
      },
    ])
    renderPeople("member")
    await screen.findByText("Launch team")
    expect(screen.getByRole("button", { name: "Join" })).toBeTruthy()
    expect(acceptReceivedInvitation).not.toHaveBeenCalled()
  })

  it("changes a member's role only when a different one is chosen from the menu", async () => {
    vi.mocked(getMembers).mockResolvedValue([
      {
        organization_id: "org-1",
        user_id: "person-2",
        email: "jordan@example.com",
        display_name: "Jordan Lee",
        role: "member",
        joined_at: "2026-09-20T00:00:00Z",
      },
    ])
    vi.mocked(changeMemberRole).mockResolvedValue({
      organization_id: "org-1",
      user_id: "person-2",
      email: "jordan@example.com",
      display_name: "Jordan Lee",
      role: "admin",
      joined_at: "2026-09-20T00:00:00Z",
    })
    renderPeople("owner")
    await screen.findByText("Jordan Lee")

    // Opening the menu, by itself, must send nothing.
    fireEvent.click(screen.getByRole("button", { name: "Role for Jordan Lee" }))
    expect(changeMemberRole).not.toHaveBeenCalled()

    // Arrow-key browsing (what a native select fired `change` for on every
    // press) must not commit a role either.
    fireEvent.keyDown(screen.getByRole("menuitemradio", { name: "Owner" }), {
      key: "ArrowDown",
    })
    fireEvent.keyDown(screen.getByRole("menuitemradio", { name: "Admin" }), {
      key: "ArrowDown",
    })
    expect(changeMemberRole).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("menuitemradio", { name: "Admin" }))
    await waitFor(() =>
      expect(changeMemberRole).toHaveBeenCalledWith(
        "org-1",
        "person-2",
        "admin"
      )
    )
    expect(changeMemberRole).toHaveBeenCalledTimes(1)
  })
})
