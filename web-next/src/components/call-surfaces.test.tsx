// @vitest-environment jsdom

import { useEffect } from "react"
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
import { AgentPanelChips } from "./agent-panel-chips"
import { CallProvider, useRoomChatHost } from "./call-provider"
import { CallSurface, ParleyButton, ParleyStrip, tintOf } from "./call-surfaces"
import type { CallView } from "@/lib/call/client"
import { callDuration, parseVoiceFrame, whoIsIn } from "@/lib/call/client"
import type { CallMedia, CallTile, HeardSegment } from "@/lib/call/media"

const room = { kind: "teamspace", id: "tsp_1" } as const
const base = "/live/org_1/rooms/teamspace/tsp_1"

class QuietSocket extends EventTarget {
  static all: QuietSocket[] = []
  constructor(readonly url: string) {
    super()
    QuietSocket.all.push(this)
  }
  send() {}
  close() {}
  frame(data: unknown) {
    this.dispatchEvent(
      new MessageEvent("message", { data: JSON.stringify(data) })
    )
  }
}

function socketFor(path: string) {
  return [...QuietSocket.all]
    .reverse()
    .find((socket) => socket.url.includes(path))
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function view(overrides: Partial<CallView> = {}): CallView {
  return {
    callId: "call_1",
    startedAt: new Date(Date.now() - 65_000).toISOString(),
    startedBy: "usr_raya",
    participants: [
      {
        member: "usr_raya",
        name: "Raya Patel",
        joinedAt: "2026-10-14T10:00:00Z",
      },
      {
        member: "usr_rohan",
        name: "Rohan Mehta",
        joinedAt: "2026-10-14T10:01:00Z",
      },
    ],
    recording: { on: false, by: null, since: null },
    agent: { present: true, name: "Krude" },
    ...overrides,
  }
}

function device(
  deviceId: string,
  kind: MediaDeviceKind,
  label: string
): MediaDeviceInfo {
  return { deviceId, kind, label, groupId: "g", toJSON: () => ({}) }
}

function fakeMedia() {
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
    },
  ]
  let heard: ((segment: HeardSegment) => void) | undefined
  const media: CallMedia & { say: (segment: HeardSegment) => void } = {
    tiles: () => tiles,
    onChange: () => () => undefined,
    onTranscript(listener) {
      heard = listener
      return () => undefined
    },
    setMic: vi.fn(async (on: boolean) => {
      tiles[0] = { ...tiles[0], audio: on }
    }),
    setCamera: vi.fn(async () => undefined),
    setScreen: vi.fn(async () => undefined),
    leave: vi.fn(async () => undefined),
    devices: vi.fn(async () => ({
      microphones: [
        device("mic-1", "audioinput", "MacBook mic"),
        device("mic-2", "audioinput", "AirPods"),
      ],
      cameras: [device("cam-1", "videoinput", "FaceTime camera")],
      speakers: [
        device("spk-1", "audiooutput", "MacBook speakers"),
        device("default", "audiooutput", "Default speaker"),
      ],
      current: {
        audioinput: "mic-1",
        videoinput: "cam-1",
        audiooutput: "spk-1",
      },
    })),
    setDevice: vi.fn(async () => undefined),
    onDevicesChange: () => () => undefined,
    say: (segment) => heard?.(segment),
  }
  return media
}

function Parley({ onRoomPage }: { onRoomPage: boolean }) {
  return (
    <>
      {onRoomPage && (
        <>
          <ParleyButton
            organizationId="org_1"
            room={room}
            roomName="EU launch"
            member="usr_ada"
            name="Ada"
          />
          <ParleyStrip
            organizationId="org_1"
            room={room}
            roomName="EU launch"
            member="usr_ada"
            name="Ada"
          />
        </>
      )}
      <CallSurface />
    </>
  )
}

