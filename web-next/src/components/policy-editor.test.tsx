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
import { ApiError } from "@/lib/api/client"
import type { Policy } from "@/lib/policies/client"
import { PolicyEditor } from "./policy-editor"
import type { PolicyEditorClient } from "./policy-editor"

function show(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

function policy(overrides: Partial<Policy> = {}): Policy {
  return {
    id: "pol_1",
    name: "Hide confidential",
    text: "forbid (principal, action, resource) when { resource.metadata has confidential };",
    created_by: "user-1",
    created_at: "2026-01-05T12:00:00Z",
    updated_at: "2026-01-05T12:00:00Z",
    ...overrides,
  }
}

function stubClient(
  overrides: Partial<PolicyEditorClient> = {}
): PolicyEditorClient {
  return {
    list: vi.fn(async () => []),
    create: vi.fn(async () => policy()),
    remove: vi.fn(async () => undefined),
    ...overrides,
  }
}

const resource = { kind: "corpus", id: "cor_1" }

afterEach(() => cleanup())

describe("PolicyEditor", () => {
  it("always shows the narrowing-only explanation, and lists existing policies", async () => {
    const client = stubClient({
      list: vi.fn(async () => [
        policy(),
        policy({ id: "pol_2", name: "Second policy" }),
      ]),
    })
    show(<PolicyEditor resource={resource} client={client} canManage />)

    expect(
      screen.getByText(
        "Policies can only restrict who sees what here; they never grant access."
      )
    ).toBeTruthy()
    expect(await screen.findByText("Hide confidential")).toBeTruthy()
    expect(screen.getByText("Second policy")).toBeTruthy()
    expect(
      screen.getAllByText(/forbid \(principal, action, resource\)/)
    ).toHaveLength(2)
    expect(screen.getAllByText(/^Added by user-1/)).toHaveLength(2)
  })

  it("says so when there are none yet", async () => {
    show(<PolicyEditor resource={resource} client={stubClient()} canManage />)
    expect(await screen.findByText("No policies yet")).toBeTruthy()
  })

  it("shows a loading state before the list settles", () => {
    const client = stubClient({
      list: vi.fn(() => new Promise<Policy[]>(() => {})),
    })
    show(<PolicyEditor resource={resource} client={client} canManage />)
    expect(screen.getByRole("status").textContent).toContain("Loading policies")
  })

  it("tells a non-admin only admins can manage policies, and never fetches", () => {
    const client = stubClient()
    show(<PolicyEditor resource={resource} client={client} canManage={false} />)
    expect(
      screen.getByText("Only organization admins can manage policies")
    ).toBeTruthy()
    expect(client.list).not.toHaveBeenCalled()
  })

  it("shows the same admins-only message when the server itself refuses with a 403", async () => {
    const client = stubClient({
      list: vi.fn(async () => {
        throw new ApiError("you may not do that here", 403)
      }),
    })
    show(<PolicyEditor resource={resource} client={client} canManage />)
    expect(
      await screen.findByText("Only organization admins can manage policies")
    ).toBeTruthy()
  })

  it("shows a network-error state with a retry that recovers", async () => {
    let attempt = 0
    const client = stubClient({
      list: vi.fn(async () => {
        attempt += 1
        if (attempt === 1) throw new ApiError("boom", 500)
        return [policy()]
      }),
    })
    show(<PolicyEditor resource={resource} client={client} canManage />)

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("Couldn’t load policies.")
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(await screen.findByText("Hide confidential")).toBeTruthy()
  })

  it("adds a policy prefilled with the example Cedar text, and refetches the list", async () => {
    let policies: Policy[] = []
    const client = stubClient({
      list: vi.fn(async () => policies),
      create: vi.fn(async (input) => {
        const created = policy({ id: "pol_new", ...input })
        policies = [...policies, created]
        return created
      }),
    })
    show(<PolicyEditor resource={resource} client={client} canManage />)
    await screen.findByText("No policies yet")

    fireEvent.click(screen.getByRole("button", { name: "Add policy" }))
    const text = screen.getByRole<HTMLTextAreaElement>("textbox", {
      name: "Cedar policy",
    })
    expect(text.value).toContain(
      'forbid (principal, action == Oryxa::Action::"read"'
    )
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "New policy" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Add policy" }))

    await waitFor(() =>
      expect(client.create).toHaveBeenCalledWith({
        name: "New policy",
        text: text.value,
      })
    )
    expect(await screen.findByText("New policy")).toBeTruthy()
    expect(screen.queryByRole("textbox", { name: "Name" })).toBeNull()
  })

  it("requires a name and text before submitting", async () => {
    const client = stubClient()
    show(<PolicyEditor resource={resource} client={client} canManage />)
    await screen.findByText("No policies yet")

    fireEvent.click(screen.getByRole("button", { name: "Add policy" }))
    fireEvent.change(screen.getByRole("textbox", { name: "Cedar policy" }), {
      target: { value: "  " },
    })
    fireEvent.click(screen.getByRole("button", { name: "Add policy" }))

    expect(screen.getByText("Name this policy.")).toBeTruthy()
    expect(screen.getByText("Write the policy’s Cedar text.")).toBeTruthy()
    expect(client.create).not.toHaveBeenCalled()
  })

  it("shows the server's 400 message inline and keeps the form open", async () => {
    const client = stubClient({
      create: vi.fn(async () => {
        throw new ApiError("only forbid statements are allowed", 400)
      }),
    })
    show(<PolicyEditor resource={resource} client={client} canManage />)
    await screen.findByText("No policies yet")

    fireEvent.click(screen.getByRole("button", { name: "Add policy" }))
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "New policy" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Add policy" }))

    expect(
      await screen.findByText("Only forbid statements are allowed.")
    ).toBeTruthy()
    expect(screen.getByRole("textbox", { name: "Name" })).toBeTruthy()
  })

  it("shows a plain duplicate-name message on a 409", async () => {
    const client = stubClient({
      create: vi.fn(async () => {
        throw new ApiError("a policy already exists with that name", 409)
      }),
    })
    show(<PolicyEditor resource={resource} client={client} canManage />)
    await screen.findByText("No policies yet")

    fireEvent.click(screen.getByRole("button", { name: "Add policy" }))
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "Dup" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Add policy" }))

    expect(
      await screen.findByText("A policy with this name already exists.")
    ).toBeTruthy()
  })

  it("disables Cancel and submit while the add request is pending", async () => {
    let resolveCreate: (value: Policy) => void = () => {}
    const client = stubClient({
      create: vi.fn(
        () => new Promise<Policy>((resolve) => (resolveCreate = resolve))
      ),
    })
    show(<PolicyEditor resource={resource} client={client} canManage />)
    await screen.findByText("No policies yet")

    fireEvent.click(screen.getByRole("button", { name: "Add policy" }))
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "New policy" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Add policy" }))

    await waitFor(() => {
      expect(
        screen.getByRole<HTMLButtonElement>("button", { name: "Cancel" })
          .disabled
      ).toBe(true)
      expect(
        screen.getByRole<HTMLButtonElement>("button", { name: "Adding…" })
          .disabled
      ).toBe(true)
    })

    resolveCreate(policy({ id: "pol_new", name: "New policy" }))
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Adding…" })).toBeNull()
    )
  })

  it("asks for confirmation before removing, then deletes and refetches", async () => {
    let policies = [policy()]
    const client = stubClient({
      list: vi.fn(async () => policies),
      remove: vi.fn(async () => {
        policies = []
      }),
    })
    show(<PolicyEditor resource={resource} client={client} canManage />)
    await screen.findByText("Hide confidential")

    fireEvent.click(
      screen.getByRole("button", { name: "Remove Hide confidential" })
    )
    expect(screen.getByText(/Remove “Hide confidential”\?/)).toBeTruthy()
    expect(client.remove).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(screen.queryByText(/Remove “Hide confidential”\?/)).toBeNull()
    expect(client.remove).not.toHaveBeenCalled()

    fireEvent.click(
      screen.getByRole("button", { name: "Remove Hide confidential" })
    )
    fireEvent.click(screen.getByRole("button", { name: "Remove" }))

    await waitFor(() => expect(client.remove).toHaveBeenCalledWith("pol_1"))
    expect(await screen.findByText("No policies yet")).toBeTruthy()
  })

  it("keeps the policy and shows an error inline when removal fails", async () => {
    const client = stubClient({
      list: vi.fn(async () => [policy()]),
      remove: vi.fn(async () => {
        throw new ApiError("cannot remove that policy", 500)
      }),
    })
    show(<PolicyEditor resource={resource} client={client} canManage />)
    await screen.findByText("Hide confidential")

    fireEvent.click(
      screen.getByRole("button", { name: "Remove Hide confidential" })
    )
    fireEvent.click(screen.getByRole("button", { name: "Remove" }))

    expect(
      await screen.findByText("Could not remove it. Try again.")
    ).toBeTruthy()
    expect(screen.getByText("Hide confidential")).toBeTruthy()
  })

  it("expands and collapses a long policy's text", async () => {
    const long = policy({
      text: "forbid (principal, action, resource) when { ".padEnd(200, "x"),
    })
    show(
      <PolicyEditor
        resource={resource}
        client={stubClient({ list: vi.fn(async () => [long]) })}
        canManage
      />
    )
    await screen.findByText("Hide confidential")

    const toggle = screen.getByRole("button", { name: "Show more" })
    expect(toggle.getAttribute("aria-expanded")).toBe("false")
    fireEvent.click(toggle)
    expect(
      screen
        .getByRole("button", { name: "Show less" })
        .getAttribute("aria-expanded")
    ).toBe("true")
  })
})
