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
import { CommandPalette } from "./command-palette"
import type {
  CommandPaletteActions,
  CommandPaletteNavigation,
} from "./command-palette"
import { useCommandPalette } from "@/hooks/use-command-palette"
import type { CommandPaletteScope } from "@/hooks/use-command-palette"

const root = "/v1/organizations/org_1"
const json = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  })
const failure = (status: number, code = "forbidden") =>
  json(
    {
      error: { code, message: "SECRET diagnostic" },
    },
    status
  )

function backend(overrides: Record<string, unknown> = {}) {
  const routes: Record<string, unknown> = {
    [`${root}/threads`]: {
      threads: [
        {
          id: "th_1",
          title: "Recent launch",
          updated_at: "2026-09-29T12:00:00Z",
        },
        {
          id: "th_2",
          title: "Older launch",
          updated_at: "2026-09-28T12:00:00Z",
        },
        {
          id: "th_wake",
          title: "Private app update",
          origin: "wakeup",
          updated_at: "2026-09-29T12:00:00Z",
        },
        {
          id: "th_side",
          title: "Private side request",
          origin: "side_conversation",
          updated_at: "2026-09-29T12:00:00Z",
        },
      ],
    },
    [`${root}/agents`]: {
      agents: [
        { id: "agt_1", name: "Mira", kind: "specialist", status: "disabled" },
      ],
    },
    [`${root}/agent-teams`]: {
      teams: [{ id: "atm_1", name: "Writing team", status: "active" }],
    },
    [`${root}/teamspaces`]: {
      teamspaces: [{ id: "tsp_1", name: "Launch room", status: "disabled" }],
    },
    [`${root}/corpora`]: { corpora: [{ id: "base_1", name: "Launch notes" }] },
    [`${root}/members`]: {
      members: [
        { user_id: "member", display_name: "Myself", email: "me@example.com" },
        { user_id: "ada", display_name: "Ada", email: "ada@example.com" },
      ],
    },
    [`${root}/apps/connections`]: {
      connections: [
        {
          id: "con_1",
          label: "Personal Drive",
          toolkit: "googledrive",
          status: "expired",
        },
      ],
    },
    ...overrides,
  }
  const calls: { path: string; method: string }[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      calls.push({ path: url.pathname, method: init?.method ?? "GET" })
      const value = routes[url.pathname]
      if (typeof value === "function") return value()
      return value === undefined ? failure(404, "not_found") : json(value)
    })
  )
  return calls
}

function Harness({
  organizationId = "org_1",
  viewerId = "member",
  actions = {},
  navigation = [],
}: CommandPaletteScope & {
  actions?: CommandPaletteActions
  navigation?: readonly CommandPaletteNavigation[]
}) {
  const palette = useCommandPalette({ organizationId, viewerId })
  return (
    <>
      <button type="button" onClick={() => palette.onOpenChange(true)}>
        Search workspace
      </button>
      <CommandPalette
        {...palette}
        organizationId={organizationId}
        viewerId={viewerId}
        actions={actions}
        navigation={navigation}
      />
    </>
  )
}

function mount(props: Parameters<typeof Harness>[0] = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retryDelay: 0 } },
  })
  const view = render(
    <QueryClientProvider client={client}>
      <Harness {...props} />
    </QueryClientProvider>
  )
  return {
    ...view,
    client,
    update: (next: Parameters<typeof Harness>[0]) =>
      view.rerender(
        <QueryClientProvider client={client}>
          <Harness {...props} {...next} />
        </QueryClientProvider>
      ),
  }
}

function open() {
  const trigger = screen.getByRole("button", { name: "Search workspace" })
  trigger.focus()
  fireEvent.click(trigger)
  return trigger
}

const originalScroll = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  "scrollIntoView"
)
beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
    configurable: true,
    value: vi.fn(),
  })
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  if (originalScroll)
    Object.defineProperty(
      HTMLElement.prototype,
      "scrollIntoView",
      originalScroll
    )
  else Reflect.deleteProperty(HTMLElement.prototype, "scrollIntoView")
})

