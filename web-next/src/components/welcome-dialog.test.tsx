// @vitest-environment jsdom

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { WelcomeDialog, welcomeKey } from "./welcome-dialog"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()
let connections: unknown[] = []
let members: unknown[] = []
let threads: unknown[] = []

/** This browser's storage, for the test's own runs. */
let stored = new Map<string, string>()

beforeEach(() => {
  stored = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => stored.get(key) ?? null,
    setItem: (key: string, value: string) => stored.set(key, value),
    removeItem: (key: string) => stored.delete(key),
    clear: () => stored.clear(),
  })
  connections = []
  members = [
    {
      user_id: "usr_ada",
      email: "ada@example.com",
      role: "owner",
      joined_at: new Date().toISOString(),
    },
  ]
  threads = []
  vi.stubGlobal("fetch", fetchMock)
  fetchMock.mockImplementation(async (url) => {
    const target = String(url)
    if (target.includes("/apps/connections")) return json({ connections })
    if (target.includes("/members")) return json({ members })
    if (target.includes("/threads")) return json({ threads })
    return json({ error: { code: "not_found", message: "no" } }, 404)
  })
})

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

function renderWelcome() {
  const handlers = {
    onConnectApps: vi.fn(),
    onInviteTeam: vi.fn(),
    onAskAgent: vi.fn(),
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <WelcomeDialog
        organizationId="org_1"
        viewerId="usr_ada"
        viewerName="Ada Brennan"
        {...handlers}
      />
    </QueryClientProvider>
  )
  return handlers
}

describe("the welcome", () => {
  it("ticks the first steps from what the member has done, and leads to the next", async () => {
    connections = [{ id: "con_1", toolkit: "slack", status: "active" }]
    const handlers = renderWelcome()
    const dialog = await screen.findByRole("dialog", {
      name: "Welcome aboard, Ada",
    })
    expect(dialog.querySelector('[data-scene="crows-nest"]')).toBeTruthy()
    expect(within(dialog).getByText("oryxa")).toBeTruthy()
    const steps = within(dialog).getByRole("list", { name: "First steps" })
    const items = within(steps).getAllByRole("listitem")
    expect(items[0]?.getAttribute("data-done")).toBe("true")
    expect(items[1]?.getAttribute("data-done")).toBeNull()
    expect(within(dialog).getByText("1 of 3 done")).toBeTruthy()

    fireEvent.click(
      within(dialog).getByRole("button", { name: "Invite your team" })
    )
    expect(handlers.onInviteTeam).toHaveBeenCalled()
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    expect(
      window.localStorage.getItem(welcomeKey("org_1", "usr_ada"))
    ).not.toBeNull()
  })

  it("shows once: not again after Skip", async () => {
    renderWelcome()
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Skip for now" })
    )
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    cleanup()
    renderWelcome()
    // Nothing is read, and nothing opens.
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(screen.queryByRole("dialog")).toBeNull()
  })

  it("doesn't welcome someone who joined long ago", async () => {
    members = [
      {
        user_id: "usr_ada",
        email: "ada@example.com",
        role: "owner",
        joined_at: "2026-01-02T10:00:00Z",
      },
    ]
    renderWelcome()
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3))
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(screen.queryByRole("dialog")).toBeNull()
  })

  it("stays away when every step is done", async () => {
    connections = [{ id: "con_1", toolkit: "slack", status: "active" }]
    members = [...members, { user_id: "usr_raya", role: "member" }]
    threads = [{ id: "thr_1" }]
    renderWelcome()
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3))
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(screen.queryByRole("dialog")).toBeNull()
  })
})
