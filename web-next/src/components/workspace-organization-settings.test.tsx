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
import type { Session } from "@/lib/auth/client"
import { WorkspaceOrganizationSettings } from "./workspace-organization-settings"

const organization = { id: "org-1", name: "Old name", slug: "old-name" }

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function setup(role: "owner" | "member") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const session: Session = {
    user: { id: "user-1", email: "owner@example.com" },
    organizations: [organization],
  }
  client.setQueryData<Session>(["auth", "session"], session)
  render(
    <QueryClientProvider client={client}>
      <WorkspaceOrganizationSettings
        organization={organization}
        role={role}
        preview={false}
        session={session}
      />
    </QueryClientProvider>
  )
  return client
}

describe("Organisation settings", () => {
  it("lets an owner save the selected organisation and refreshes session data", async () => {
    const fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify({ ...organization, name: "New name" }), {
          headers: { "Content-Type": "application/json" },
        })
    )
    vi.stubGlobal("fetch", fetchMock)
    const client = setup("owner")
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "New name" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/v1/organizations/org-1",
        expect.objectContaining({ method: "PATCH" })
      )
      expect(
        client.getQueryData<Session>(["auth", "session"])?.organizations[0].name
      ).toBe("New name")
    })
  })

  it("keeps organisation editing owner-only", () => {
    setup("member")
    expect(
      screen.getByRole("textbox", { name: "Name" }).hasAttribute("disabled")
    ).toBe(true)
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull()
  })

  it("requires an exact name before deleting an organisation", () => {
    setup("owner")
    fireEvent.click(screen.getByRole("button", { name: "Delete organisation" }))
    const confirm = screen.getByRole("button", { name: "Delete organisation" })
    expect(confirm.hasAttribute("disabled")).toBe(true)
    fireEvent.change(
      screen.getByRole("textbox", {
        name: "Type organisation name to confirm",
      }),
      {
        target: { value: "Old name" },
      }
    )
    expect(confirm.hasAttribute("disabled")).toBe(false)
  })
})
