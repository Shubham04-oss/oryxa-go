// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import {
  SIDE_SEND_TIMEOUT_MS,
  SideActionError,
  useSideConversation,
} from "./use-side-conversation"
import { SIDE_MAX_BODY_LENGTH, sideGoneCloseCode } from "./protocol"
import type {
  SideRoster,
  SideServerMessage,
  SideWireAgentMessage,
  SideWireItem,
  SideWireMessage,
  SideWireRequestCard,
} from "./protocol"
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
const teamspace = "tsp_launch"
const name = "org_sim~tsp_launch~user-admin~user-member"
const path = `/agents/side-conversation-agent/${name}`

const roster: SideRoster = {
  teamspaceName: "Launch room",
  paused: false,
  people: [
    {
      userId: viewer,
      displayName: "Member",
      agentId: "agt_leo",
      agentName: "Leo",
    },
    {
      userId: peer,
      displayName: "Org Admin",
      agentId: "agt_nia",
      agentName: "Nia",
    },
  ],
  contributions: [
    {
      agentId: "agt_pricing",
      ownerId: peer,
      name: "Pricing",
      role: "Prices things",
      delegationMode: "automatic",
    },
    {
      agentId: "agt_notes",
      ownerId: viewer,
      name: "Notes",
      delegationMode: "request",
    },
  ],
}

function at(seq: number): string {
  return `2026-09-24T09:00:${String(seq % 60).padStart(2, "0")}.000Z`
}

function message(
  seq: number,
  authorId = peer,
  extra: Partial<SideWireMessage> = {}
): SideWireMessage {
  return {
    kind: "message",
    id: `sdi_${seq}`,
    seq,
    authorId,
    body: `line ${seq}`,
    createdAt: at(seq),
    ...extra,
  }
}

function agentMessage(
  seq: number,
  extra: Partial<SideWireAgentMessage> = {}
): SideWireAgentMessage {
  return {
    kind: "agent-message",
    id: `sdi_${seq}`,
    seq,
    agentId: "agt_leo",
    agentName: "Leo",
    ownerId: viewer,
    body: "",
    createdAt: at(seq),
    updatedAt: at(seq),
    status: "streaming",
    ...extra,
  }
}

function card(
  seq: number,
  extra: Partial<SideWireRequestCard> = {}
): SideWireRequestCard {
  return {
    kind: "request-card",
    id: `sdi_${seq}`,
    seq,
    requestId: `irq_${seq}`,
    requesterId: peer,
    requesterAgentId: "agt_nia",
    requesterAgentName: "Nia",
    targetId: viewer,
    targetAgentId: "agt_leo",
    targetAgentName: "Leo",
    approval: "owner",
    status: "pending",
    task: "Send the brief",
    createdAt: at(seq),
    updatedAt: at(seq),
    expiresAt: "2026-09-25T09:00:00.000Z",
    ...extra,
  }
}

function snapshot(
  items: SideWireItem[] = [],
  overrides: Partial<Extract<SideServerMessage, { type: "side-snapshot" }>> = {}
): SideServerMessage {
  return {
    type: "side-snapshot",
    conversation: "sdc_1",
    items,
    hasOlder: false,
    reads: [
      { userId: peer, seq: 0 },
      { userId: viewer, seq: 0 },
    ],
    online: [],
    typing: [],
    roster,
    ...overrides,
  }
}

type Ids = [
  string | undefined,
  string | undefined,
  string | undefined,
  string | undefined,
]

function setup(ids: Ids = [org, viewer, teamspace, peer]) {
  return renderHook(
    ({ current }: { current: Ids }) => useSideConversation(...current),
    { initialProps: { current: ids } }
  )
}

/** The conversation as its object sends it when the socket opens. */
function open(items: SideWireItem[] = [], overrides = {}): FakeRoomSocket {
  const socket = roomSockets.latest
  socket.open()
  socket.says(snapshot(items, overrides))
  return socket
}

