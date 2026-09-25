import { afterEach, describe, expect, it, vi } from "vitest"
import { reopenSocket } from "./agent-socket"

class FakeSocket extends EventTarget {
  reconnects = 0
  listeners = 0

  override addEventListener(
    type: string,
    listener: EventListenerOrEventListenerObject | null
  ) {
    this.listeners += 1
    super.addEventListener(type, listener)
  }

  override removeEventListener(
    type: string,
    listener: EventListenerOrEventListenerObject | null
  ) {
    this.listeners -= 1
    super.removeEventListener(type, listener)
  }

  reconnect() {
    this.reconnects += 1
  }

  open() {
    this.dispatchEvent(new Event("open"))
  }
}

describe("reopenSocket", () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it("resolves once the new socket is open, and stops listening", async () => {
    const socket = new FakeSocket()
    const reopened = reopenSocket(socket)
    expect(socket.reconnects).toBe(1)
    socket.open()
    await expect(reopened).resolves.toBeUndefined()
    expect(socket.listeners).toBe(0)
  })

  it("gives up on a socket that does not open in time", async () => {
    vi.useFakeTimers()
    const socket = new FakeSocket()
    const reopened = reopenSocket(socket, 1000)
    const assertion = expect(reopened).rejects.toThrow("Could not reconnect")
    vi.advanceTimersByTime(1000)
    await assertion
    expect(socket.listeners).toBe(0)
    // A late open after giving up is ignored rather than resolving anything.
    socket.open()
  })

  it("rejects at once, leaving no timer or listener behind, when reconnecting throws", async () => {
    vi.useFakeTimers()
    const socket = new FakeSocket()
    socket.reconnect = () => {
      throw new Error("socket is closed for good")
    }
    await expect(reopenSocket(socket, 1000)).rejects.toThrow(
      "socket is closed for good"
    )
    expect(socket.listeners).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })
})
