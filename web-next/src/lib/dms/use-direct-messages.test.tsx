// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { DM_SEND_TIMEOUT_MS, useDirectMessages } from "./use-direct-messages"
import {
  DM_MAX_BODY_LENGTH,
  DM_TYPING_REFRESH_MS,
  DM_TYPING_TTL_MS,
  conversationGoneCloseCode,
} from "./protocol"
import type { DmServerMessage, DmWireMessage } from "./protocol"
import { roomSockets } from "@/test/room-socket"
import type { FakeRoomSocket } from "@/test/room-socket"
import { apiError, json, mockApi } from "@/test/teamspaces"

vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)

const org = "org_sim"
const viewer = "user-member"
const peer = "user-admin"
const name = "org_sim~user-admin~user-member"
const path = `/agents/direct-message-agent/${name}`

function wire(
  seq: number,
  authorId = peer,
  extra: Partial<DmWireMessage> = {}
): DmWireMessage {
  return {
    id: `dmm_${seq}`,
    seq,
    authorId,
    body: `line ${seq}`,
    createdAt: `2026-09-24T09:00:${String(seq % 60).padStart(2, "0")}.000Z`,
    ...extra,
  }
}

function snapshot(
  messages: DmWireMessage[] = [],
  overrides: Partial<Extract<DmServerMessage, { type: "dm-snapshot" }>> = {}
): DmServerMessage {
  return {
    type: "dm-snapshot",
    conversation: "dmc_1",
    messages,
    hasOlder: false,
    reads: [
      { userId: peer, seq: 0 },
      { userId: viewer, seq: 0 },
    ],
    online: [],
    typing: [],
    ...overrides,
  }
}

function setup(
  ids: [string | undefined, string | undefined, string | undefined] = [
    org,
    viewer,
    peer,
  ]
) {
  return renderHook(
    ({ current }: { current: typeof ids }) => useDirectMessages(...current),
    { initialProps: { current: ids } }
  )
}

/** The DM as its object sends it when the socket opens. */
function open(messages: DmWireMessage[] = [], overrides = {}): FakeRoomSocket {
  const socket = roomSockets.latest
  socket.open()
  socket.says(snapshot(messages, overrides))
  return socket
}

let visibility: DocumentVisibilityState = "visible"

function setVisibility(next: DocumentVisibilityState) {
  visibility = next
  act(() => {
    document.dispatchEvent(new Event("visibilitychange"))
  })
}

beforeEach(() => {
  roomSockets.reset()
  visibility = "visible"
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => visibility,
  })
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("useDirectMessages: opening", () => {
  it("opens nothing until it knows the organization and two different people", () => {
    for (const ids of [
      [undefined, viewer, peer],
      [org, undefined, peer],
      [org, viewer, undefined],
      [org, viewer, viewer],
      [org, "user member", peer],
    ] as Array<[string | undefined, string | undefined, string | undefined]>) {
      const { result } = setup(ids)
      expect(result.current.status).toBe("closed")
      expect(result.current.messages).toEqual([])
    }
    expect(roomSockets.all.every((socket) => !socket.options.enabled)).toBe(
      true
    )
  })

  it("opens the DM's object, says whether the tab is visible, and is live once the conversation arrives", () => {
    const { result } = setup()
    const socket = roomSockets.latest
    expect(socket.options).toMatchObject({
      agent: "DirectMessageAgent",
      name,
      enabled: true,
    })
    expect(result.current.status).toBe("connecting")

    socket.open()
    expect(socket.sent).toEqual([{ type: "dm-visibility", visible: true }])
    socket.says(
      snapshot([wire(1), wire(2, viewer, { clientId: "dmc-old" })], {
        reads: [
          { userId: peer, seq: 2 },
          { userId: viewer, seq: 1 },
        ],
        online: [peer],
        typing: [peer],
        hasOlder: true,
      })
    )
    expect(result.current.status).toBe("live")
    expect(result.current.messages).toEqual([
      wire(1),
      wire(2, viewer, { clientId: "dmc-old" }),
    ])
    expect(result.current.hasOlder).toBe(true)
    expect(result.current.peer).toEqual({
      online: true,
      typing: true,
      lastReadSeq: 2,
    })
  })

  it("keeps each view's own socket: two views of one DM on a page both stay current", () => {
    const panel = setup()
    const canvas = setup()
    const sockets = roomSockets.named(name)
    expect(sockets).toHaveLength(2)
    for (const socket of sockets) {
      socket.open()
      socket.says(snapshot([wire(1)]))
    }

    act(() => panel.result.current.send("From the panel"))
    const [sent] = sockets[0].said("dm-send")
    expect(sockets[1].said("dm-send")).toEqual([])
    const stored = wire(2, viewer, {
      body: "From the panel",
      clientId: sent.clientId,
    })
    for (const socket of sockets)
      socket.says({ type: "dm-message", message: stored })

    expect(panel.result.current.messages.at(-1)).toEqual(stored)
    expect(canvas.result.current.messages.at(-1)).toEqual(stored)
    expect(canvas.result.current.messages).toHaveLength(2)
  })
})

