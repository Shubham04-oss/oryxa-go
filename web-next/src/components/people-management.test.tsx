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
  getInvitations,
  getMembers,
  getReceivedInvitations,
  revokeInvitation,
  sendInvitation,
} from "@/lib/auth/invitations"
import { PeopleManagement } from "./people-management"

vi.mock("@/lib/auth/invitations", () => ({
  acceptReceivedInvitation: vi.fn(),
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
    fireEvent.change(screen.getByRole("textbox", { name: "Email address" }), {
      target: { value: "sam@example.com" },
    })
    fireEvent.change(screen.getByRole("combobox", { name: "Role" }), {
      target: { value: "admin" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Send invite" }))
    await waitFor(() =>
      expect(sendInvitation).toHaveBeenCalledWith(
        "org-1",
        "sam@example.com",
        "admin"
      )
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
})