function renderParley(
  join: () => Promise<CallMedia> = vi.fn(async () => fakeMedia())
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const tree = (onRoomPage: boolean) => (
    <QueryClientProvider client={client}>
      <CallProvider joinMediaWith={join}>
        <Parley onRoomPage={onRoomPage} />
      </CallProvider>
    </QueryClientProvider>
  )
  const { rerender } = render(tree(true))
  return { join, leaveRoomPage: () => rerender(tree(false)) }
}

const fetchMock = vi.fn<typeof fetch>()
let current: CallView | null = view()

function requests(path: string, method = "GET") {
  return fetchMock.mock.calls.filter(([url, init]) => {
    const target = String(url)
    return target.includes(path) && (init?.method ?? "GET") === method
  })
}

beforeEach(() => {
  QuietSocket.all = []
  current = view()
  vi.stubGlobal("WebSocket", QuietSocket)
  vi.stubGlobal("fetch", fetchMock)
  fetchMock.mockImplementation(async (url, init) => {
    const target = String(url)
    const method = init?.method ?? "GET"
    if (target.endsWith(`${base}/call`) && method === "GET")
      return json({ call: current })
    if (target.includes(`${base}/log`))
      return json({ events: [], next: 0, more: false })
    if (target.endsWith("/call/join")) {
      return json({
        callId: "call_1",
        meetingId: "m_1",
        participantId: "p_me",
        token: "rtk-token",
        preset: "oryxa-member",
      })
    }
    if (target.endsWith("/call/leave")) return json({ left: true })
    if (target.endsWith("/call/transcript")) return json({ accepted: 1 }, 202)
    return json({ error: { code: "not_found", message: "no" } }, 404)
  })
})

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

describe("the call's words", () => {
  it("says how long a call has run and who is in", () => {
    expect(
      callDuration("2026-10-14T10:00:00Z", Date.parse("2026-10-14T10:18:42Z"))
    ).toBe("18:42")
    expect(
      callDuration("2026-10-14T10:00:00Z", Date.parse("2026-10-14T11:02:05Z"))
    ).toBe("1:02:05")
    expect(whoIsIn([])).toBe("Nobody is in yet")
    expect(whoIsIn(["Raya"])).toBe("Raya is in")
    expect(whoIsIn(["Raya", "Rohan"])).toBe("Raya and Rohan are in")
    expect(whoIsIn(["Raya", "Rohan", "Kenji", "Krude"])).toBe(
      "Raya, Rohan and 2 others are in"
    )
  })

  it("reads voice frames and ignores the rest", () => {
    expect(
      parseVoiceFrame('{"type":"voice.state","present":true,"name":"Krude"}')
    ).toEqual({
      type: "voice.state",
      present: true,
      name: "Krude",
    })
    expect(parseVoiceFrame('{"type":"event"}')).toBeUndefined()
    expect(parseVoiceFrame("nope")).toBeUndefined()
  })

  it("gives each person the same colour every time", () => {
    expect(tintOf("usr_raya")).toBe(tintOf("usr_raya"))
    expect(tintOf("usr_raya")).toMatch(/^var\(--call-tint-[1-6]\)$/)
  })
})