let visibility: DocumentVisibilityState = "visible"

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

describe("useSideConversation: opening", () => {
  it("opens nothing until it knows the organization, the teamspace and two different people", () => {
    for (const ids of [
      [undefined, viewer, teamspace, peer],
      [org, undefined, teamspace, peer],
      [org, viewer, undefined, peer],
      [org, viewer, teamspace, undefined],
      [org, viewer, teamspace, viewer],
      [org, viewer, "tsp launch", peer],
    ] as Ids[]) {
      const { result } = setup(ids)
      expect(result.current.status).toBe("closed")
      expect(result.current.items).toEqual([])
    }
    expect(roomSockets.all.every((socket) => !socket.options.enabled)).toBe(
      true
    )
  })

  it("opens the side conversation's object, says whether the tab is visible, and is live with who is in it", () => {
    const { result } = setup()
    const socket = roomSockets.latest
    expect(socket.options).toMatchObject({
      agent: "SideConversationAgent",
      name,
      enabled: true,
    })
    expect(result.current.status).toBe("connecting")
    socket.open()
    expect(socket.sent).toEqual([{ type: "side-visibility", visible: true }])
    socket.says(
      snapshot([message(1), card(2)], {
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
    expect(result.current.items).toEqual([message(1), card(2)])
    expect(result.current.hasOlder).toBe(true)
    expect(result.current.peer).toEqual({
      online: true,
      typing: true,
      lastReadSeq: 2,
    })
    expect(result.current.agents).toEqual({
      mine: {
        id: "agt_leo",
        name: "Leo",
        ownerId: viewer,
        kind: "primary",
        approval: "owner",
      },
      theirs: [
        {
          id: "agt_nia",
          name: "Nia",
          ownerId: peer,
          kind: "primary",
          approval: "owner",
        },
        {
          id: "agt_pricing",
          name: "Pricing",
          ownerId: peer,
          kind: "specialist",
          approval: "agent",
          role: "Prices things",
        },
      ],
    })
    expect(result.current.teamspace).toEqual({
      name: "Launch room",
      paused: false,
    })
    // Who is in it changes live.
    socket.says({
      type: "side-roster",
      roster: { ...roster, paused: true, contributions: [] },
    })
    expect(result.current.teamspace.paused).toBe(true)
    expect(result.current.agents.theirs).toHaveLength(1)
  })
})

describe("useSideConversation: people's messages", () => {
  it("shows a message at once, confirms it by its page id, fails it on a refusal, and retries it under the same id", () => {
    const { result } = setup()
    const socket = open([message(1)])
    act(() => result.current.send("  Morning  "))
    const [sent] = socket.said("side-send")
    expect(sent.body).toBe("Morning")
    expect(result.current.items.at(-1)).toMatchObject({
      kind: "message",
      seq: 0,
      authorId: viewer,
      body: "Morning",
      clientId: sent.clientId,
      status: "sending",
    })
    socket.says({
      type: "side-item",
      item: message(2, viewer, { body: "Morning", clientId: sent.clientId }),
    })
    expect(result.current.items).toEqual([
      message(1),
      message(2, viewer, { body: "Morning", clientId: sent.clientId }),
    ])

    act(() => result.current.send("Too fast"))
    const [, second] = socket.said("side-send")
    socket.says({
      type: "side-failed",
      clientId: second.clientId,
      code: "rate_limited",
      message: "You're sending messages too quickly. Wait a moment.",
    })
    expect(result.current.items.at(-1)).toMatchObject({
      status: "failed",
      error: "You're sending messages too quickly. Wait a moment.",
    })
    act(() => result.current.retry(second.clientId))
    expect(socket.said("side-send").at(-1)).toEqual(second)
    expect(result.current.items.at(-1)?.kind).toBe("message")

    // Too long for the object: failed at once, never sent.
    act(() => result.current.send("x".repeat(SIDE_MAX_BODY_LENGTH + 1)))
    expect(socket.said("side-send")).toHaveLength(3)
    expect(result.current.items.at(-1)).toMatchObject({ status: "failed" })
  })

  it("fails a message the object never answered", () => {
    vi.useFakeTimers()
    const { result } = setup()
    open()
    act(() => result.current.send("Hello?"))
    act(() => vi.advanceTimersByTime(SIDE_SEND_TIMEOUT_MS))
    expect(result.current.items.at(-1)).toMatchObject({ status: "failed" })
  })

  it("reads through the latest item while visible, and says typing and history as a DM does", async () => {
    const { result } = setup()
    const socket = open([message(51)], { hasOlder: true })
    act(() => result.current.markRead())
    expect(socket.said("side-read")).toEqual([{ type: "side-read", seq: 51 }])
    act(() => result.current.setTyping(true))
    act(() => result.current.setTyping(true))
    act(() => result.current.setTyping(false))
    expect(socket.said("side-typing")).toEqual([
      { type: "side-typing", typing: true },
      { type: "side-typing", typing: false },
    ])
    let loaded = false
    act(() => {
      void result.current.loadOlder().then(() => {
        loaded = true
      })
    })
    const [asked] = socket.said("side-history")
    expect(asked).toMatchObject({ beforeSeq: 51, limit: 50 })
    socket.says({
      type: "side-history",
      requestId: asked.requestId,
      items: [message(50)],
      hasOlder: false,
    })
    await waitFor(() => expect(loaded).toBe(true))
    expect(result.current.items.map((item) => item.seq)).toEqual([50, 51])
    expect(result.current.hasOlder).toBe(false)
  })
})

describe("useSideConversation: asking agents", () => {
  it("asks the viewer's own agent explicitly, and streams its answer as it arrives", () => {
    const { result } = setup()
    const socket = open([message(1)])
    act(() => result.current.askMyAgent("Leo, what did we promise?"))
    const [asked] = socket.said("side-ask-mine")
    expect(asked.body).toBe("Leo, what did we promise?")
    expect(result.current.items.at(-1)).toMatchObject({
      kind: "message",
      asksAgentId: "agt_leo",
      status: "sending",
    })
    socket.says({
      type: "side-item",
      item: message(2, viewer, {
        body: "Leo, what did we promise?",
        clientId: asked.clientId,
        asksAgentId: "agt_leo",
      }),
    })
    socket.says({ type: "side-item", item: agentMessage(3, { replyTo: 2 }) })
    socket.says({ type: "side-delta", seq: 3, at: 0, text: "The beta " })
    socket.says({ type: "side-delta", seq: 3, at: 9, text: "on Friday." })
    expect(result.current.items.at(-1)).toMatchObject({
      kind: "agent-message",
      body: "The beta on Friday.",
      status: "streaming",
    })
    // A delta for a length the view does not hold waits for the whole copy.
    socket.says({ type: "side-delta", seq: 3, at: 99, text: "lost" })
    expect(result.current.items.at(-1)).toMatchObject({
      body: "The beta on Friday.",
    })
    socket.says({
      type: "side-item",
      item: agentMessage(3, {
        replyTo: 2,
        body: "The beta ships on Friday.",
        status: "done",
      }),
    })
    expect(result.current.items.map((item) => item.kind)).toEqual([
      "message",
      "message",
      "agent-message",
    ])
    expect(result.current.items.at(-1)).toMatchObject({
      body: "The beta ships on Friday.",
      status: "done",
    })
  })

  it("files a request to the teammate's agent as a card that is sending, then the object's card", () => {
    const { result } = setup()
    const socket = open()
    act(() =>
      result.current.askTeammateAgent("agt_pricing", "Price the add-on")
    )
    const [asked] = socket.said("side-ask-teammate")
    expect(asked).toMatchObject({
      agentId: "agt_pricing",
      body: "Price the add-on",
    })
    expect(result.current.items).toEqual([
      expect.objectContaining({
        kind: "request-card",
        seq: 0,
        requestId: "",
        requesterId: viewer,
        requesterAgentName: "Leo",
        targetId: peer,
        targetAgentId: "agt_pricing",
        targetAgentName: "Pricing",
        approval: "agent",
        status: "sending",
        task: "Price the add-on",
        clientId: asked.clientId,
      }),
    ])
    socket.says({
      type: "side-failed",
      clientId: asked.clientId,
      code: "not_allowed",
      message: "This teamspace is paused.",
    })
    expect(result.current.items[0]).toMatchObject({
      status: "not_filed",
      error: "This teamspace is paused.",
    })
    act(() => result.current.retry(asked.clientId))
    const filed = card(1, {
      requesterId: viewer,
      targetId: peer,
      targetAgentId: "agt_pricing",
      targetAgentName: "Pricing",
      approval: "agent",
      task: "Price the add-on",
      clientId: asked.clientId,
    })
    socket.says({ type: "side-item", item: filed })
    expect(result.current.items).toEqual([filed])
  })

  it("decides a request held for the viewer, resolving once the card shows it, and says why one was refused", async () => {
    const { result } = setup()
    const socket = open([card(1), card(2)])
    let decided = false
    act(() => {
      void result.current.decide("irq_1", "approved").then(() => {
        decided = true
      })
    })
    expect(socket.said("side-decide")).toEqual([
      { type: "side-decide", requestId: "irq_1", decision: "approved" },
    ])
    expect(result.current.items[0]).toMatchObject({ deciding: true })
    socket.says({
      type: "side-item",
      item: card(1, {
        status: "approved",
        decision: {
          decision: "approved",
          message: "Approved in the side conversation.",
          decidedAt: at(3),
        },
      }),
    })
    await waitFor(() => expect(decided).toBe(true))
    expect(result.current.items[0]).not.toHaveProperty("deciding")

    let refusal: unknown
    act(() => {
      result.current
        .decide("irq_2", "rejected", "  Not now ")
        .catch((error: unknown) => {
          refusal = error
        })
    })
    expect(socket.said("side-decide").at(-1)).toEqual({
      type: "side-decide",
      requestId: "irq_2",
      decision: "rejected",
      message: "Not now",
    })
    socket.says({
      type: "side-refused",
      target: "irq_2",
      code: "already_decided",
      message: "This request was decided already, or expired.",
    })
    await waitFor(() => expect(refusal).toBeInstanceOf(SideActionError))
    expect(refusal).toMatchObject({ code: "already_decided" })
    expect(result.current.items[1]).toMatchObject({
      decisionError: "This request was decided already, or expired.",
    })

    // Not the viewer's to decide: refused here, nothing sent.
    const theirs = card(3, { requesterId: viewer, targetId: peer })
    socket.says({ type: "side-item", item: theirs })
    await expect(result.current.decide("irq_3", "approved")).rejects.toThrow(
      SideActionError
    )
    expect(socket.said("side-decide")).toHaveLength(2)
  })

  it("answers a confirmation the viewer's own agent waits on", async () => {
    const { result } = setup()
    const waiting = agentMessage(2, {
      status: "waiting",
      body: "I can email the plan.",
      confirmations: [{ callId: "call_1", tool: "gmail_send" }],
    })
    const socket = open([message(1, viewer), waiting])
    let answered = false
    act(() => {
      void result.current
        .confirm(2, [{ id: "call_1", confirmed: true }])
        .then(() => {
          answered = true
        })
    })
    expect(socket.said("side-confirm")).toEqual([
      {
        type: "side-confirm",
        seq: 2,
        answers: [{ id: "call_1", confirmed: true }],
      },
    ])
    socket.says({
      type: "side-item",
      item: { ...waiting, status: "streaming", confirmations: undefined },
    })
    await waitFor(() => expect(answered).toBe(true))
    await expect(
      result.current.confirm(1, [{ id: "call_1", confirmed: true }])
    ).rejects.toThrow(SideActionError)
  })
})

describe("useSideConversation: sessions and refusals", () => {
  it("renews an ended session, reopens, and asks again what was waiting, once", async () => {
    const api = mockApi(({ path: asked }) =>
      asked === "/v1/auth/me" ? json({ user: { id: viewer } }) : undefined
    )
    const { result } = setup()
    const socket = open()
    act(() => result.current.askMyAgent("Summarise"))
    const [asked] = socket.said("side-ask-mine")
    socket.says({
      type: "side-failed",
      clientId: asked.clientId,
      code: "session_expired",
      message: "Your session has ended.",
    })
    socket.says({ type: "side-session-expired" })
    expect(result.current.items.at(-1)).toMatchObject({ status: "sending" })
    await waitFor(() => expect(socket.reconnects).toBe(1))
    expect(api.calls.map((call) => call.path)).toEqual(["/v1/auth/me"])
    await waitFor(() => expect(socket.readyState).toBe(1))
    socket.says(snapshot())
    expect(socket.said("side-ask-mine").at(-1)).toEqual(asked)
    // The reopen's own close asked nothing of the Worker.
    expect(api.calls.map((call) => call.path)).not.toContain(path)

    // Ended again: this time it fails.
    socket.says({
      type: "side-failed",
      clientId: asked.clientId,
      code: "session_expired",
      message: "Your session has ended.",
    })
    expect(result.current.items.at(-1)).toMatchObject({ status: "failed" })
  })

  it("ends on a session that could not be refreshed", async () => {
    mockApi(({ path: asked }) =>
      asked === "/v1/auth/me" || asked === "/v1/auth/refresh"
        ? apiError(401, "unauthorized", "access token is required")
        : undefined
    )
    const { result } = setup()
    const socket = open()
    socket.says({ type: "side-session-expired" })
    await waitFor(() => expect(result.current.status).toBe("session_expired"))
    expect(socket.options.enabled).toBe(false)
  })

  it("asks the Worker why a socket closed: off the team is not allowed, for good", async () => {
    const api = mockApi(({ path: asked }) =>
      asked === path
        ? apiError(404, "peer_not_found", "Not on the teamspace's team.")
        : undefined
    )
    const { result } = setup()
    const socket = roomSockets.latest
    act(() => result.current.send("Hello?"))
    socket.drop()
    await waitFor(() => expect(result.current.status).toBe("not-allowed"))
    expect(api.calls.map((call) => call.path)).toEqual([path])
    expect(socket.options.enabled).toBe(false)
    expect(result.current.items[0]).toMatchObject({ status: "failed" })
  })

  it("after a purge, reopens when the Worker lets the viewer in, and takes what stayed as a new run", async () => {
    mockApi(({ path: asked }) =>
      asked === path ? json(undefined, 204) : undefined
    )
    const { result } = setup()
    const socket = open([message(1), message(2)], { conversation: "sdc_1" })
    socket.says({ type: "side-gone" })
    expect(result.current.status).toBe("error")
    await waitFor(() => expect(socket.reconnects).toBe(1))
    await waitFor(() => expect(socket.readyState).toBe(1))
    socket.says(snapshot([message(2)], { conversation: "sdc_2" }))
    expect(result.current.status).toBe("live")
    expect(result.current.items).toEqual([message(2)])
  })

  it("after a purge, is not allowed when the Worker refuses", async () => {
    mockApi(({ path: asked }) =>
      asked === path
        ? apiError(403, "forbidden", "Only the teamspace's team.")
        : undefined
    )
    const { result } = setup()
    const socket = open([message(1)])
    socket.drop(sideGoneCloseCode)
    await waitFor(() => expect(result.current.status).toBe("not-allowed"))
    expect(socket.reconnects).toBe(0)
  })
})
