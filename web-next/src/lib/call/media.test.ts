// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"

import { joinMedia } from "./media"

const sdk = vi.hoisted(() => {
  const handlers = new Map<string, () => void>()
  const play = vi.fn(async () => {})
  const meeting = {
    join: vi.fn(async () => {}),
    leave: vi.fn(async () => {}),
    audio: { play },
    self: {
      id: "self",
      name: "Me",
      audioEnabled: true,
      videoEnabled: false,
      screenShareEnabled: false,
      on: (event: string, listener: () => void) =>
        handlers.set(event, listener),
    },
    participants: { joined: Object.assign(new Map(), { on: () => {} }) },
    ai: { on: () => {}, off: () => {} },
  }
  const init = vi.fn(
    async (_options: { modules?: Record<string, unknown> }) => meeting
  )
  return { handlers, play, meeting, init }
})

vi.mock("@cloudflare/realtimekit", () => ({
  default: { init: sdk.init },
}))

afterEach(() => {
  sdk.handlers.clear()
  sdk.play.mockClear()
})

describe("joinMedia's audio", () => {
  it("asks the SDK to play everyone else's audio, which it doesn't by default", async () => {
    await joinMedia({ token: "t", mic: true, camera: false })
    expect(sdk.init.mock.calls[0]?.[0].modules).toEqual({
      experimentalAudioPlayback: true,
    })
  })

  it("plays the call again at the member's next click once the browser refused it", async () => {
    await joinMedia({ token: "t", mic: true, camera: false })
    document.dispatchEvent(new Event("pointerdown"))
    expect(sdk.play).not.toHaveBeenCalled()

    sdk.handlers.get("autoplayError")?.()
    document.dispatchEvent(new Event("pointerdown"))
    expect(sdk.play).toHaveBeenCalledTimes(1)

    // Once is enough: the next click doesn't play again.
    document.dispatchEvent(new Event("keydown"))
    expect(sdk.play).toHaveBeenCalledTimes(1)
  })

  it("stops waiting for a click once the member leaves", async () => {
    const media = await joinMedia({ token: "t", mic: true, camera: false })
    sdk.handlers.get("autoplayError")?.()
    await media.leave()
    document.dispatchEvent(new Event("pointerdown"))
    expect(sdk.play).not.toHaveBeenCalled()
  })
})