describe("useDirectMessages: a view that moves to another DM", () => {
  it("starts over, and ignores what the socket it replaced still says", () => {
    const { result, rerender } = setup()
    const socket = open([wire(1)])
    act(() => result.current.send("For the admin"))

    rerender({ current: [org, viewer, "user-owner"] })
    expect(socket.options.name).toBe("org_sim~user-member~user-owner")
    expect(result.current.messages).toEqual([])
    expect(result.current.status).toBe("connecting")

    // Until the SDK swaps sockets, the previous one's frames and close
    // still reach this view's handlers: its room tells them apart.
    const previous = { room: name }
    act(() => {
      socket.options.onMessage?.({
        data: JSON.stringify({ type: "dm-message", message: wire(7) }),
        target: previous,
      } as unknown as { data: string })
      socket.options.onClose?.({ code: 1000, target: previous })
    })
    expect(result.current.messages).toEqual([])
    expect(result.current.status).toBe("connecting")
    // The new socket's own frames do count.
    act(() => {
      socket.options.onMessage?.({
        data: JSON.stringify(snapshot([wire(2, "user-owner")])),
        target: { room: "org_sim~user-member~user-owner" },
      } as unknown as { data: string })
    })
    expect(result.current.status).toBe("live")
    expect(result.current.messages).toEqual([wire(2, "user-owner")])
  })
})

describe("useDirectMessages: a message confirmed long after", () => {
  it("does not splice an old message into what it holds, and leaves it to history", () => {
    const { result } = setup()
    const socket = open()
    act(() => result.current.send("Sent before a long drop"))
    const clientId = result.current.messages[0].clientId
    socket.drop()
    socket.open()
    // Fifty and more messages later, the snapshot no longer has it; sent
    // again, the object answers with the stored one.
    socket.says(
      snapshot(
        Array.from({ length: 50 }, (_, index) => wire(61 + index)),
        { hasOlder: true }
      )
    )
    socket.says({
      type: "dm-message",
      message: wire(9, viewer, { body: "Sent before a long drop", clientId }),
    })
    expect(result.current.messages.map((message) => message.seq)).toEqual(
      Array.from({ length: 50 }, (_, index) => 61 + index)
    )
    expect(result.current.hasOlder).toBe(true)
    act(() => {
      void result.current.loadOlder()
    })
    expect(socket.said("dm-history").at(-1)?.beforeSeq).toBe(61)
  })
})

