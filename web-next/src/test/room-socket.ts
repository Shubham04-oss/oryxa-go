// A stand-in for the Agents SDK's `useAgent`, for the tests of the pages on
// realtime objects (the primary agent's chat): each mount gets a socket the
// test opens, closes and reads what the page said on, as the Durable Object
// would. Use it with
// `vi.mock("agents/react", async () => (await import("@/test/room-socket")).agentsReact)`,
// and, for a chat, src/test/agent-chat.ts for `useAgentChat`.
import { act } from "@testing-library/react"
import { useRef } from "react"

type Options = {
  agent: string
  name?: string
  enabled?: boolean
  onOpen?: (event: Event) => void
  onClose?: (event: { code: number; target: unknown }) => void
}

export class FakeRoomSocket {
  options!: Options
  readonly OPEN = 1
  readyState = 0
  /** What the page said, oldest first, whether or not the socket was open (PartySocket queues it). */
  readonly sent: unknown[] = []
  reconnects = 0
  private readonly openListeners = new Set<() => void>()

  send(data: string) {
    this.sent.push(JSON.parse(data))
  }

  addEventListener(type: string, listener: () => void) {
    if (type === "open") this.openListeners.add(listener)
  }

  removeEventListener(type: string, listener: () => void) {
    if (type === "open") this.openListeners.delete(listener)
  }

  /** As PartySocket's: closes this connection (code 1000) and opens a new one. */
  reconnect() {
    this.reconnects += 1
    setTimeout(() => {
      act(() => {
        this.readyState = 3
        this.options.onClose?.({ code: 1000, target: this })
      })
      this.open()
    })
  }

  close() {
    this.readyState = 3
  }

  /** The socket opens. */
  open() {
    act(() => {
      this.readyState = 1
      this.options.onOpen?.(new Event("open"))
      for (const listener of [...this.openListeners]) listener()
    })
  }
}

export const roomSockets = {
  all: [] as FakeRoomSocket[],
  get latest(): FakeRoomSocket {
    const socket = this.all.at(-1)
    if (!socket) throw new Error("no room socket was asked for")
    return socket
  },
  /** Every socket on the object with this name, oldest first. */
  named(name: string): FakeRoomSocket[] {
    return this.all.filter((socket) => socket.options.name === name)
  },
  reset() {
    this.all.length = 0
  },
}

function useAgent(options: Options): FakeRoomSocket {
  const socket = useRef<FakeRoomSocket>(undefined)
  if (!socket.current) {
    socket.current = new FakeRoomSocket()
    roomSockets.all.push(socket.current)
  }
  socket.current.options = options
  return socket.current
}

export const agentsReact = { useAgent }