describe("a parley in a teamspace", () => {
  it("shows a running parley and who is in it, and hides Start", async () => {
    renderParley()
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    expect(within(strip).getByText(/^Parley · 1:0\d$/)).toBeTruthy()
    expect(within(strip).getByText("Raya, Rohan and Krude are in")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Parley" })).toBeNull()
  })

  it("offers to start one when none runs", async () => {
    current = null
    const { join } = renderParley()
    const start = await screen.findByRole("button", { name: "Parley" })
    fireEvent.click(start)
    await waitFor(() => expect(join).toHaveBeenCalled())
    expect(join).toHaveBeenCalledWith({
      token: "rtk-token",
      mic: true,
      camera: false,
    })
  })

  it("joins with the mic and camera as set, then follows the member as a mini player", async () => {
    const { join, leaveRoomPage } = renderParley()
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(
      within(strip).getByRole("button", { name: "Join with your mic on" })
    )
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))

    await waitFor(() =>
      expect(join).toHaveBeenCalledWith({
        token: "rtk-token",
        mic: false,
        camera: false,
      })
    )
    const [, init] = requests("/call/join", "POST")[0] ?? []
    expect(JSON.parse(String(init?.body))).toEqual({ name: "Ada" })

    expect(await screen.findByText(/^You're in the parley/)).toBeTruthy()
    // On the room's own page the strip is the call's control.
    expect(screen.queryByRole("region", { name: /mini player/ })).toBeNull()
    leaveRoomPage()
    const pip = screen.getByRole("region", {
      name: "EU launch parley, mini player",
    })
    expect(within(pip).getByRole("group", { name: "Raya Patel" })).toBeTruthy()
  })

  it("opens the full page with its controls, and leaves", async () => {
    const media = fakeMedia()
    renderParley(vi.fn(async () => media))
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    fireEvent.click(await screen.findByRole("button", { name: "Open" }))

    const page = screen.getByRole("dialog", { name: "EU launch parley" })
    const dock = within(page).getByRole("toolbar", { name: "Parley controls" })
    fireEvent.click(within(dock).getByRole("button", { name: "Mute" }))
    await waitFor(() => expect(media.setMic).toHaveBeenCalledWith(false))
    expect(within(dock).getByRole("button", { name: "Unmute" })).toBeTruthy()
    expect(
      within(page).getByRole("group", { name: "Krude, the agent" })
    ).toBeTruthy()

    fireEvent.click(within(dock).getByRole("button", { name: "Leave" }))
    await waitFor(() => expect(requests("/call/leave", "POST")).toHaveLength(1))
    expect(media.leave).toHaveBeenCalled()
    expect(screen.queryByRole("dialog")).toBeNull()
    expect(screen.queryByRole("region", { name: /mini player/ })).toBeNull()
  })

  it("captions everyone and sends only the member's own words to the room", async () => {
    const media = fakeMedia()
    renderParley(vi.fn(async () => media))
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    await screen.findByText(/^You're in the parley/)

    media.say({
      id: "s1",
      member: "usr_raya",
      name: "Raya Patel",
      text: "Then 4 November.",
      partial: false,
      at: 1,
    })
    media.say({
      id: "s2",
      member: "usr_ada",
      name: "Ada",
      text: "Agreed.",
      partial: false,
      at: 2,
    })

    await waitFor(
      () => expect(requests("/call/transcript", "POST")).toHaveLength(1),
      {
        timeout: 3000,
      }
    )
    const [, init] = requests("/call/transcript", "POST")[0] ?? []
    expect(JSON.parse(String(init?.body))).toEqual({
      callId: "call_1",
      segments: [
        {
          id: "s2",
          speaker: "usr_ada",
          text: "Agreed.",
          partial: false,
          at: 2,
        },
      ],
    })
  })

  it("says why a join was refused", async () => {
    fetchMock.mockImplementation(async (url, init) => {
      const target = String(url)
      if (target.endsWith("/call/join")) {
        return json(
          {
            error: { code: "forbidden", message: "not a member of this room" },
          },
          403
        )
      }
      if (
        target.endsWith(`${base}/call`) &&
        (init?.method ?? "GET") === "GET"
      ) {
        return json({ call: current })
      }
      return json({ events: [], next: 0, more: false })
    })
    const { join } = renderParley()
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    expect((await screen.findByRole("alert")).textContent).toBe(
      "You can't join a parley in this room yet."
    )
    expect(join).not.toHaveBeenCalled()
  })

  it("gives the seat back when the member leaves while still joining", async () => {
    const media = fakeMedia()
    let joined: (value: CallMedia) => void = () => undefined
    const { leaveRoomPage } = renderParley(
      vi.fn(
        () =>
          new Promise<CallMedia>((resolve) => {
            joined = resolve
          })
      )
    )
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    await waitFor(() => expect(requests("/call/join", "POST")).toHaveLength(1))
    leaveRoomPage()
    const pip = await screen.findByRole("region", { name: /mini player/ })
    fireEvent.click(within(pip).getByRole("button", { name: "Leave" }))
    await waitFor(() =>
      expect(screen.queryByRole("region", { name: /mini player/ })).toBeNull()
    )

    joined(media)
    await waitFor(() => expect(media.leave).toHaveBeenCalled())
    await waitFor(() => expect(requests("/call/leave", "POST")).toHaveLength(1))
    expect(screen.queryByRole("region", { name: /mini player/ })).toBeNull()
  })

  it("gives the seat back when the media can't start", async () => {
    renderParley(
      vi.fn(async () => {
        throw new DOMException("Permission denied", "NotAllowedError")
      })
    )
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    expect((await screen.findByRole("alert")).textContent).toBe(
      "Allow the microphone and camera to join."
    )
    await waitFor(() => expect(requests("/call/leave", "POST")).toHaveLength(1))
  })

  it("lets go when someone else ends the call", async () => {
    const media = fakeMedia()
    renderParley(vi.fn(async () => media))
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    await screen.findByText(/^You're in the parley/)

    current = null
    socketFor("/live")?.frame({
      type: "event",
      event: {
        seq: 9,
        at: new Date().toISOString(),
        org: "org_1",
        room,
        actor: { type: "member", id: "usr_raya" },
        run: null,
        type: "call.ended",
        summary: "Raya ended the parley",
        data: {},
        durationMs: 0,
        private: false,
      },
    })
    await waitFor(() => expect(media.leave).toHaveBeenCalled())
    expect(screen.queryByText(/^You're in the parley/)).toBeNull()
    // The call is over: there is no seat to give back.
    expect(requests("/call/leave", "POST")).toHaveLength(0)
  })

  it("lets go when the agent's voice says the call ended", async () => {
    const media = fakeMedia()
    renderParley(vi.fn(async () => media))
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    await screen.findByText(/^You're in the parley/)
    socketFor("/voice/live")?.frame({
      type: "voice.stop",
      utteranceId: "u1",
      reason: "call_ended",
    })
    await waitFor(() => expect(media.leave).toHaveBeenCalled())
    expect(screen.queryByText(/^You're in the parley/)).toBeNull()
  })

  it("leaves with a bodiless beacon when the tab closes", async () => {
    const beacon = vi.fn(() => true)
    Object.defineProperty(navigator, "sendBeacon", {
      value: beacon,
      configurable: true,
    })
    renderParley()
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    await screen.findByText(/^You're in the parley/)
    window.dispatchEvent(new Event("pagehide"))
    expect(beacon).toHaveBeenCalledWith(`${base}/call/leave`)
  })

  it("names the member with their own name when the page names none", async () => {
    current = null
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <CallProvider
          viewerName="Ada Brennan"
          joinMediaWith={async () => fakeMedia()}
        >
          <ParleyButton
            organizationId="org_1"
            room={room}
            roomName="Raya Patel"
            member="usr_ada"
          />
        </CallProvider>
      </QueryClientProvider>
    )
    fireEvent.click(await screen.findByRole("button", { name: "Parley" }))
    await waitFor(() => expect(requests("/call/join", "POST")).toHaveLength(1))
    const [, init] = requests("/call/join", "POST")[0] ?? []
    expect(JSON.parse(String(init?.body))).toEqual({ name: "Ada Brennan" })
  })

  it("says the parley ended, how long and who was in it, when someone else ends it", async () => {
    const media = fakeMedia()
    renderParley(vi.fn(async () => media))
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    await screen.findByText(/^You're in the parley/)
    // The view after joining is the one the summary remembers.
    await waitFor(() => expect(requests(`${base}/call`)).not.toHaveLength(0))

    socketFor("/voice/live")?.frame({
      type: "voice.stop",
      utteranceId: "u1",
      reason: "call_ended",
    })
    const ended = await screen.findByRole("dialog", {
      name: "What's kept from EU launch",
    })
    expect(
      within(ended).getByText("Parley ended · 1 min · 2 people")
    ).toBeTruthy()
    expect(
      within(ended).getByText("The transcript, kept with EU launch.")
    ).toBeTruthy()
    // The library's dialog traps focus; it starts on Done.
    const done = within(ended).getByRole("button", { name: "Done" })
    await waitFor(() => expect(document.activeElement).toBe(done))
    expect(within(ended).getByRole("button", { name: "Close" })).toBeTruthy()
    fireEvent.click(done)
    expect(
      screen.queryByRole("dialog", { name: "What's kept from EU launch" })
    ).toBeNull()
  })

  it("says so after ending it for everyone, but not after just leaving", async () => {
    renderParley()
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    fireEvent.click(await screen.findByRole("button", { name: "Open" }))
    const page = screen.getByRole("dialog", { name: "EU launch parley" })
    fireEvent.click(
      within(page).getByRole("button", { name: /End for everyone/ })
    )
    expect(
      await screen.findByRole("dialog", { name: "What's kept from EU launch" })
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Done" }))

    const again = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(again).getByRole("button", { name: "Join" }))
    fireEvent.click(await screen.findByRole("button", { name: "Leave" }))
    await waitFor(() => expect(requests("/call/leave", "POST")).toHaveLength(1))
    expect(
      screen.queryByRole("dialog", { name: "What's kept from EU launch" })
    ).toBeNull()
  })
  it("lets the member choose their microphone, camera and speaker", async () => {
    const media = fakeMedia()
    renderParley(vi.fn(async () => media))
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    fireEvent.click(await screen.findByRole("button", { name: "Open" }))
    const page = screen.getByRole("dialog", { name: "EU launch parley" })
    fireEvent.click(within(page).getByRole("button", { name: "Devices" }))

    const picker = await within(page).findByRole("dialog", { name: "Devices" })
    const microphone = await within(picker).findByRole("combobox", {
      name: "Microphone",
    })
    expect(within(picker).getByDisplayValue("MacBook mic")).toBe(microphone)
    expect(
      within(picker).getByRole("combobox", { name: "Camera" })
    ).toBeTruthy()
    expect(
      within(picker).getByRole("combobox", { name: "Speaker" })
    ).toBeTruthy()

    fireEvent.change(microphone, { target: { value: "mic-2" } })
    await waitFor(() =>
      expect(media.setDevice).toHaveBeenCalledWith("audioinput", "mic-2")
    )

    // The speaker called "default" is chosen as a speaker, never as a mic.
    fireEvent.change(
      within(picker).getByRole("combobox", { name: "Speaker" }),
      {
        target: { value: "default" },
      }
    )
    await waitFor(() =>
      expect(media.setDevice).toHaveBeenCalledWith("audiooutput", "default")
    )

    // Captions arriving don't make the picker read the devices again.
    const reads = vi.mocked(media.devices).mock.calls.length
    media.say({
      id: "c1",
      member: "usr_raya",
      name: "Raya Patel",
      text: "Hi",
      partial: false,
      at: 1,
    })
    media.say({
      id: "c2",
      member: "usr_raya",
      name: "Raya Patel",
      text: "Hi all",
      partial: false,
      at: 2,
    })
    await screen.findAllByText("Hi all")
    expect(vi.mocked(media.devices).mock.calls.length).toBe(reads)
  })
  it("shows the room's conversation beside the call, moved from the room's page", async () => {
    renderParley()
    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    fireEvent.click(await screen.findByRole("button", { name: "Open" }))
    const page = screen.getByRole("dialog", { name: "EU launch parley" })
    fireEvent.click(within(page).getByRole("button", { name: "Show the room" }))
    const pane = within(page).getByRole("region", { name: "EU launch room" })
    // No room page here has moved its conversation in, so the pane says where it is.
    expect(
      within(pane).getByText(/Open EU launch's page to talk in its room/)
    ).toBeTruthy()
    expect(
      within(page).queryByRole("region", { name: "Transcript" })
    ).toBeNull()
    fireEvent.click(
      within(page).getByRole("button", { name: "Show the transcript" })
    )
    expect(
      within(page).getByRole("region", { name: "Transcript" })
    ).toBeTruthy()
    expect(
      within(page).queryByRole("region", { name: "EU launch room" })
    ).toBeNull()
  })

  it("moves the room page's own conversation into the pane, and back, without remounting it", async () => {
    let mounts = 0
    function Conversation() {
      useEffect(() => {
        mounts += 1
      }, [])
      return <input aria-label="Room draft" defaultValue="" />
    }
    function RoomPage() {
      const chat = useRoomChatHost("org_1", room)
      return (
        <>
          <div ref={chat.pageRef} />
          {chat.render(<Conversation />)}
        </>
      )
    }
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <CallProvider joinMediaWith={async () => fakeMedia()}>
          <RoomPage />
          <Parley onRoomPage />
        </CallProvider>
      </QueryClientProvider>
    )
    const draft = await screen.findByRole("textbox", { name: "Room draft" })
    fireEvent.change(draft, { target: { value: "Half a thought" } })

    const strip = await screen.findByRole("region", {
      name: "Parley in progress",
    })
    fireEvent.click(within(strip).getByRole("button", { name: "Join" }))
    fireEvent.click(await screen.findByRole("button", { name: "Open" }))
    const page = screen.getByRole("dialog", { name: "EU launch parley" })
    fireEvent.click(within(page).getByRole("button", { name: "Show the room" }))
    const pane = within(page).getByRole("region", { name: "EU launch room" })
    await waitFor(() =>
      expect(within(pane).getByRole("textbox", { name: "Room draft" })).toBe(
        draft
      )
    )
    expect((draft as HTMLInputElement).value).toBe("Half a thought")
    expect(within(pane).queryByText(/Open EU launch's page/)).toBeNull()

    fireEvent.click(within(page).getByRole("button", { name: "Hide the room" }))
    await waitFor(() => expect(within(page).queryByRole("textbox")).toBeNull())
    expect(screen.getByRole("textbox", { name: "Room draft" })).toBe(draft)
    expect(mounts).toBe(1)
  })
})

describe("the agent panel's parley chip", () => {
  function renderChips(teamspace?: { id: string; name: string }) {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <CallProvider joinMediaWith={vi.fn(async () => fakeMedia())}>
          <AgentPanelChips
            organizationId="org_1"
            viewerId="usr_ada"
            teamspace={teamspace}
          />
        </CallProvider>
      </QueryClientProvider>
    )
  }

  it("offers to join the teamspace's parley, then says the member is in it", async () => {
    renderChips({ id: "tsp_1", name: "EU launch" })
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Join the parley in EU launch",
      })
    )
    expect(
      await screen.findByRole("button", { name: /In the parley · Open/ })
    ).toBeTruthy()
    expect(requests("/call/join", "POST")).toHaveLength(1)
  })

  it("shows nothing where no parley runs, or off a teamspace", async () => {
    current = null
    renderChips({ id: "tsp_1", name: "EU launch" })
    await waitFor(() => expect(requests(`${base}/call`)).not.toHaveLength(0))
    expect(screen.queryByRole("group", { name: "For you now" })).toBeNull()
    cleanup()
    current = view()
    renderChips()
    expect(screen.queryByRole("group", { name: "For you now" })).toBeNull()
  })
})