describe("command palette", () => {
  it("fetches nothing while closed or signed out, including on a shortcut", () => {
    const calls = backend()
    const props = { actions: { openConversation: vi.fn() } }
    const view = mount(props)
    expect(calls).toHaveLength(0)
    view.update({ organizationId: "", viewerId: "" })
    expect(fireEvent.keyDown(window, { key: "k", metaKey: true })).toBe(true)
    open()
    expect(screen.queryByRole("dialog")).toBeNull()
    expect(calls).toHaveLength(0)
  })

  it.each(["metaKey", "ctrlKey"] as const)(
    "opens with %s+K, selects by keyboard, then returns focus after Escape",
    async (modifier) => {
      const calls = backend()
      const choose = vi.fn()
      mount({ actions: { openConversation: choose } })
      const trigger = screen.getByRole("button", { name: "Search workspace" })
      trigger.focus()
      expect(fireEvent.keyDown(trigger, { key: "k", [modifier]: true })).toBe(
        false
      )
      const input = await screen.findByRole("combobox", {
        name: "Search workspace",
      })
      await waitFor(() => expect(document.activeElement).toBe(input))
      const first = await screen.findByRole("option", { name: "Recent launch" })
      const second = screen.getByRole("option", { name: "Older launch" })
      await waitFor(() =>
        expect(first.getAttribute("aria-selected")).toBe("true")
      )
      fireEvent.keyDown(input, { key: "ArrowDown" })
      await waitFor(() =>
        expect(second.getAttribute("aria-selected")).toBe("true")
      )
      fireEvent.keyDown(input, { key: "Enter" })
      expect(choose).toHaveBeenCalledExactlyOnceWith("th_2")
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
      open()
      const reopened = await screen.findByRole("combobox", {
        name: "Search workspace",
      })
      fireEvent.change(reopened, { target: { value: "launch" } })
      fireEvent.keyDown(reopened, { key: "Escape" })
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
      await waitFor(() => expect(document.activeElement).toBe(trigger))
      open()
      expect(screen.getByRole<HTMLInputElement>("combobox").value).toBe("")
      expect(
        calls.every(
          (call) => call.path.endsWith("/threads") && call.method === "GET"
        )
      ).toBe(true)
    }
  )

  it("closes on the shortcut while input is focused and ignores repeat/composition/other chords", async () => {
    const calls = backend()
    mount()
    for (const extras of [
      { repeat: true },
      { isComposing: true },
      { shiftKey: true },
      { altKey: true },
    ])
      fireEvent.keyDown(window, { key: "k", ctrlKey: true, ...extras })
    expect(screen.queryByRole("dialog")).toBeNull()
    open()
    const input = screen.getByRole("combobox")
    fireEvent.keyDown(input, { key: "k", ctrlKey: true })
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    expect(calls).toHaveLength(0)
  })

  it("searches real members locally by name/email, excluding self, without an invented search endpoint", async () => {
    const calls = backend()
    const choose = vi.fn()
    mount({ actions: { openPerson: choose } })
    open()
    await screen.findByRole("option", { name: /Ada/ })
    expect(screen.queryByText("Myself")).toBeNull()
    fireEvent.change(screen.getByRole("combobox"), {
      target: { value: "ADA@EXAMPLE" },
    })
    fireEvent.click(screen.getByRole("option", { name: /Ada/ }))
    expect(choose).toHaveBeenCalledExactlyOnceWith("ada")
    expect(calls).toEqual([{ path: `${root}/members`, method: "GET" }])
  })

  it("offers only adapter-backed sources and explicit caller navigation, with recent chats first", async () => {
    const calls = backend()
    const actions = {
      openConversation: vi.fn(),
      openPerson: vi.fn(),
      openAgent: vi.fn(),
      openAgentTeam: vi.fn(),
      openTeamspace: vi.fn(),
      openKnowledgeBase: vi.fn(),
      openTool: vi.fn(),
    }
    const settings = vi.fn()
    mount({
      actions,
      navigation: [{ id: "settings", label: "Settings", onSelect: settings }],
    })
    open()
    await screen.findByRole("option", { name: /Personal Drive/ })
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(9))
    expect(screen.getAllByRole("option")[0].textContent).toContain(
      "Recent launch"
    )
    expect(screen.queryByText("Private app update")).toBeNull()
    expect(screen.queryByText("Private side request")).toBeNull()
    expect(screen.getByRole("option", { name: /Mira Paused/ })).toBeTruthy()
    expect(
      screen.getByRole("option", { name: /Launch room Paused/ })
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("option", { name: "Settings" }))
    expect(settings).toHaveBeenCalledOnce()
    expect(calls).toHaveLength(7)
    expect(calls.every((call) => call.method === "GET")).toBe(true)
    expect(calls.some((call) => call.path.includes("catalog"))).toBe(false)
  })

  it("bounds local scan and displayed matches without claiming a network limit or universal no-result", async () => {
    backend({
      [`${root}/threads`]: {
        threads: Array.from({ length: 120 }, (_, i) => ({
          id: `th_${i}`,
          title: i === 110 ? "Outside local coverage" : `Chat ${i}`,
        })),
      },
    })
    mount({ actions: { openConversation: vi.fn() } })
    open()
    await screen.findByRole("option", { name: "Chat 0" })
    expect(screen.getAllByRole("option")).toHaveLength(4)
    expect(
      screen.getByText(
        "Showing the first 4 matches. Keep typing to narrow them."
      )
    ).toBeTruthy()
    expect(
      screen.getByText("Search covers the first 100 recent conversations.")
    ).toBeTruthy()
    fireEvent.change(screen.getByRole("combobox"), {
      target: { value: "Outside" },
    })
    expect(screen.getByText("No matches in the items searched.")).toBeTruthy()
    expect(screen.queryByText("Outside local coverage")).toBeNull()
  })

  it.each([
    ["openAgent", "Mira", "agt_1"],
    ["openAgentTeam", "Writing team", "atm_1"],
    ["openTeamspace", "Launch room", "tsp_1"],
    ["openKnowledgeBase", "Launch notes", "base_1"],
    ["openTool", "Personal Drive", "con_1"],
  ] as const)(
    "selects %s with the real item ID and closes without mutations",
    async (adapter, label, id) => {
      const calls = backend()
      const choose = vi.fn()
      mount({ actions: { [adapter]: choose } })
      open()
      fireEvent.click(
        await screen.findByRole("option", { name: new RegExp(label) })
      )
      expect(choose).toHaveBeenCalledExactlyOnceWith(id)
      expect(screen.queryByRole("dialog")).toBeNull()
      expect(calls).toHaveLength(1)
      expect(calls[0]?.method).toBe("GET")
    }
  )

  it("does not reuse another member's cached rows in the same organization", async () => {
    let nextRead = false
    let finish!: (value: Response) => void
    const pending = new Promise<Response>((resolve) => {
      finish = resolve
    })
    const calls = backend({
      [`${root}/members`]: () =>
        nextRead
          ? pending
          : json({
              members: [
                {
                  user_id: "ada",
                  display_name: "Old member private name",
                  email: "ada@example.com",
                },
              ],
            }),
    })
    const view = mount({ actions: { openPerson: vi.fn() } })
    open()
    await screen.findByRole("option", { name: /Old member private name/ })
    nextRead = true
    view.update({ viewerId: "new_member" })
    expect(screen.queryByRole("dialog")).toBeNull()
    open()
    expect(await screen.findByText("Loading people…")).toBeTruthy()
    expect(screen.queryByText("Old member private name")).toBeNull()
    finish(failure(403))
    await screen.findByText("You can't search people in this workspace.")
    expect(screen.queryByRole("option")).toBeNull()
    expect(calls).toHaveLength(2)
  })

  it("closes with the accessible close button and restores the opener's focus", async () => {
    backend()
    mount()
    const trigger = open()
    fireEvent.click(screen.getByRole("button", { name: "Close" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it.each([404, 503])(
    "keeps optional %i sources distinct from successful empty reads",
    async (status) => {
      backend({
        [`${root}/agents`]: () => failure(status, "agents_unavailable"),
      })
      mount({ actions: { openAgent: vi.fn(), openPerson: vi.fn() } })
      open()
      expect(
        await screen.findByText("agents is not available here.")
      ).toBeTruthy()
      expect(screen.getByRole("option", { name: /Ada/ })).toBeTruthy()
      expect(screen.queryByText("No matches in the items searched.")).toBeNull()
      expect(document.body.textContent).not.toContain("SECRET")
    }
  )

  it("hides failed-source results and offers retry without losing independent results", async () => {
    let failed = true
    backend({
      [`${root}/members`]: () =>
        failed
          ? failure(500, "internal")
          : json({
              members: [
                {
                  user_id: "ada",
                  display_name: "Recovered Ada",
                  email: "ada@example.com",
                },
              ],
            }),
    })
    mount({ actions: { openPerson: vi.fn(), openConversation: vi.fn() } })
    open()
    expect(await screen.findByText("Couldn't load people.")).toBeTruthy()
    expect(screen.getByRole("option", { name: "Recent launch" })).toBeTruthy()
    expect(screen.queryByText("No matches in the items searched.")).toBeNull()
    failed = false
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(
      await screen.findByRole("option", { name: /Recovered Ada/ })
    ).toBeTruthy()
    expect(document.body.textContent).not.toContain("SECRET")
  })

  it("fresh-reads on reopening and removes cached choices after a revoked read", async () => {
    let revoked = false
    const choose = vi.fn()
    backend({
      [`${root}/members`]: () =>
        revoked
          ? failure(403)
          : json({
              members: [
                {
                  user_id: "ada",
                  display_name: "Private cached name",
                  email: "ada@example.com",
                },
              ],
            }),
    })
    mount({ actions: { openPerson: choose } })
    open()
    await screen.findByRole("option", { name: /Private cached name/ })
    fireEvent.keyDown(screen.getByRole("combobox"), { key: "Escape" })
    revoked = true
    open()
    expect(
      screen.queryByRole("option", { name: /Private cached name/ })
    ).toBeNull()
    expect(
      await screen.findByText("You can't search people in this workspace.")
    ).toBeTruthy()
    expect(screen.queryByRole("option")).toBeNull()
    expect(screen.queryByText("No matches in the items searched.")).toBeNull()
    expect(choose).not.toHaveBeenCalled()
  })

  it("closes immediately on organization/member changes and never shows a late old-scope response", async () => {
    let finish!: (value: Response) => void
    const pending = new Promise<Response>((resolve) => {
      finish = resolve
    })
    const calls = backend({
      [`${root}/members`]: () => pending,
      "/v1/organizations/org_2/members": {
        members: [
          {
            user_id: "bo",
            display_name: "New workspace Bo",
            email: "bo@example.com",
          },
        ],
      },
    })
    const view = mount({ actions: { openPerson: vi.fn() } })
    open()
    await screen.findByText("Loading people…")
    view.update({ organizationId: "org_2", viewerId: "new_member" })
    expect(screen.queryByRole("dialog")).toBeNull()
    expect(calls).toHaveLength(1)
    open()
    await screen.findByRole("option", { name: /New workspace Bo/ })
    finish(
      json({
        members: [
          {
            user_id: "ada",
            display_name: "Private old scope",
            email: "ada@example.com",
          },
        ],
      })
    )
    await waitFor(() => expect(view.client.isFetching()).toBe(0))
    expect(screen.queryByText("Private old scope")).toBeNull()
    view.update({ organizationId: "org_2", viewerId: "another_member" })
    expect(screen.queryByRole("dialog")).toBeNull()
  })

  it("drops a removed adapter's cached results and does not refetch that source", async () => {
    const calls = backend()
    const view = mount({ actions: { openPerson: vi.fn() } })
    open()
    await screen.findByRole("option", { name: /Ada/ })
    view.update({ actions: {} })
    expect(screen.queryByRole("option")).toBeNull()
    await view.client.invalidateQueries({
      queryKey: ["organizations", "org_1", "members"],
    })
    expect(calls).toHaveLength(1)
  })

  it("stays closed when returning to a scope that had an open palette", async () => {
    const calls = backend()
    const view = mount({ actions: { openPerson: vi.fn() } })
    open()
    await screen.findByRole("option", { name: /Ada/ })
    view.update({ organizationId: "org_2" })
    expect(screen.queryByRole("dialog")).toBeNull()
    view.update({ organizationId: "org_1" })
    expect(screen.queryByRole("dialog")).toBeNull()
    expect(calls).toHaveLength(1)
  })
})
