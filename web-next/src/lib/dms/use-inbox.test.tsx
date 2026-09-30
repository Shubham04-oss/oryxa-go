// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, renderHook, waitFor } from "@testing-library/react"
import { useInbox } from "./use-inbox"
import { conversationGoneCloseCode } from "./protocol"
import type { Conversation } from "./types"
import { roomSockets } from "@/test/room-socket"
import { apiError, json, mockApi } from "@/test/teamspaces"

vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)

const org = "org_sim"
const viewer = "user-member"
const name = "org_sim~user-member"
const path = `/agents/inbox-agent/${name}`

function conversation(
  peerId: string,
  updatedAt: string,
  unread = 0
): Conversation {
  const [first, second] = [viewer, peerId].sort()
  return {
    kind: "dm",
    name: `${org}~${first}~${second}`,
    peerId,
    lastMessage: { authorId: peerId, body: "hello", createdAt: updatedAt },
    unread,
    updatedAt,
  }
}

const withAdmin = conversation("user-admin", "2026-09-24T09:00:00.000Z", 2)
const withOwner = conversation("user-owner", "2026-09-24T10:00:00.000Z", 1)

function setup(ids: [string | undefined, string | undefined] = [org, viewer]) {
  return renderHook(() => useInbox(...ids))
}

beforeEach(() => {
  roomSockets.reset()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("useInbox", () => {
  it("opens nothing until it knows the organization and the member", () => {
    for (const ids of [
      [undefined, viewer],
      [org, undefined],
      [org, "user member"],
    ] as Array<[string | undefined, string | undefined]>) {
      expect(setup(ids).result.current).toEqual({
        conversations: [],
        unreadTotal: 0,
        status: "closed",
      })
    }
    expect(roomSockets.all.every((socket) => !socket.options.enabled)).toBe(
      true
    )
  })

  it("opens the member's inbox, and keeps its list current, the latest first", () => {
    const { result } = setup()
    const socket = roomSockets.latest
    expect(socket.options).toMatchObject({
      agent: "InboxAgent",
      name,
      enabled: true,
    })
    expect(result.current.status).toBe("connecting")

    socket.open()
    // The page says nothing on it.
    expect(socket.sent).toEqual([])
    socket.says({
      type: "inbox-snapshot",
      conversations: [withAdmin, withOwner],
      unreadTotal: 3,
    })
    expect(result.current).toEqual({
      conversations: [withOwner, withAdmin],
      unreadTotal: 3,
      status: "live",
    })

    const newer = {
      ...withAdmin,
      unread: 3,
      updatedAt: "2026-09-24T11:00:00.000Z",
    }
    socket.says({
      type: "inbox-conversation",
      conversation: newer,
      unreadTotal: 4,
    })
    expect(result.current.conversations).toEqual([newer, withOwner])
    expect(result.current.unreadTotal).toBe(4)

    socket.says({ type: "inbox-removed", name: withOwner.name, unreadTotal: 3 })
    expect(result.current.conversations).toEqual([newer])
    expect(result.current.unreadTotal).toBe(3)
  })

  it.each([
    {
      state: "the Worker lets it in",
      answer: () => json(undefined, 204),
      expected: "error",
      enabled: true,
    },
    {
      state: "the session could not be refreshed",
      answer: () => apiError(401, "unauthorized", "access token is required"),
      expected: "session_expired",
      enabled: false,
    },
    {
      state: "the organization is not the member's",
      answer: () => apiError(404, "not_found", "resource not found"),
      expected: "error",
      enabled: false,
    },
  ])(
    "after a drop, checks the session when $state",
    async ({ answer, expected, enabled }) => {
      const api = mockApi(({ path: asked }) =>
        asked === path || asked === "/v1/auth/refresh" ? answer() : undefined
      )
      const { result } = setup()
      const socket = roomSockets.latest
      socket.open()
      socket.says({ type: "inbox-snapshot", conversations: [], unreadTotal: 0 })
      socket.drop()
      await waitFor(() => expect(api.calls.length).toBeGreaterThan(0))
      await waitFor(() => expect(result.current.status).toBe(expected))
      expect(socket.options.enabled).toBe(enabled)
    }
  )

  it("after a purge, stops for good when the Worker refuses the member", async () => {
    const api = mockApi(({ path: asked }) =>
      asked === path
        ? apiError(404, "not_found", "resource not found")
        : undefined
    )
    const { result } = setup()
    const socket = roomSockets.latest
    socket.open()
    socket.says({
      type: "inbox-snapshot",
      conversations: [withAdmin],
      unreadTotal: 2,
    })
    socket.says({ type: "inbox-gone" })
    socket.drop(conversationGoneCloseCode)
    await waitFor(() => expect(socket.options.enabled).toBe(false))
    expect(result.current.status).toBe("error")
    expect(api.calls.map((call) => call.path)).toEqual([path])
    expect(socket.reconnects).toBe(0)
  })

  it("after a purge that kept something, reopens and takes the inbox as it now stands", async () => {
    mockApi(({ path: asked }) =>
      asked === path ? json(undefined, 204) : undefined
    )
    const { result } = setup()
    const socket = roomSockets.latest
    socket.open()
    socket.says({
      type: "inbox-snapshot",
      conversations: [withAdmin, withOwner],
      unreadTotal: 3,
    })
    socket.says({ type: "inbox-gone" })
    await waitFor(() => expect(socket.reconnects).toBe(1))
    await waitFor(() => expect(socket.readyState).toBe(1))
    socket.says({
      type: "inbox-snapshot",
      conversations: [withOwner],
      unreadTotal: 1,
    })
    expect(result.current).toEqual({
      conversations: [withOwner],
      unreadTotal: 1,
      status: "live",
    })
  })
})
