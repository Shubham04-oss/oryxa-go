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
    fireEvent.click(screen.getByRole("button", { name: "Rename" }))
    const form = screen.getByRole("form", { name: "Rename organisation" })
    // The action row: Cancel, then the primary on the right.
    expect(
      within(form)
        .getAllByRole("button")
        .map((button) => button.textContent)
    ).toEqual(["Cancel", "Save changes"])
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
    expect(screen.queryByRole("button", { name: "Rename" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
    expect(
      screen.getByText("Only owners can change these details.")
    ).toBeTruthy()
    expect(screen.getByText("old-name")).toBeTruthy()
  })

  it("saves the email domains with the name and slug it has", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) =>
        new Response(
          JSON.stringify({
            ...organization,
            ...(JSON.parse(String(init?.body ?? "{}")) as object),
          }),
          { headers: { "Content-Type": "application/json" } }
        )
    )
    vi.stubGlobal("fetch", fetchMock)
    setup("owner")
    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    fireEvent.change(screen.getByRole("textbox", { name: "Email domains" }), {
      target: { value: "northstar.io, @Northwind.de" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() =>
      expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
        name: "Old name",
        slug: "old-name",
        domains: ["northstar.io", "northwind.de"],
      })
    )
  })

  it("puts deleting in a Danger zone with a destructive confirm after Cancel", () => {
    setup("owner")
    const zone = screen.getByRole("region", { name: "Danger zone" })
    fireEvent.click(
      within(zone).getByRole("button", { name: "Delete organisation" })
    )
    const buttons = within(zone).getAllByRole("button")
    expect(buttons.map((button) => button.textContent)).toEqual([
      "Cancel",
      "Delete organisation",
    ])
    expect(buttons[1].getAttribute("data-tone")).toBe("danger")
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

  it("lets Cancel close the confirmation while nothing has been sent", () => {
    setup("owner")
    fireEvent.click(screen.getByRole("button", { name: "Delete organisation" }))
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))

    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull()
    expect(
      screen.getByRole("button", { name: "Delete organisation" })
    ).toBeTruthy()
  })
})

describe("Leaving or deleting an organisation that is already under way", () => {
  /** Go's answer to the DELETE, held until the test lets it go. */
  function heldDelete() {
    let answer!: (response: Response) => void
    const held = new Promise<Response>((resolve) => (answer = resolve))
    const fetchMock = vi.fn(async () => held)
    vi.stubGlobal("fetch", fetchMock)
    const assign = vi.fn()
    vi.stubGlobal("location", { assign })
    return { answer, assign, fetchMock }
  }

  const cancel = () => screen.getByRole("button", { name: "Cancel" })

  it("keeps Cancel disabled while a leave runs, and goes on to the home page", async () => {
    const { answer, assign, fetchMock } = heldDelete()
    setup("member")
    fireEvent.click(screen.getByRole("button", { name: "Leave organisation" }))
    fireEvent.click(screen.getByRole("button", { name: "Leave organisation" }))

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/v1/organizations/org-1/members/user-1",
        expect.objectContaining({ method: "DELETE" })
      )
    )
    // The request is out: Cancel cannot take it back, so it is not offered,
    // and pressing it anyway leaves the confirmation where it is.
    expect(cancel().hasAttribute("disabled")).toBe(true)
    fireEvent.click(cancel())
    expect(screen.getByText(/Leave Old name\?/)).toBeTruthy()
    expect(
      screen
        .getByRole("button", { name: "Leave organisation" })
        .hasAttribute("disabled")
    ).toBe(true)

    answer(new Response(null, { status: 204 }))
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/"))
    // Nor between the answer and the page going away.
    expect(cancel().hasAttribute("disabled")).toBe(true)
  })

  it("keeps Cancel disabled while a delete runs, and offers it again if Go refuses", async () => {
    const { answer, assign } = heldDelete()
    setup("owner")
    fireEvent.click(screen.getByRole("button", { name: "Delete organisation" }))
    fireEvent.change(
      screen.getByRole("textbox", {
        name: "Type organisation name to confirm",
      }),
      { target: { value: "Old name" } }
    )
    fireEvent.click(screen.getByRole("button", { name: "Delete organisation" }))

    await waitFor(() => expect(cancel().hasAttribute("disabled")).toBe(true))
    fireEvent.click(cancel())
    expect(
      screen.getByRole("textbox", { name: "Type organisation name to confirm" })
    ).toBeTruthy()

    answer(
      new Response(JSON.stringify({ error: "you may not delete this" }), {
        status: 403,
        headers: { "Content-Type": "application/json" },
      })
    )
    expect((await screen.findByRole("alert")).textContent).toBe(
      "you may not delete this"
    )
    expect(assign).not.toHaveBeenCalled()
    expect(cancel().hasAttribute("disabled")).toBe(false)
    fireEvent.click(cancel())
    expect(
      screen.queryByRole("textbox", {
        name: "Type organisation name to confirm",
      })
    ).toBeNull()
  })
})