describe("useDirectMessages: sending", () => {
  it("shows a message at once as sending, and as a normal message once the object confirms it", () => {
    const { result } = setup()
    const socket = open([wire(1)])

    act(() => result.current.send("  Morning!  "))
    const pending = result.current.messages.at(-1)
    expect(pending).toEqual({
      id: expect.stringMatching(/^dmc-[0-9a-f]{24}$/) as unknown,
      seq: 0,
      authorId: viewer,
      body: "Morning!",
      createdAt: expect.any(String) as unknown,
      clientId: pending?.id,
      status: "sending",
    })
    expect(socket.said("dm-send")).toEqual([
      { type: "dm-send", clientId: pending?.clientId, body: "Morning!" },
    ])

    const stored = wire(2, viewer, {
      body: "Morning!",
      clientId: pending?.clientId,
    })
    socket.says({ type: "dm-message", message: stored })
    expect(result.current.messages).toEqual([wire(1), stored])
  })

  it("drops an empty message, and fails an over-long one without sending it", () => {
    const { result } = setup()
    const socket = open()
    act(() => result.current.send("   \n "))
    expect(result.current.messages).toEqual([])
    act(() => result.current.send("x".repeat(DM_MAX_BODY_LENGTH + 1)))
    expect(result.current.messages.map((message) => message.status)).toEqual([
      "failed",
    ])
    act(() => result.current.retry(result.current.messages[0].clientId ?? ""))
    expect(result.current.messages[0].status).toBe("failed")
    expect(socket.said("dm-send")).toEqual([])
  })

  it("marks a refused message failed, and retry sends it again under the same id", () => {
    const { result } = setup()
    const socket = open()
    act(() => result.current.send("Too quick"))
    const clientId = result.current.messages[0].clientId ?? ""
    socket.says({
      type: "dm-send-failed",
      clientId,
      code: "rate_limited",
      message: "You're sending messages too quickly. Wait a moment.",
    })
    expect(result.current.messages[0].status).toBe("failed")

    act(() => result.current.retry(clientId))
    expect(result.current.messages[0].status).toBe("sending")
    expect(socket.said("dm-send")).toEqual([
      { type: "dm-send", clientId, body: "Too quick" },
      { type: "dm-send", clientId, body: "Too quick" },
    ])
  })

  it("fails a message the object never answered, and takes it back if it arrived after all", () => {
    vi.useFakeTimers()
    const { result } = setup()
    const socket = open()
    act(() => result.current.send("Hello?"))
    const clientId = result.current.messages[0].clientId
    act(() => vi.advanceTimersByTime(DM_SEND_TIMEOUT_MS))
    expect(result.current.messages[0].status).toBe("failed")

    socket.says({
      type: "dm-message",
      message: wire(1, viewer, { body: "Hello?", clientId }),
    })
    expect(result.current.messages).toEqual([
      wire(1, viewer, { body: "Hello?", clientId }),
    ])
  })

  it("holds what is sent before the conversation arrives, and sends again what a reopened socket's snapshot lacks", () => {
    const { result } = setup()
    act(() => result.current.send("Early"))
    const socket = roomSockets.latest
    expect(socket.said("dm-send")).toEqual([])
    socket.open()
    socket.says(snapshot())
    const early = socket.said("dm-send")
    expect(early).toEqual([
      {
        type: "dm-send",
        clientId: expect.any(String) as unknown,
        body: "Early",
      },
    ])

    // The socket drops before the object answered; the reopened socket's
    // snapshot does not have it, so it goes again under the same id.
    socket.drop()
    expect(result.current.status).toBe("error")
    act(() => result.current.send("While away"))
    socket.open()
    socket.says(snapshot())
    expect(socket.said("dm-send").slice(1)).toEqual([
      { type: "dm-send", clientId: early[0].clientId, body: "Early" },
      {
        type: "dm-send",
        clientId: expect.any(String) as unknown,
        body: "While away",
      },
    ])

    // A snapshot that has one confirms it.
    socket.drop()
    socket.open()
    socket.says(
      snapshot([
        wire(1, viewer, { body: "Early", clientId: early[0].clientId }),
      ])
    )
    expect(
      result.current.messages.map((message) => [message.body, message.status])
    ).toEqual([
      ["Early", undefined],
      ["While away", "sending"],
    ])
  })
})

