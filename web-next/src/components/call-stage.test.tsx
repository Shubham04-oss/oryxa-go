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
import { CallProvider } from "./call-provider"
import { CallStageHost } from "./call-stage"
import { CallSurface, ParleyStrip } from "./call-surfaces"
import type { CallView } from "@/lib/call/client"
import type { CallMedia, CallTile } from "@/lib/call/media"

const room = { kind: "teamspace", id: "tsp_1" } as const
const base = "/live/org_1/rooms/teamspace/tsp_1"

class QuietSocket extends EventTarget {
  constructor(readonly url: string) {
    super()
  }
  send() {}
  close() {}
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const view: CallView = {
  callId: "call_1",
  startedAt: new Date(Date.now() - 65_000).toISOString(),
  startedBy: "usr_raya",
  participants: [
    {
      member: "usr_raya",
      name: "Raya Patel",
      joinedAt: "2026-10-14T10:00:00Z",
    },
    { member: "usr_ada", name: "Ada", joinedAt: "2026-10-14T10:01:00Z" },
  ],
  recording: { on: false, by: null, since: null },
  agent: { present: true, name: "Krude" },
}

function fakeMedia(sharing = false): CallMedia {
  const tiles: CallTile[] = [
    {
      id: "p_me",
      member: "usr_ada",
      name: "Ada",
      self: true,
      audio: true,
      video: false,
    },
    {
      id: "p_raya",
      member: "usr_raya",
      name: "Raya Patel",
      self: false,
      audio: true,
      video: false,
      screenTrack: sharing ? ({} as MediaStreamTrack) : undefined,
    },
  ]
  return {
    tiles: () => tiles,
    onChange: () => () => undefined,
    onTranscript: () => () => undefined,
    setMic: vi.fn(async () => undefined),
    setCamera: vi.fn(async () => undefined),
    setScreen: vi.fn(async () => undefined),
    leave: vi.fn(async () => undefined),
    devices: vi.fn(async () => ({
      microphones: [],
      cameras: [],
      speakers: [],
      current: {},
    })),
    setDevice: vi.fn(async () => undefined),
    onDevicesChange: () => () => undefined,
  }
}

function Workspace({ place, onPage }: { place: string; onPage: boolean }) {
  return (
    <div className="workspace-middle">
      <CallStageHost place={place} />
      {onPage ? (
        <main aria-label="Teamspace page">
          <ParleyStrip
            organizationId="org_1"
            room={room}
            roomName="EU launch"
            member="usr_ada"
            name="Ada"
          />
        </main>
      ) : (
        <main aria-label="People" />
      )}
    </div>
  )
}

function renderWorkspace(media = fakeMedia()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const tree = (place: string, onPage = true) => (
    <QueryClientProvider client={client}>
      <CallProvider joinMediaWith={async () => media}>
        <Workspace place={place} onPage={onPage} />
        <CallSurface />
      </CallProvider>
    </QueryClientProvider>
  )
  const { rerender } = render(tree("teamspaces/tsp_1"))
  return {
    goTo: (place: string, onPage = true) => rerender(tree(place, onPage)),
  }
}

async function openStage() {
  const strip = await screen.findByRole("region", {
    name: "Parley in progress",
  })
  fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
  fireEvent.click(await screen.findByRole("button", { name: "Open" }))
  return screen.getByRole("region", { name: "EU launch parley" })
}

const fetchMock = vi.fn<typeof fetch>()

beforeEach(() => {
  vi.stubGlobal("WebSocket", QuietSocket)
  vi.stubGlobal("fetch", fetchMock)
  vi.stubGlobal(
    "MediaStream",
    class {
      constructor(readonly tracks: unknown[]) {}
    }
  )
  fetchMock.mockImplementation(async (url, init) => {
    const target = String(url)
    const method = init?.method ?? "GET"
    if (target.endsWith(`${base}/call`) && method === "GET")
      return json({ call: view })
    if (target.includes(`${base}/log`))
      return json({ events: [], next: 0, more: false })
    if (target.endsWith("/call/join"))
      return json({
        callId: "call_1",
        meetingId: "m_1",
        participantId: "p_me",
        token: "rtk-token",
        preset: "oryxa-member",
      })
    if (target.endsWith("/call/leave")) return json({ left: true })
    return json({ error: { code: "not_found", message: "no" } }, 404)
  })
})

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

describe("the parley's stage", () => {
  it("opens in the main column over the page, which stays mounted and inert", async () => {
    renderWorkspace()
    const stage = await openStage()
    expect(within(stage).getByText(/^Parley · 1:0\d$/)).toBeTruthy()
    expect(
      within(stage).getByRole("img", { name: "2 in the parley" })
    ).toBeTruthy()
    const people = within(stage).getByRole("group", {
      name: "People in the parley",
    })
    expect(
      within(people).getByRole("group", { name: "Raya Patel" })
    ).toBeTruthy()
    expect(
      within(people).getByRole("group", { name: "Krude, the agent" })
    ).toBeTruthy()
    expect(
      within(stage).getByRole("toolbar", { name: "Parley controls" })
    ).toBeTruthy()
    const page = screen.getByRole("main", { hidden: true })
    expect(page.inert).toBe(true)
    // No mini player while the stage shows the call.
    expect(screen.queryByRole("region", { name: /mini player/ })).toBeNull()
  })

  it("moves between the stage, the full page and the mini player", async () => {
    renderWorkspace()
    const stage = await openStage()
    // The header's Full page pill (the dock has one too).
    const [pill] = within(stage).getAllByRole("button", { name: "Full page" })
    fireEvent.click(pill)
    const full = screen.getByRole("dialog", { name: "EU launch parley" })
    fireEvent.click(
      within(full).getByRole("button", { name: "Exit full page" })
    )
    const again = await screen.findByRole("region", {
      name: "EU launch parley",
    })
    fireEvent.click(within(again).getByRole("button", { name: "Mini player" }))
    await waitFor(() =>
      expect(
        screen.queryByRole("region", { name: "EU launch parley" })
      ).toBeNull()
    )
    expect(screen.getByRole("main").inert).toBe(false)
  })

  it("goes to the mini player on Escape, or when the member goes elsewhere", async () => {
    const { goTo } = renderWorkspace()
    await openStage()
    fireEvent.keyDown(window, { key: "Escape" })
    await waitFor(() =>
      expect(
        screen.queryByRole("region", { name: "EU launch parley" })
      ).toBeNull()
    )
    fireEvent.click(screen.getByRole("button", { name: "Open" }))
    await screen.findByRole("region", { name: "EU launch parley" })
    goTo("people", false)
    await screen.findByRole("region", {
      name: "EU launch parley, mini player",
    })
    expect(
      screen.queryByRole("region", { name: "EU launch parley" })
    ).toBeNull()
  })

  it("focuses a shared screen beside a column of named cams, and fills with it", async () => {
    renderWorkspace(fakeMedia(true))
    const stage = await openStage()
    expect(within(stage).getByText("Raya Patel's screen")).toBeTruthy()
    const column = within(stage).getByRole("group", {
      name: "People in the parley",
    })
    const raya = within(column).getByRole("group", {
      name: "Raya Patel, sharing",
    })
    expect(within(raya).getByText("Raya Patel")).toBeTruthy()
    fireEvent.click(
      within(stage).getByRole("button", { name: "Fill the stage" })
    )
    expect(
      within(stage).queryByRole("group", { name: "People in the parley" })
    ).toBeNull()
    fireEvent.click(
      within(stage).getByRole("button", { name: "Show the people" })
    )
    expect(
      within(stage).getByRole("group", { name: "People in the parley" })
    ).toBeTruthy()
  })
})
