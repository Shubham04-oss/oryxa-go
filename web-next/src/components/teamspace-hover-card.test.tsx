// @vitest-environment jsdom

import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { toast } from "sonner"
import { Toaster } from "@/components/ui/sonner"
import { TeamspaceCard, TeamspaceHoverCard } from "./teamspace-hover-card"
import { notifyCopied, notifyParleyStarted } from "./workspace-toasts"
import type { Teamspace } from "@/lib/teamspaces/client"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const space = {
  id: "tsp_1",
  organization_id: "org_1",
  team_id: "team_1",
  team_name: "Launch",
  name: "EU launch",
  description: "Launch on 4 November",
  created_by_user_id: "usr_ada",
  whisperers: [
    {
      user_id: "usr_ada",
      user_display_name: "Ada Brennan",
      agent_id: "agt_1",
      agent_name: "Krude",
      mode: "whisperer",
    },
    {
      user_id: "usr_raya",
      user_display_name: "Raya Patel",
      agent_id: "agt_2",
      agent_name: "Raya's agent",
      mode: "whisperer",
    },
  ],
  contributions: [{ agent_id: "agt_3" }],
} as unknown as Teamspace

const fetchMock = vi.fn<typeof fetch>()
let call: unknown = null

beforeEach(() => {
  call = null
  vi.stubGlobal("fetch", fetchMock)
  fetchMock.mockImplementation(async (url) => {
    if (String(url).endsWith("/rooms/teamspace/tsp_1/call"))
      return json({ call })
    return json({ error: { code: "not_found", message: "no" } }, 404)
  })
})

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

function withQueries(node: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return <QueryClientProvider client={client}>{node}</QueryClientProvider>
}

describe("the teamspace hover card", () => {
  it("shows the teamspace, who's in it and its agents", async () => {
    render(withQueries(<TeamspaceCard space={space} />))
    expect(screen.getByRole("heading", { name: "EU launch" })).toBeTruthy()
    expect(screen.getByText("Launch on 4 November")).toBeTruthy()
    expect(screen.getByText("2 people · 2 agents")).toBeTruthy()
    await new Promise((resolve) => setTimeout(resolve, 10))
    expect(screen.queryByText(/Parley live/)).toBeNull()
  })

  it("says a parley is live from the room's own call", async () => {
    call = {
      callId: "call_1",
      startedAt: "2026-10-15T10:00:00Z",
      startedBy: "usr_raya",
      participants: [
        { member: "usr_raya", name: "Raya Patel", joinedAt: "x" },
        { member: "usr_ada", name: "Ada", joinedAt: "x" },
      ],
      recording: { on: false, by: null, since: null },
      agent: { present: false, name: "Krude" },
    }
    render(withQueries(<TeamspaceCard space={space} />))
    expect(await screen.findByText("Parley live · 2 people in")).toBeTruthy()
  })

  it("opens from the sidebar's own item", async () => {
    render(
      withQueries(
        <TeamspaceHoverCard space={space}>
          <button type="button">EU launch</button>
        </TeamspaceHoverCard>
      )
    )
    const item = screen.getByRole("button", { name: "EU launch" })
    fireEvent.focus(item)
    fireEvent.mouseEnter(item)
    expect(
      await screen.findByText("2 people · 2 agents", {}, { timeout: 2000 })
    ).toBeTruthy()
  })
})

describe("the workspace's toasts", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({
        matches: false,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      }))
    )
  })

  it("says a link was copied, and who started a parley with Join", async () => {
    const onJoin = vi.fn()
    render(<Toaster />)
    act(() => notifyCopied())
    expect(await screen.findByText("Link copied")).toBeTruthy()
    act(() =>
      notifyParleyStarted({
        member: "usr_raya",
        name: "Raya Patel",
        roomName: "EU launch",
        onJoin,
      })
    )
    expect(await screen.findByText("Raya Patel started a parley")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Join" }))
    expect(onJoin).toHaveBeenCalled()
    // Sonner removes dismissed toasts on a timer: let it run here, not after
    // the test environment has gone.
    act(() => {
      toast.dismiss()
    })
    await waitFor(() =>
      expect(document.querySelector("[data-sonner-toast]")).toBeNull()
    )
  })
})