describe("useDirectMessages: history", () => {
  it("loads the page before the oldest message held", async () => {
    const { result } = setup()
    const page = Array.from({ length: 50 }, (_, index) => wire(51 + index))
    const socket = open(page, { hasOlder: true })

    let loaded!: Promise<void>
    act(() => {
      loaded = result.current.loadOlder()
    })
    const [asked] = socket.said("dm-history")
    expect(asked).toEqual({
      type: "dm-history",
      requestId: expect.stringMatching(/^older-/) as unknown,
      beforeSeq: 51,
      limit: 50,
    })
    // Asking again while it is out waits for the same page.
    let again!: Promise<void>
    act(() => {
      again = result.current.loadOlder()
    })
    expect(socket.said("dm-history")).toHaveLength(1)

    socket.says({
      type: "dm-history",
      requestId: asked.requestId,
      messages: Array.from({ length: 50 }, (_, index) => wire(1 + index)),
      hasOlder: false,
    })
    await loaded
    await again
    expect(result.current.messages.map((message) => message.seq)).toEqual(
      Array.from({ length: 100 }, (_, index) => index + 1)
    )
    expect(result.current.hasOlder).toBe(false)
    await result.current.loadOlder()
    expect(socket.said("dm-history")).toHaveLength(1)
  })

  it("joins a reopened socket's snapshot to what it held, and starts over for a new run of the conversation", () => {
    const { result } = setup()
    const socket = open([wire(1), wire(2)])
    socket.drop()
    socket.open()
    socket.says(snapshot([wire(2), wire(3)]))
    expect(result.current.messages.map((message) => message.seq)).toEqual([
      1, 2, 3,
    ])

    // So much later that messages would be missing between them.
    socket.drop()
    socket.open()
    socket.says(snapshot([wire(90), wire(91)], { hasOlder: true }))
    expect(result.current.messages.map((message) => message.seq)).toEqual([
      90, 91,
    ])
    expect(result.current.hasOlder).toBe(true)

    // A purge and a new start: the same seqs are another conversation.
    socket.drop()
    socket.open()
    socket.says(
      snapshot([wire(1, peer, { id: "dmm_new", body: "Hello again" })], {
        conversation: "dmc_2",
      })
    )
    expect(result.current.messages).toEqual([
      wire(1, peer, { id: "dmm_new", body: "Hello again" }),
    ])
  })
})

describe("useDirectMessages: read state, typing and presence", () => {
  it("reads through the latest message once, and only while the tab is visible", () => {
    const { result } = setup()
    const socket = open([wire(1), wire(2)])

    act(() => result.current.markRead())
    act(() => result.current.markRead())
    expect(socket.said("dm-read")).toEqual([{ type: "dm-read", seq: 2 }])
    socket.says({ type: "dm-read", userId: viewer, seq: 2 })

    socket.says({ type: "dm-message", message: wire(3) })
    setVisibility("hidden")
    act(() => result.current.markRead())
    expect(socket.said("dm-read")).toHaveLength(1)
    setVisibility("visible")
    act(() => result.current.markRead())
    expect(socket.said("dm-read")).toEqual([
      { type: "dm-read", seq: 2 },
      { type: "dm-read", seq: 3 },
    ])
    // The viewer's own reads never show as the peer's.
    expect(result.current.peer.lastReadSeq).toBe(0)
    socket.says({ type: "dm-read", userId: peer, seq: 3 })
    expect(result.current.peer.lastReadSeq).toBe(3)
  })

  it("says the viewer is typing, again every few seconds while they are, and when they stop or hide the tab", () => {
    vi.useFakeTimers()
    const { result } = setup()
    const socket = open()

    act(() => result.current.setTyping(true))
    act(() => vi.advanceTimersByTime(DM_TYPING_REFRESH_MS - 1))
    act(() => result.current.setTyping(true))
    act(() => vi.advanceTimersByTime(1))
    act(() => result.current.setTyping(true))
    act(() => result.current.setTyping(false))
    act(() => result.current.setTyping(false))
    act(() => result.current.setTyping(true))
    setVisibility("hidden")
    expect(socket.said("dm-typing").map((message) => message.typing)).toEqual([
      true,
      true,
      false,
      true,
      false,
    ])
    expect(socket.said("dm-visibility").at(-1)).toEqual({
      type: "dm-visibility",
      visible: false,
    })
  })

  it("shows the other person typing until they stop or it lapses, and whether they are online", () => {
    vi.useFakeTimers()
    const { result } = setup()
    const socket = open()

    socket.says({ type: "dm-typing", userId: peer, typing: true })
    expect(result.current.peer.typing).toBe(true)
    socket.says({ type: "dm-typing", userId: peer, typing: false })
    expect(result.current.peer.typing).toBe(false)
    socket.says({ type: "dm-typing", userId: peer, typing: true })
    act(() => vi.advanceTimersByTime(DM_TYPING_TTL_MS + 1_000))
    expect(result.current.peer.typing).toBe(false)
    // Only the other person's typing shows.
    socket.says({ type: "dm-typing", userId: viewer, typing: true })
    expect(result.current.peer.typing).toBe(false)

    // The same peer, the same object: a view's effects need not re-run.
    const before = result.current.peer
    socket.says({ type: "dm-read", userId: viewer, seq: 0 })
    expect(result.current.peer).toBe(before)
    socket.says({ type: "dm-presence", userId: peer, online: true })
    expect(result.current.peer.online).toBe(true)
    socket.says({ type: "dm-presence", userId: peer, online: false })
    expect(result.current.peer.online).toBe(false)
  })
})

