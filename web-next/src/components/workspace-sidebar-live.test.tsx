// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { CallProvider } from "./call-provider"
import { ParleyButton } from "./call-surfaces"
import {
  LIVE_DOT_CAP,
  LiveNow,
  useLiveTeamspaces,
} from "./workspace-sidebar-live"
import type { CallView } from "@/lib/call/client"
import type { CallMedia } from "@/lib/call/media"
import type { Teamspace } from "@/lib/teamspaces/client"

class QuietSocket extends EventTarget {
  send() {}
  close() {}
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const running: CallView = {
  callId: "call_1",
  startedAt: new Date(Date.now() - 65_000).toISOString(),
  startedBy: "usr_raya",
  participants: [],
  recording: { on: false, by: null, since: null },
  agent: { present: false, name: "Krude" },
}

// Which teamspace rooms have a parley running.
let live = new Set<string>()
const fetchMock = vi.fn<typeof fetch>()

function media(): CallMedia {
  return {
    tiles: () => [],
    onChange: () => () => undefined,
    onTranscript: () => () => undefined,
    setMic: vi.fn(async () => undefined),
    setCamera: vi.fn(async () => undefined),
    setScreen: vi.fn(async () => undefined),
    leave: vi.fn(async () => undefined),
  }
}

function space(id: string): Teamspace {
  return { id, name: id } as Teamspace
}

function Dots({ spaces }: { spaces: Teamspace[] }) {
  const ids = useLiveTeamspaces("org_1", spaces)
  return <p data-testid="live">{[...ids].join(",")}</p>
}

function renderWith(children: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <CallProvider joinMediaWith={vi.fn(async () => media())}>
        {children}
      </CallProvider>
    </QueryClientProvider>
  )
}

beforeEach(() => {
  live = new Set()
  vi.stubGlobal("WebSocket", QuietSocket)
  vi.stubGlobal("fetch", fetchMock)
  fetchMock.mockImplementation(async (url, init) => {
    const target = String(url)
    const method = init?.method ?? "GET"
    const room = /rooms\/teamspace\/([^/]+)\//.exec(target)?.[1] ?? ""
    if (target.endsWith("/call") && method === "GET")
      return json({ call: live.has(room) ? running : null })
    if (target.includes("/log"))
      return json({ events: [], next: 0, more: false })
    if (target.endsWith("/call/join")) {
      live.add(room)
      return json({
        callId: "call_1",
        meetingId: "m_1",
        participantId: "p_me",
        token: "rtk-token",
        preset: "oryxa-member",
      })
    }
    if (target.endsWith("/call/leave")) {
      live.delete(room)
      return json({ left: true })
    }
    return json({ error: { code: "not_found", message: "no" } }, 404)
  })
})

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

describe("Live now", () => {
  it("shows nothing until the member is in a parley, then its name, mute, camera and leave", async () => {
    renderWith(
      <>
        <ParleyButton
          organizationId="org_1"
          room={{ kind: "teamspace", id: "tsp_1" }}
          roomName="EU launch"
          member="usr_ada"
          name="Ada"
        />
        <LiveNow />
      </>
    )
    expect(screen.queryByRole("region", { name: "Live now" })).toBeNull()

    fireEvent.click(await screen.findByRole("button", { name: "Parley" }))
    expect(
      await screen.findByRole("button", {
        name: "Open the parley in EU launch",
      })
    ).toBeTruthy()
    expect(await screen.findByRole("button", { name: "Mute" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "Turn camera on" })).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Leave parley" }))
    await vi.waitFor(() =>
      expect(screen.queryByRole("region", { name: "Live now" })).toBeNull()
    )
  })
})

describe("useLiveTeamspaces", () => {
  it("marks the teamspaces with a parley running, reading only the first few", async () => {
    live = new Set(["tsp_1", `tsp_${LIVE_DOT_CAP + 1}`])
    const spaces = Array.from({ length: LIVE_DOT_CAP + 2 }, (_, index) =>
      space(`tsp_${index + 1}`)
    )
    renderWith(<Dots spaces={spaces} />)

    await vi.waitFor(() =>
      expect(screen.getByTestId("live").textContent).toBe("tsp_1")
    )
    const read = fetchMock.mock.calls.filter(([url]) =>
      String(url).endsWith("/call")
    )
    expect(read).toHaveLength(LIVE_DOT_CAP)
  })
})