describe("useDirectMessages: refusals", () => {
  it("asks the Worker why a socket closed: a peer outside the organization is not allowed, for good", async () => {
    const api = mockApi(({ path: asked }) =>
      asked === path
        ? apiError(
            404,
            "peer_not_found",
            "This person is not in your organization."
          )
        : undefined
    )
    const { result } = setup()
    const socket = roomSockets.latest
    act(() => result.current.send("Hello?"))
    socket.drop()
    await waitFor(() => expect(result.current.status).toBe("not-allowed"))
    expect(api.calls.map((call) => call.path)).toEqual([path])
    expect(socket.options.enabled).toBe(false)
    expect(result.current.messages[0].status).toBe("failed")
    // Nothing more goes out, nor is retried.
    act(() => result.current.retry(result.current.messages[0].clientId ?? ""))
    act(() => result.current.send("Anyone?"))
    expect(result.current.messages).toHaveLength(1)
  })

  it("ends on a session that could not be refreshed", async () => {
    mockApi(({ path: asked }) =>
      asked === path || asked === "/v1/auth/refresh"
        ? apiError(401, "unauthorized", "access token is required")
        : undefined
    )
    const { result } = setup()
    roomSockets.latest.drop()
    await waitFor(() => expect(result.current.status).toBe("session_expired"))
  })

  it("keeps trying when the Worker lets it in or cannot say", async () => {
    for (const answer of [
      json(undefined, 204),
      apiError(503, "unavailable", "down"),
    ]) {
      roomSockets.reset()
      const api = mockApi(() => answer)
      const { result, unmount } = setup()
      roomSockets.latest.drop()
      await waitFor(() => expect(api.calls).toHaveLength(1))
      expect(result.current.status).toBe("error")
      expect(roomSockets.latest.options.enabled).toBe(true)
      unmount()
    }
  })

  it("opens the socket again at once when asked while it is down, and only then", async () => {
    const api = mockApi(({ path: asked }) =>
      asked === path ? json(undefined, 204) : undefined
    )
    const { result } = setup()
    const socket = open([wire(1)])
    act(() => result.current.reconnect())
    expect(socket.reconnects).toBe(0)

    socket.drop()
    await waitFor(() => expect(api.calls).toHaveLength(1))
    expect(result.current.status).toBe("error")
    act(() => result.current.send("While away"))
    act(() => result.current.reconnect())
    expect(socket.reconnects).toBe(1)
    await waitFor(() => expect(socket.readyState).toBe(socket.OPEN))
    socket.says(snapshot([wire(1)]))
    expect(result.current.status).toBe("live")
    // What waited goes on the reopened socket.
    expect(socket.said("dm-send").map((message) => message.body)).toEqual([
      "While away",
    ])

    socket.says({ type: "dm-gone" })
    act(() => result.current.reconnect())
    expect(socket.reconnects).toBe(1)
  })

  it("is not allowed once the conversation was purged, without asking", () => {
    const api = mockApi(() => undefined)
    const { result } = setup()
    const socket = open([wire(1)])
    socket.says({ type: "dm-gone" })
    expect(result.current.status).toBe("not-allowed")
    socket.drop(conversationGoneCloseCode)
    expect(result.current.status).toBe("not-allowed")
    expect(api.calls).toEqual([])
  })
})
