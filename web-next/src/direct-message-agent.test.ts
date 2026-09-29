import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  DirectMessageAgent,
  dmInboxRetry,
  dmSendLimit,
  dmTypingRetellMs,
} from "./direct-message-agent"
import { InboxAgent } from "./inbox-agent"
import { directMessageName, inboxName } from "./lib/dms/names"
import {
  DM_HISTORY_LIMIT,
  DM_MAX_BODY_LENGTH,
  DM_TYPING_TTL_MS,
  conversationGoneCloseCode,
} from "./lib/dms/protocol"
import { DM_REQUEST_LIMIT } from "./lib/dms/request-notes"
import type { RequestNote } from "./lib/dms/request-notes"
import { realtimeEventTypes } from "./realtime-events"
import { RealtimeRegistryAgent } from "./realtime-registry-agent"
import {
  World,
  closeSocket,
  drain,
  openSocket,
  runDueJobs,
  say,
} from "./test/durable-objects"
import type { FakeSocket, Harness } from "./test/durable-objects"
import {
  FakeGoEvents,
  eventsOrigin,
  eventsSecret,
  routeFetch,
} from "./test/fake-go-events"

vi.mock(
  "agents",
  async () => (await import("./test/durable-objects")).agentsModule
)

const org = "org_sim"
const ada = "user-ada"
const bo = "user-bo"
const dmName = directMessageName(org, bo, ada)

type Dm = DirectMessageAgent & Harness
type Inbox = InboxAgent & Harness

function makeWorld(vars: Record<string, unknown> = {}) {
  return new World(
    {
      DirectMessageAgent,
      InboxAgent,
      RealtimeRegistryAgent,
    } as unknown as ConstructorParameters<typeof World>[0],
    vars
  )
}

async function setup(vars?: Record<string, unknown>) {
  const world = makeWorld(vars)
  const dm = await world.object<Dm>("DirectMessageAgent", dmName)
  return { world, dm }
}

function join(dm: Dm, userId: string, headers?: Record<string, string>) {
  return openSocket(dm, { organizationId: org, userId }, headers)
}

let clientIds = 0

function nextClientId() {
  clientIds += 1
  return `client-${String(clientIds).padStart(4, "0")}`
}

async function send(
  dm: Dm,
  socket: FakeSocket,
  body: string,
  clientId?: string
) {
  const id = clientId ?? nextClientId()
  await say(dm, socket, { type: "dm-send", clientId: id, body })
  return id
}

function messagesOf(socket: FakeSocket) {
  return socket
    .of("dm-message")
    .map((message) => message.message as Record<string, unknown>)
}

async function inboxOf(world: World, userId: string) {
  const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, userId))
  return inbox.listConversations()
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date", "setTimeout", "clearTimeout"] })
  vi.setSystemTime(new Date("2026-09-24T09:00:00.000Z"))
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe("DirectMessageAgent: sockets", () => {
  it("opens only for its two people, as the Worker named them, and sends nothing before it has checked", async () => {
    const { dm } = await setup()
    const refused: Array<Record<string, string>> = [
      { "X-Oryxa-User": "" },
      { "X-Oryxa-User": "user-cy" },
      { "X-Oryxa-Organization": "org_other" },
      { "X-Oryxa-Organization": "" },
    ]
    for (const headers of refused) {
      const socket = await join(dm, ada, headers)
      expect(socket.closed).toEqual({ code: 1008, reason: "Unauthorized" })
      expect(socket.sent).toEqual([])
    }
    // No SDK identity, state or MCP frames on any socket.
    expect(dm.shouldSendProtocolMessages()).toBe(false)

    const socket = await join(dm, bo)
    expect(socket.closed).toBeUndefined()
    expect(socket.state).toEqual({
      userId: bo,
      visible: false,
      connectedAt: "2026-09-24T09:00:00.000Z",
    })
    expect(socket.sent).toEqual([
      {
        type: "dm-snapshot",
        conversation: "",
        messages: [],
        hasOlder: false,
        reads: [
          { userId: ada, seq: 0 },
          { userId: bo, seq: 0 },
        ],
        online: [],
        typing: [],
      },
    ])
  })

  it("answers the Worker's check for a refused socket, and refuses a page's state", async () => {
    const { dm } = await setup()
    expect(dm.onRequest(new Request("http://x/")).status).toBe(204)
    const post = dm.onRequest(new Request("http://x/", { method: "POST" }))
    expect([post.status, post.headers.get("Allow")]).toEqual([405, "GET"])
    expect(() =>
      dm.validateStateChange({}, {} as Parameters<Dm["validateStateChange"]>[1])
    ).toThrow()
    expect(() => dm.validateStateChange({}, "server")).not.toThrow()
  })

  it("ignores what it does not take", async () => {
    const { dm } = await setup()
    const socket = await join(dm, ada)
    socket.clear()
    for (const message of [
      "not json",
      "[]",
      JSON.stringify({ type: "dm-unknown" }),
      JSON.stringify({ type: "dm-send", clientId: "short", body: "hi" }),
      JSON.stringify({ type: "dm-send", clientId: "client-bad!", body: "hi" }),
      JSON.stringify({ type: "dm-send", clientId: "client-0001", body: 7 }),
      JSON.stringify({ type: "dm-read", seq: -1 }),
      JSON.stringify({ type: "dm-read", seq: 1.5 }),
      JSON.stringify({ type: "dm-typing", typing: "yes" }),
      JSON.stringify({ type: "cf_agent_state", state: { x: 1 } }),
      JSON.stringify({
        type: "dm-send",
        clientId: "client-0001",
        body: "x".repeat(70_000),
      }),
    ]) {
      await say(dm, socket, message)
    }
    expect(socket.sent).toEqual([])
    expect(dm.sql`SELECT * FROM dm_messages`).toEqual([])
  })
})

describe("DirectMessageAgent: sending", () => {
  it("stores a message as the socket's person and sends it to every socket at once, its page id only to the author's", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    const adaPanel = await join(dm, ada)
    const boTab = await join(dm, bo)

    // Whatever the page claims, the author is the socket's person.
    await say(dm, adaTab, {
      type: "dm-send",
      clientId: "client-0001",
      body: "  Morning, Bo  ",
      authorId: bo,
    })

    const stored = {
      id: expect.stringMatching(/^dmm_[0-9a-f]{32}$/) as unknown,
      seq: 1,
      authorId: ada,
      body: "Morning, Bo",
      createdAt: "2026-09-24T09:00:00.000Z",
    }
    expect(messagesOf(adaTab)).toEqual([{ ...stored, clientId: "client-0001" }])
    expect(messagesOf(adaPanel)).toEqual([
      { ...stored, clientId: "client-0001" },
    ])
    expect(messagesOf(boTab)).toEqual([stored])
    // Sending reads the conversation through it, and everybody hears.
    for (const socket of [adaTab, adaPanel, boTab]) {
      expect(socket.of("dm-read")).toEqual([
        { type: "dm-read", userId: ada, seq: 1 },
      ])
    }

    await send(dm, boTab, "Morning!")
    expect(
      messagesOf(adaTab).map((message) => [message.seq, message.authorId])
    ).toEqual([
      [1, ada],
      [2, bo],
    ])
  })

  it("stores a message once per author and page id: sending it again answers with the stored one", async () => {
    const { dm, world } = await setup()
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)

    await send(dm, adaTab, "Once", "client-0001")
    adaTab.clear()
    boTab.clear()
    await send(dm, adaTab, "Once, again", "client-0001")

    expect(messagesOf(adaTab)).toEqual([
      expect.objectContaining({
        seq: 1,
        body: "Once",
        clientId: "client-0001",
      }),
    ])
    expect(boTab.sent).toEqual([])
    expect(dm.sql`SELECT seq FROM dm_messages`).toEqual([{ seq: 1 }])
    expect((await inboxOf(world, bo))[0].unread).toBe(1)

    // The same page id from the other person is their own message.
    await send(dm, boTab, "Mine", "client-0001")
    expect(dm.sql`SELECT seq, author_id FROM dm_messages`).toEqual([
      { seq: 1, author_id: ada },
      { seq: 2, author_id: bo },
    ])
  })

  it("refuses an empty or over-long message to the socket that sent it", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    boTab.clear()

    await send(dm, adaTab, "   \n ", "client-0001")
    await send(dm, adaTab, "x".repeat(DM_MAX_BODY_LENGTH + 1), "client-0002")
    expect(adaTab.of("dm-send-failed")).toEqual([
      {
        type: "dm-send-failed",
        clientId: "client-0001",
        code: "empty",
        message: "Write something to send.",
      },
      {
        type: "dm-send-failed",
        clientId: "client-0002",
        code: "too_long",
        message: "A message holds at most 4,000 characters.",
      },
    ])
    expect(boTab.sent).toEqual([])

    // The limit counts code points: 4,000 emoji are 8,000 UTF-16 units.
    await send(dm, adaTab, "🙂".repeat(DM_MAX_BODY_LENGTH), "client-0003")
    expect(messagesOf(adaTab)).toHaveLength(1)
  })

  it("lets one person send a burst, then one a second", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)

    for (let index = 0; index < dmSendLimit.burst; index += 1) {
      await send(dm, adaTab, `line ${index}`)
    }
    const over = await send(dm, adaTab, "one too many")
    expect(adaTab.of("dm-send-failed")).toEqual([
      {
        type: "dm-send-failed",
        clientId: over,
        code: "rate_limited",
        message: "You're sending messages too quickly. Wait a moment.",
      },
    ])
    // The limit is each person's own.
    await send(dm, boTab, "I can still talk")
    expect(boTab.of("dm-send-failed")).toEqual([])

    vi.advanceTimersByTime(dmSendLimit.refillMs)
    await send(dm, adaTab, "one more", over)
    expect(messagesOf(adaTab).at(-1)).toEqual(
      expect.objectContaining({ body: "one more", clientId: over })
    )
    await send(dm, adaTab, "and again")
    expect(adaTab.of("dm-send-failed")).toHaveLength(2)
  })

  it("keeps the latest DM_HISTORY_LIMIT messages, dropping the oldest", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    // A full conversation, written directly: sending 10,000 would take a while.
    for (let seq = 1; seq <= DM_HISTORY_LIMIT; seq += 1) {
      dm.sql`INSERT INTO dm_messages (seq, id, author_id, client_id, body, created_at)
        VALUES (${seq}, ${`dmm_${seq}`}, ${ada}, ${`seed-${seq}`}, ${"old"}, ${"2026-09-23T09:00:00.000Z"})`
    }
    await send(dm, adaTab, "the newest")
    const kept = dm.sql<{ first: number; last: number; count: number }>`
      SELECT MIN(seq) AS first, MAX(seq) AS last, COUNT(*) AS count FROM dm_messages`
    expect(kept).toEqual([
      { first: 2, last: DM_HISTORY_LIMIT + 1, count: DM_HISTORY_LIMIT },
    ])
  })
})

describe("DirectMessageAgent: history", () => {
  it("sends the latest page as a socket opens, and older pages on request", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    for (let index = 1; index <= 120; index += 1) {
      vi.advanceTimersByTime(dmSendLimit.refillMs)
      await send(dm, adaTab, `line ${index}`)
    }

    const boTab = await join(dm, bo)
    const [snapshot] = boTab.of("dm-snapshot")
    const seqs = (snapshot.messages as Array<{ seq: number }>).map((m) => m.seq)
    expect(seqs).toEqual(Array.from({ length: 50 }, (_, index) => 71 + index))
    expect(snapshot.hasOlder).toBe(true)
    expect(snapshot.conversation).toMatch(/^dmc_[0-9a-f]{32}$/)

    await say(dm, boTab, {
      type: "dm-history",
      requestId: "older-1",
      beforeSeq: 71,
      limit: 50,
    })
    await say(dm, boTab, {
      type: "dm-history",
      requestId: "older-2",
      beforeSeq: 21,
      limit: 50,
    })
    await say(dm, boTab, {
      type: "dm-history",
      requestId: "huge",
      beforeSeq: 0,
      limit: 5_000,
    })
    const pages = boTab.of("dm-history").map((page) => ({
      requestId: page.requestId,
      seqs: (page.messages as Array<{ seq: number }>).map((m) => m.seq),
      hasOlder: page.hasOlder,
    }))
    expect(pages[0]).toEqual({
      requestId: "older-1",
      seqs: Array.from({ length: 50 }, (_, index) => 21 + index),
      hasOlder: true,
    })
    expect(pages[1]).toEqual({
      requestId: "older-2",
      seqs: Array.from({ length: 20 }, (_, index) => 1 + index),
      hasOlder: false,
    })
    // A page holds at most 100.
    expect(pages[2].seqs).toHaveLength(100)
    expect(pages[2].seqs.at(-1)).toBe(120)
    // Only the socket that asked hears.
    expect(adaTab.of("dm-history")).toEqual([])
  })

  it("sends a page of long messages shorter, saying there is more", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    for (let index = 1; index <= 40; index += 1) {
      vi.advanceTimersByTime(dmSendLimit.refillMs)
      await send(dm, adaTab, `${index} ${"🙂".repeat(3_990)}`)
    }
    const boTab = await join(dm, bo)
    const [snapshot] = boTab.of("dm-snapshot")
    const messages = snapshot.messages as Array<{ seq: number }>
    expect(messages.length).toBeLessThan(40)
    expect(messages.at(-1)?.seq).toBe(40)
    expect(snapshot.hasOlder).toBe(true)
    expect(JSON.stringify(snapshot).length).toBeLessThan(300 * 1024)
  })
})

describe("DirectMessageAgent: reading", () => {
  it("keeps how far each person has read, only ever further, and tells both", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    await send(dm, adaTab, "one")
    await send(dm, adaTab, "two")
    await send(dm, adaTab, "three")
    adaTab.clear()
    boTab.clear()

    await say(dm, boTab, { type: "dm-read", seq: 2 })
    await say(dm, boTab, { type: "dm-read", seq: 1 })
    await say(dm, boTab, { type: "dm-read", seq: 2 })
    // Beyond the latest message reads through the latest.
    await say(dm, boTab, { type: "dm-read", seq: 99 })
    const told = [
      { type: "dm-read", userId: bo, seq: 2 },
      { type: "dm-read", userId: bo, seq: 3 },
    ]
    expect(adaTab.of("dm-read")).toEqual(told)
    expect(boTab.of("dm-read")).toEqual(told)

    const late = await join(dm, ada)
    expect(late.last("dm-snapshot")?.reads).toEqual([
      { userId: ada, seq: 3 },
      { userId: bo, seq: 3 },
    ])
  })
})

describe("DirectMessageAgent: typing", () => {
  it("tells the other person someone is typing, again while it lasts, and that it stopped", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    adaTab.clear()
    boTab.clear()

    await say(dm, adaTab, { type: "dm-typing", typing: true })
    vi.advanceTimersByTime(dmTypingRetellMs - 1)
    await say(dm, adaTab, { type: "dm-typing", typing: true })
    vi.advanceTimersByTime(1)
    await say(dm, adaTab, { type: "dm-typing", typing: true })
    await say(dm, adaTab, { type: "dm-typing", typing: false })
    expect(boTab.of("dm-typing")).toEqual([
      { type: "dm-typing", userId: ada, typing: true },
      { type: "dm-typing", userId: ada, typing: true },
      { type: "dm-typing", userId: ada, typing: false },
    ])
    // Never to the typist, and never stored.
    expect(adaTab.of("dm-typing")).toEqual([])
    expect(JSON.stringify(dm.sql`SELECT * FROM dm_meta`)).not.toContain("typ")
  })

  it("stops on its own after DM_TYPING_TTL_MS, when the typist sends, and when their last socket closes", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    boTab.clear()

    await say(dm, adaTab, { type: "dm-typing", typing: true })
    const late = await join(dm, bo)
    expect(late.last("dm-snapshot")?.typing).toEqual([ada])
    vi.advanceTimersByTime(DM_TYPING_TTL_MS)
    expect(boTab.of("dm-typing").map((message) => message.typing)).toEqual([
      true,
      false,
    ])

    boTab.clear()
    await say(dm, adaTab, { type: "dm-typing", typing: true })
    await send(dm, adaTab, "done typing")
    expect(boTab.of("dm-typing").map((message) => message.typing)).toEqual([
      true,
      false,
    ])

    boTab.clear()
    await say(dm, adaTab, { type: "dm-typing", typing: true })
    await closeSocket(dm, adaTab)
    expect(boTab.of("dm-typing").map((message) => message.typing)).toEqual([
      true,
      false,
    ])
    vi.advanceTimersByTime(DM_TYPING_TTL_MS)
    expect(boTab.of("dm-typing")).toHaveLength(2)
  })
})

describe("DirectMessageAgent: presence", () => {
  it("counts a person online while one of their sockets says its tab is visible", async () => {
    const { dm } = await setup()
    const boTab = await join(dm, bo)
    const adaTab = await join(dm, ada)
    const adaPanel = await join(dm, ada)
    boTab.clear()

    await say(dm, adaTab, { type: "dm-visibility", visible: true })
    await say(dm, adaPanel, { type: "dm-visibility", visible: true })
    await say(dm, adaTab, { type: "dm-visibility", visible: false })
    expect(boTab.of("dm-presence")).toEqual([
      { type: "dm-presence", userId: ada, online: true },
    ])
    const late = await join(dm, bo)
    expect(late.last("dm-snapshot")?.online).toEqual([ada])

    await say(dm, adaPanel, { type: "dm-visibility", visible: false })
    await say(dm, adaPanel, { type: "dm-visibility", visible: true })
    await closeSocket(dm, adaPanel)
    expect(boTab.of("dm-presence")).toEqual([
      { type: "dm-presence", userId: ada, online: true },
      { type: "dm-presence", userId: ada, online: false },
      { type: "dm-presence", userId: ada, online: true },
      { type: "dm-presence", userId: ada, online: false },
    ])
    // A hidden socket closing changes nothing.
    await closeSocket(dm, adaTab)
    expect(boTab.of("dm-presence")).toHaveLength(4)
  })
})

describe("DirectMessageAgent: inboxes", () => {
  it("lists the conversation in both inboxes, unread for the other person until they read it", async () => {
    const { dm, world } = await setup()
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    const boInbox = await world.object<Inbox>("InboxAgent", inboxName(org, bo))
    const boList = await openSocket(boInbox, {
      organizationId: org,
      userId: bo,
    })

    await send(dm, adaTab, "Are you free at 3?")
    vi.advanceTimersByTime(60_000)
    await send(dm, adaTab, `A long thought: ${"and more ".repeat(40)}`)

    const lastMessage = {
      authorId: ada,
      body: expect.stringMatching(/^A long thought: and more .*…$/) as unknown,
      createdAt: "2026-09-24T09:01:00.000Z",
    }
    expect(await inboxOf(world, bo)).toEqual([
      {
        kind: "dm",
        name: dmName,
        peerId: ada,
        lastMessage,
        unread: 2,
        updatedAt: "2026-09-24T09:01:00.000Z",
      },
    ])
    expect((await inboxOf(world, bo))[0].lastMessage?.body).toHaveLength(160)
    expect(await inboxOf(world, ada)).toEqual([
      expect.objectContaining({ peerId: bo, unread: 0 }),
    ])
    // Sending reads through it in the author's inbox as it is noted: no
    // read of its own goes to either inbox.
    expect(world.callsTo("InboxAgent", "markRead")).toEqual([])
    expect(boList.last("inbox-conversation")).toEqual({
      type: "inbox-conversation",
      conversation: expect.objectContaining({
        name: dmName,
        unread: 2,
      }) as unknown,
      unreadTotal: 2,
    })

    await say(dm, boTab, { type: "dm-read", seq: 1 })
    expect(world.callsTo("InboxAgent", "markRead")).toEqual([
      { binding: "InboxAgent", name: inboxName(org, bo), method: "markRead" },
    ])
    expect((await inboxOf(world, bo))[0].unread).toBe(1)
    await say(dm, boTab, { type: "dm-read", seq: 2 })
    expect((await inboxOf(world, bo))[0].unread).toBe(0)
    expect(boList.last("inbox-conversation")?.unreadTotal).toBe(0)

    // Replying reads through the reply, for the one who wrote it.
    await send(dm, adaTab, "?")
    await send(dm, boTab, "Yes, 3 works")
    expect((await inboxOf(world, bo))[0]).toEqual(
      expect.objectContaining({
        unread: 0,
        lastMessage: expect.objectContaining({ authorId: bo }) as unknown,
      })
    )
    expect((await inboxOf(world, ada))[0].unread).toBe(1)
  })

  it("tells an inbox what it missed once it can be reached, on a schedule", async () => {
    const { dm, world } = await setup()
    const adaTab = await join(dm, ada)
    const boInbox = inboxName(org, bo)
    vi.spyOn(console, "error").mockImplementation(() => {})

    world.failing.add(boInbox)
    await send(dm, adaTab, "one")
    await send(dm, adaTab, "two")
    // The sockets heard at once; the inbox did not.
    expect(messagesOf(adaTab)).toHaveLength(2)
    expect(dm.jobs.map((job) => job.callback)).toEqual(["retryInboxSync"])
    expect(dm.jobs[0].time * 1000 - Date.now()).toBe(
      dmInboxRetry.firstSeconds * 1000
    )

    // Still unreachable: the next try waits twice as long.
    vi.advanceTimersByTime(dmInboxRetry.firstSeconds * 1000)
    await runDueJobs(dm)
    expect(dm.jobs.map((job) => job.time * 1000 - Date.now())).toEqual([
      dmInboxRetry.firstSeconds * 2 * 1000,
    ])

    world.failing.delete(boInbox)
    vi.advanceTimersByTime(dmInboxRetry.firstSeconds * 2 * 1000)
    await runDueJobs(dm)
    expect(await inboxOf(world, bo)).toEqual([
      expect.objectContaining({
        unread: 2,
        lastMessage: expect.objectContaining({ body: "two" }) as unknown,
      }),
    ])
    expect(dm.jobs).toEqual([])

    // What each inbox has been told is kept, so it is not told again.
    expect(
      dm.sql`SELECT user_id, noted_seq, read_seq FROM dm_inbox_sync ORDER BY user_id`
    ).toEqual([
      { user_id: ada, noted_seq: 2, read_seq: 2 },
      { user_id: bo, noted_seq: 2, read_seq: 0 },
    ])
  })

  it("tells both inboxes it exists as it opens, before any message, without listing it", async () => {
    const { dm, world } = await setup()
    await join(dm, ada)
    expect(
      world.callsTo("InboxAgent", "knowConversation").map((call) => call.name)
    ).toEqual([inboxName(org, ada), inboxName(org, bo)])
    for (const user of [ada, bo]) {
      expect(await inboxOf(world, user)).toEqual([])
      const inbox = await world.object<Inbox>(
        "InboxAgent",
        inboxName(org, user)
      )
      expect(inbox.sql`SELECT name, peer_id FROM inbox_known`).toEqual([
        { name: dmName, peer_id: user === ada ? bo : ada },
      ])
    }
    // Once known, it is not told again.
    await join(dm, bo)
    expect(world.callsTo("InboxAgent", "knowConversation")).toHaveLength(2)
  })

  it("sets its alarm before it calls the inboxes, and lets it go once they answered", async () => {
    const { dm, world } = await setup()
    const adaTab = await join(dm, ada)
    const release = world.hold(inboxName(org, bo))
    const sending = send(dm, adaTab, "one")
    await drain()
    // Were this instance lost now, the alarm would still tell Bo's inbox.
    expect(dm.jobs.map((job) => job.callback)).toEqual(["retryInboxSync"])
    release()
    await sending
    expect(dm.jobs).toEqual([])
    expect((await inboxOf(world, bo))[0].unread).toBe(1)
  })

  it("tells an inbox again after a restart, when nothing was scheduled", async () => {
    const world = makeWorld()
    const first = await world.object<Dm>("DirectMessageAgent", dmName)
    const adaTab = await join(first, ada)
    vi.spyOn(console, "error").mockImplementation(() => {})
    world.failing.add(inboxName(org, bo))
    await send(first, adaTab, "one")
    // As a restart that lost the schedule.
    first.jobs = []
    first.sql`DELETE FROM dm_meta WHERE key = 'retry'`
    await first.onStart()
    expect(first.jobs.map((job) => job.callback)).toEqual(["retryInboxSync"])
  })
})

describe("DirectMessageAgent: purge", () => {
  it("empties the conversation and closes every socket, and starts a new one after", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    await send(dm, adaTab, "one")
    await say(dm, boTab, { type: "dm-typing", typing: true })
    const before = (await join(dm, ada)).last("dm-snapshot")?.conversation
    expect(before).toMatch(/^dmc_/)

    const sockets = [...dm.getConnections()] as unknown as Array<FakeSocket>
    await dm.purge()
    expect(sockets).toHaveLength(3)
    for (const socket of sockets) {
      expect(socket.sent.at(-1)).toEqual({ type: "dm-gone" })
      expect(socket.closed).toEqual({
        code: conversationGoneCloseCode,
        reason: "Gone",
      })
    }
    for (const table of ["dm_messages", "dm_inbox_sync"]) {
      expect(
        dm.sql([`SELECT * FROM ${table}`] as unknown as TemplateStringsArray)
      ).toEqual([])
    }
    // Two numbers stay: how far each person read, and the highest seq used.
    expect(dm.sql`SELECT user_id, seq FROM dm_reads`).toEqual([
      { user_id: ada, seq: 1 },
    ])
    expect(dm.sql`SELECT key, value FROM dm_meta`).toEqual([
      { key: "seq_floor", value: "1" },
    ])
    await dm.purge()

    const again = await join(dm, ada)
    expect(again.last("dm-snapshot")).toEqual(
      expect.objectContaining({ conversation: "", messages: [], typing: [] })
    )
    await send(dm, again, "a new start")
    // Newer than anything before: no seq is used twice.
    expect(messagesOf(again)).toEqual([expect.objectContaining({ seq: 2 })])
    const reopened = await join(dm, bo)
    expect(reopened.last("dm-snapshot")?.conversation).toMatch(/^dmc_/)
    expect(reopened.last("dm-snapshot")?.conversation).not.toBe(before)
  })
})

describe("DirectMessageAgent: purging one person who left", () => {
  const removedAt = "2026-09-24T09:00:30.000Z"

  it("keeps what is newer than when they left, as a new run, and restates both inboxes", async () => {
    const { dm, world } = await setup()
    const adaOld = await join(dm, ada)
    const boOld = await join(dm, bo)
    await send(dm, adaOld, "before, from Ada")
    await send(dm, boOld, "before, from Bo")
    // Ada leaves at 09:00:30. Her page from before is still open, and says
    // something after she left; then she is back, on a socket opened since.
    vi.setSystemTime(new Date("2026-09-24T09:01:00.000Z"))
    await send(dm, adaOld, "stale, from Ada")
    const adaNew = await join(dm, ada)
    await send(dm, adaNew, "after, from Ada")
    const before = adaNew.last("dm-snapshot")?.conversation
    expect((await inboxOf(world, bo))[0].unread).toBe(2)

    expect(await dm.purge({ userId: ada, removedAt })).toEqual({
      emptied: false,
    })

    expect(dm.sql`SELECT seq, body FROM dm_messages`).toEqual([
      { seq: 4, body: "after, from Ada" },
    ])
    // The sockets from before she left end their run, and each page asks
    // the Worker whether it may open the DM again; hers since is sent the
    // new run.
    for (const socket of [adaOld, boOld]) {
      expect(socket.sent.at(-1)).toEqual({ type: "dm-gone" })
      expect(socket.closed?.code).toBe(conversationGoneCloseCode)
    }
    expect(adaNew.closed).toBeUndefined()
    const run = adaNew.last("dm-snapshot")
    expect(run?.conversation).toMatch(/^dmc_/)
    expect(run?.conversation).not.toBe(before)
    expect(
      (run?.messages as Array<{ seq: number }>).map((message) => message.seq)
    ).toEqual([4])
    // Both inboxes keep only what stays: its latest message, and Bo's
    // unread without the messages that went.
    expect(await inboxOf(world, bo)).toEqual([
      expect.objectContaining({
        lastMessage: expect.objectContaining({
          body: "after, from Ada",
        }) as unknown,
        unread: 1,
      }),
    ])
    expect(await inboxOf(world, ada)).toEqual([
      expect.objectContaining({ unread: 0 }),
    ])
    // What stays goes on as before.
    const boNew = await join(dm, bo)
    await send(dm, boNew, "welcome back")
    expect((await inboxOf(world, ada))[0].unread).toBe(1)
  })

  it("empties it when nothing is newer and no socket opened since", async () => {
    const { dm, world } = await setup()
    const adaTab = await join(dm, ada)
    await send(dm, adaTab, "before")
    vi.setSystemTime(new Date("2026-09-24T09:05:00.000Z"))

    expect(await dm.purge({ userId: ada, removedAt })).toEqual({
      emptied: true,
    })
    for (const table of ["dm_messages", "dm_inbox_sync"]) {
      expect(
        dm.sql([`SELECT * FROM ${table}`] as unknown as TemplateStringsArray)
      ).toEqual([])
    }
    expect(dm.sql`SELECT user_id, seq FROM dm_reads`).toEqual([
      { user_id: ada, seq: 1 },
    ])
    expect(dm.sql`SELECT key, value FROM dm_meta`).toEqual([
      { key: "seq_floor", value: "1" },
    ])
    expect(adaTab.closed?.code).toBe(conversationGoneCloseCode)
    // The inbox's caller takes it off both inboxes.
    expect(world.callsTo("InboxAgent", "restateConversation")).toEqual([])
  })

  it("keeps a DM open on a socket opened since, known but not listed, when nothing newer was said", async () => {
    const { dm, world } = await setup()
    const adaOld = await join(dm, ada)
    await send(dm, adaOld, "before")
    vi.setSystemTime(new Date("2026-09-24T09:01:00.000Z"))
    const boNew = await join(dm, bo)

    expect(await dm.purge({ userId: ada, removedAt })).toEqual({
      emptied: false,
    })
    expect(dm.sql`SELECT * FROM dm_messages`).toEqual([])
    // The reads stay where they were: every seq to come is newer.
    expect(boNew.last("dm-snapshot")).toEqual(
      expect.objectContaining({
        conversation: "",
        messages: [],
        reads: [
          { userId: ada, seq: 1 },
          { userId: bo, seq: 0 },
        ],
      })
    )
    for (const user of [ada, bo]) {
      expect(await inboxOf(world, user)).toEqual([])
      const inbox = await world.object<Inbox>(
        "InboxAgent",
        inboxName(org, user)
      )
      expect(inbox.sql`SELECT name FROM inbox_known`).toEqual([
        { name: dmName },
      ])
    }
  })

  it("counts a message in removed_at's millisecond as at or before it, and the next one as newer", async () => {
    const { dm } = await setup()
    const boTab = await join(dm, bo)
    vi.setSystemTime(new Date("2026-09-24T10:00:00.123Z"))
    await send(dm, boTab, "in the same millisecond")
    vi.setSystemTime(new Date("2026-09-24T10:00:00.124Z"))
    await send(dm, boTab, "a millisecond later")

    // removed_at 10:00:00.123456789Z, as the purge hands it on.
    await dm.purge({ userId: ada, removedAt: "2026-09-24T10:00:00.123Z" })
    expect(dm.sql`SELECT body FROM dm_messages`).toEqual([
      { body: "a millisecond later" },
    ])
  })

  it("refuses a scope that is not this conversation's, or not an instant", async () => {
    const { dm } = await setup()
    for (const scope of [
      { userId: "user-cy", removedAt },
      { userId: ada, removedAt: "2026-09-24T09:00:30Z" },
      { userId: ada, removedAt: "yesterday" },
    ]) {
      await expect(dm.purge(scope)).rejects.toThrow(TypeError)
    }
  })
})

describe("DirectMessageAgent: events for Go", () => {
  const eventsOn = {
    ORYXA_API_ORIGIN: eventsOrigin,
    REALTIME_PURGE_SECRET: eventsSecret,
  }
  const posted = realtimeEventTypes.messagePosted
  const read = realtimeEventTypes.conversationRead
  let go: FakeGoEvents

  beforeEach(() => {
    go = new FakeGoEvents()
    vi.stubGlobal("fetch", routeFetch(go))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function outbox(dm: Dm) {
    return dm.sql<{
      id: string
      type: string
      detail: string
      reader_id: string | null
    }>`SELECT id, type, detail, reader_id FROM event_outbox ORDER BY position`.map(
      (row) => ({
        ...row,
        detail: JSON.parse(row.detail) as Record<string, unknown>,
      })
    )
  }

  it("records each message and each read that goes further, by who and where, never what was said", async () => {
    const { dm } = await setup(eventsOn)
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    await send(dm, adaTab, "Morning, Bo")
    vi.setSystemTime(new Date("2026-09-24T09:00:05.250Z"))
    await say(dm, boTab, { type: "dm-read", seq: 1 })
    // Not further: nothing new.
    await say(dm, boTab, { type: "dm-read", seq: 1 })

    const [message] = dm.sql<{ id: string }>`SELECT id FROM dm_messages`
    const where = {
      organization_id: org,
      conversation: { kind: "direct", id: dmName },
    }
    const eventId = expect.stringMatching(/^evt_[0-9a-f]{32}$/) as unknown
    expect(outbox(dm)).toEqual([
      {
        id: eventId,
        type: posted,
        reader_id: null,
        detail: {
          ...where,
          participant_user_ids: [ada, bo],
          message: { id: message.id, seq: 1 },
          author: { kind: "member", user_id: ada },
          posted_at: "2026-09-24T09:00:00.000Z",
        },
      },
      {
        id: eventId,
        type: read,
        reader_id: bo,
        detail: {
          ...where,
          reader_user_id: bo,
          read_through_seq: 1,
          read_at: "2026-09-24T09:00:05.250Z",
        },
      },
    ])
    expect(JSON.stringify(outbox(dm))).not.toContain("Morning")

    await runDueJobs(dm)
    expect(go.taken.map((event) => event.type)).toEqual([posted, read])
    expect(outbox(dm)).toEqual([])
  })

  it("records no read of its own for a message's author, whose event says they read through it, and records their read of the other's", async () => {
    const { dm } = await setup(eventsOn)
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    await send(dm, adaTab, "one")
    // Sending reads it through for Ada, and both pages are told, as ever.
    expect(dm.sql`SELECT user_id, seq FROM dm_reads`).toEqual([
      { user_id: ada, seq: 1 },
    ])
    expect(boTab.last("dm-read")).toEqual({
      type: "dm-read",
      userId: ada,
      seq: 1,
    })
    await send(dm, boTab, "two")
    await say(dm, adaTab, { type: "dm-read", seq: 2 })
    expect(
      outbox(dm).map((row) => [
        row.type,
        row.type === read
          ? row.detail.reader_user_id
          : (row.detail.author as { user_id: string }).user_id,
        row.type === read
          ? row.detail.read_through_seq
          : (row.detail.message as { seq: number }).seq,
      ])
    ).toEqual([
      [posted, ada, 1],
      [posted, bo, 2],
      [read, ada, 2],
    ])
  })

  it("stores a message and its event as one: when the event cannot be written, neither is kept", async () => {
    const { dm } = await setup(eventsOn)
    const adaTab = await join(dm, ada)
    vi.spyOn(console, "error").mockImplementation(() => {})
    // The outbox refuses its rows, as a full disk would, once the message is
    // written.
    dm.sql`CREATE TRIGGER outbox_full BEFORE INSERT ON event_outbox
      BEGIN SELECT RAISE(ABORT, 'database or disk is full'); END`

    const clientId = await send(dm, adaTab, "Lost?")
    expect(adaTab.last("dm-send-failed")).toMatchObject({
      clientId,
      code: "unavailable",
    })
    expect(messagesOf(adaTab)).toEqual([])
    expect(dm.sql`SELECT * FROM dm_messages`).toEqual([])
    expect(dm.sql`SELECT * FROM dm_reads`).toEqual([])
    expect(dm.sql`SELECT * FROM dm_meta WHERE key = 'conversation'`).toEqual([])
    expect(outbox(dm)).toEqual([])

    // Once it can be written, the page's retry keeps both.
    dm.sql`DROP TRIGGER outbox_full`
    await send(dm, adaTab, "Lost?", clientId)
    expect(dm.sql`SELECT seq FROM dm_messages`).toEqual([{ seq: 1 }])
    expect(dm.sql`SELECT user_id, seq FROM dm_reads`).toEqual([
      { user_id: ada, seq: 1 },
    ])
    expect(outbox(dm).map((row) => row.type)).toEqual([posted])
  })

  it("advances a read and records it as one: when the event cannot be written, the read stays where it was", async () => {
    const { dm } = await setup(eventsOn)
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    await send(dm, adaTab, "one")
    dm.sql`CREATE TRIGGER outbox_full BEFORE INSERT ON event_outbox
      BEGIN SELECT RAISE(ABORT, 'database or disk is full'); END`
    await expect(say(dm, boTab, { type: "dm-read", seq: 1 })).rejects.toThrow(
      "database or disk is full"
    )
    expect(dm.sql`SELECT user_id, seq FROM dm_reads ORDER BY user_id`).toEqual([
      { user_id: ada, seq: 1 },
    ])
    expect(outbox(dm).filter((row) => row.reader_id === bo)).toEqual([])
  })

  it("keeps one read of each reader waiting: a newer one takes its place, under a new id", async () => {
    const { dm } = await setup(eventsOn)
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    for (const body of ["one", "two", "three"]) await send(dm, adaTab, body)
    await say(dm, boTab, { type: "dm-read", seq: 1 })
    const first = outbox(dm).find((row) => row.reader_id === bo)
    await say(dm, boTab, { type: "dm-read", seq: 2 })
    await say(dm, boTab, { type: "dm-read", seq: 3 })

    const reads = outbox(dm).filter((row) => row.type === read)
    expect(
      reads.map((row) => [row.reader_id, row.detail.read_through_seq])
    ).toEqual([[bo, 3]])
    expect(reads.find((row) => row.reader_id === bo)?.id).not.toBe(first?.id)

    await runDueJobs(dm)
    expect(
      go.taken.map((event) =>
        event.type === read
          ? [event.detail.reader_user_id, event.detail.read_through_seq]
          : (event.detail.message as { seq: number }).seq
      )
    ).toEqual([1, 2, 3, [bo, 3]])
  })

  it("sends a newer read after the one on its way, whatever Go answers that one", async () => {
    // What each answer leaves of the request: taken, waiting again, or
    // refused. Bo's read through one was on it either way.
    const outcomes = [
      { answer: 204, waiting: [], refused: [] },
      { answer: 503, waiting: [[posted, 1]], refused: [] },
      {
        answer: 400,
        waiting: [],
        refused: [
          [posted, 1],
          [read, 1],
        ],
      },
    ]
    const described = (row: {
      type: string
      detail: Record<string, unknown>
    }) => [
      row.type,
      row.type === read
        ? row.detail.read_through_seq
        : (row.detail.message as { seq: number }).seq,
    ]
    for (const [index, { answer, waiting, refused }] of outcomes.entries()) {
      // Each on a whole second, when its drain is due at once.
      vi.setSystemTime(new Date(`2026-09-24T09:0${index}:00.000Z`))
      go = new FakeGoEvents()
      vi.stubGlobal("fetch", routeFetch(go))
      const { dm } = await setup(eventsOn)
      const adaTab = await join(dm, ada)
      const boTab = await join(dm, bo)
      await send(dm, adaTab, "one")
      await say(dm, boTab, { type: "dm-read", seq: 1 })
      const release = go.hold()
      const draining = runDueJobs(dm)
      await vi.waitFor(() => expect(go.requests).toHaveLength(1))

      // While Bo's read through one is on its way, he reads further.
      await send(dm, adaTab, "two")
      await say(dm, boTab, { type: "dm-read", seq: 2 })
      go.answers.push(answer)
      release()
      await draining
      expect(outbox(dm).map(described)).toEqual([
        ...waiting,
        [posted, 2],
        [read, 2],
      ])
      expect(
        dm.sql<{
          type: string
          detail: string
        }>`SELECT type, detail FROM event_dead_letters ORDER BY rowid`.map(
          (row) =>
            described({
              type: row.type,
              detail: JSON.parse(row.detail) as Record<string, unknown>,
            })
        )
      ).toEqual(refused)
      // The next drain, whenever Go's answer set it.
      const [next] = dm.jobs.filter((job) => job.callback === "drainEvents")
      vi.setSystemTime(next.time * 1000)
      await runDueJobs(dm)
      // The newer read goes, and only once.
      expect(
        go.taken
          .filter((event) => event.type === read)
          .map((event) => event.detail.read_through_seq)
      ).toEqual(answer === 204 ? [1, 2] : [2])
      expect(outbox(dm)).toEqual([])
    }
  })

  it("never gives a seq twice when a purge takes the newest messages away, and a read Go was told of covers none that come after", async () => {
    const { dm, world } = await setup(eventsOn)
    const adaOld = await join(dm, ada)
    await send(dm, adaOld, "one")
    // Ada left at 09:00:30. Bo writes after, and so does her page from
    // before; Bo reads through all of it, and Go is told.
    vi.setSystemTime(new Date("2026-09-24T09:01:00.000Z"))
    const boNew = await join(dm, bo)
    await send(dm, boNew, "two")
    await send(dm, adaOld, "stale")
    await say(dm, boNew, { type: "dm-read", seq: 3 })
    await runDueJobs(dm)

    await dm.purge({ userId: ada, removedAt: "2026-09-24T09:00:30.000Z" })
    // One and three went: two, the newest now, stays.
    expect(dm.sql`SELECT seq FROM dm_messages`).toEqual([{ seq: 2 }])

    // Ada is back, on a page opened since: her message takes a seq newer
    // than any Go was told of, and is unread for Bo.
    vi.setSystemTime(new Date("2026-09-24T09:02:00.000Z"))
    const adaNew = await join(dm, ada)
    await send(dm, adaNew, "back")
    expect(messagesOf(adaNew).at(-1)).toMatchObject({ seq: 4, body: "back" })
    expect((await inboxOf(world, bo))[0].unread).toBe(1)
    await runDueJobs(dm)
    expect(
      go.taken
        .filter((event) => event.type === posted)
        .map((event) => (event.detail.message as { seq: number }).seq)
    ).toEqual([1, 2, 3, 4])
  })

  it("keeps both reads where they were when a purge leaves nothing, so Go never sees a read go back", async () => {
    const { dm } = await setup(eventsOn)
    const adaOld = await join(dm, ada)
    const boOld = await join(dm, bo)
    for (const body of ["one", "two", "three"]) await send(dm, adaOld, body)
    await say(dm, boOld, { type: "dm-read", seq: 3 })
    vi.setSystemTime(new Date("2026-09-24T09:01:00.000Z"))
    const boNew = await join(dm, bo)
    await runDueJobs(dm)

    expect(
      await dm.purge({ userId: ada, removedAt: "2026-09-24T09:00:30.000Z" })
    ).toEqual({ emptied: false })
    expect(dm.sql`SELECT * FROM dm_messages`).toEqual([])
    expect(dm.sql`SELECT user_id, seq FROM dm_reads ORDER BY user_id`).toEqual([
      { user_id: ada, seq: 3 },
      { user_id: bo, seq: 3 },
    ])
    // A page that still says an old seq moves nothing.
    await say(dm, boNew, { type: "dm-read", seq: 2 })

    // Ada is back: her message is newer than both reads, and Bo reads it.
    vi.setSystemTime(new Date("2026-09-24T09:02:00.000Z"))
    const adaNew = await join(dm, ada)
    await send(dm, adaNew, "back")
    await say(dm, boNew, { type: "dm-read", seq: 4 })
    await runDueJobs(dm)
    expect(
      go.taken
        .filter(
          (event) => event.type === read && event.detail.reader_user_id === bo
        )
        .map((event) => event.detail.read_through_seq)
    ).toEqual([3, 4])
  })

  it("records nothing, and sends nothing, while Go's origin or the secret is unset, or the secret is too short", async () => {
    for (const vars of [
      {},
      { ORYXA_API_ORIGIN: eventsOrigin },
      { REALTIME_PURGE_SECRET: eventsSecret },
      { ORYXA_API_ORIGIN: eventsOrigin, REALTIME_PURGE_SECRET: " \n" },
      // A character short of the 32 Go takes: nothing Go would refuse waits.
      {
        ORYXA_API_ORIGIN: eventsOrigin,
        REALTIME_PURGE_SECRET: eventsSecret.slice(1),
      },
    ]) {
      const { dm } = await setup(vars)
      const adaTab = await join(dm, ada)
      const boTab = await join(dm, bo)
      await send(dm, adaTab, "hello")
      await say(dm, boTab, { type: "dm-read", seq: 1 })
      expect(dm.sql`SELECT seq FROM dm_messages`).toEqual([{ seq: 1 }])
      expect(outbox(dm)).toEqual([])
      expect(dm.jobs.filter((job) => job.callback === "drainEvents")).toEqual(
        []
      )
    }
    expect(go.requests).toEqual([])
  })
})

describe("DirectMessageAgent: requests", () => {
  /** A request Ada made of Bo's agent, a minute before the tests begin, less what `overrides` change. */
  function note(overrides: Partial<RequestNote> = {}): RequestNote {
    return {
      requestId: "irq_1",
      revision: 1,
      askerId: ada,
      ownerId: bo,
      origin: "agent",
      state: "waiting",
      changedAt: "2026-09-24T08:59:00.000Z",
      ...overrides,
    }
  }

  const line = (
    overrides: Record<string, unknown> = {}
  ): Record<string, unknown> => ({
    requestId: "irq_1",
    revision: 1,
    askerId: ada,
    ownerId: bo,
    origin: "agent",
    state: "waiting",
    changedAt: "2026-09-24T08:59:00.000Z",
    at: "2026-09-24T08:59:00.000Z",
    ...overrides,
  })

  it("keeps one note for each request at its highest revision, and tells every socket the line as it is new or changes", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    const adaPanel = await join(dm, ada)
    const boTab = await join(dm, bo)
    for (const socket of [adaTab, adaPanel, boTab]) socket.clear()

    expect(await dm.noteRequest(note())).toBe("noted")
    // Told twice, or late, or after a later one: nothing changes, nobody is told.
    expect(await dm.noteRequest(note())).toBe("repeated")
    expect(
      await dm.noteRequest(
        note({
          revision: 3,
          state: "answered",
          changedAt: "2026-09-24T08:59:50.000Z",
        })
      )
    ).toBe("noted")
    expect(
      await dm.noteRequest(
        note({
          revision: 2,
          state: "approved",
          changedAt: "2026-09-24T08:59:30.000Z",
        })
      )
    ).toBe("repeated")
    expect(await dm.noteRequest(note({ revision: 3, state: "held" }))).toBe(
      "repeated"
    )

    // The line's place stays where the request first was noted; its state and time move.
    const answered = line({
      revision: 3,
      state: "answered",
      changedAt: "2026-09-24T08:59:50.000Z",
    })
    for (const socket of [adaTab, adaPanel, boTab]) {
      expect(socket.of("dm-request")).toEqual([
        { type: "dm-request", request: line() },
        { type: "dm-request", request: answered },
      ])
    }
    expect(
      dm.sql`SELECT request_id, revision, state, first_at, changed_at FROM dm_requests`
    ).toEqual([
      {
        request_id: "irq_1",
        revision: 3,
        state: "answered",
        first_at: "2026-09-24T08:59:00.000Z",
        changed_at: "2026-09-24T08:59:50.000Z",
      },
    ])
  })

  it("sends a socket that opens the requests it holds, oldest first, and none of what anyone said", async () => {
    const { dm } = await setup()
    const empty = await join(dm, ada)
    expect(empty.last("dm-snapshot")).not.toHaveProperty("requests")

    await dm.noteRequest(
      note({ requestId: "irq_b", changedAt: "2026-09-24T08:58:00.000Z" })
    )
    await dm.noteRequest(
      note({
        requestId: "irq_a",
        askerId: bo,
        ownerId: ada,
        origin: "slack",
        changedAt: "2026-09-24T08:50:00.000Z",
      })
    )
    const late = await join(dm, bo)
    expect(late.last("dm-snapshot")?.requests).toEqual([
      line({
        requestId: "irq_a",
        askerId: bo,
        ownerId: ada,
        origin: "slack",
        changedAt: "2026-09-24T08:50:00.000Z",
        at: "2026-09-24T08:50:00.000Z",
      }),
      line({
        requestId: "irq_b",
        changedAt: "2026-09-24T08:58:00.000Z",
        at: "2026-09-24T08:58:00.000Z",
      }),
    ])
    // A note has no message's fields: no body, no author, no seq.
    expect(JSON.stringify(late.last("dm-snapshot")?.requests)).not.toMatch(
      /"body"|"seq"|"authorId"/
    )
  })

  it("is no message: no seq, no unread, no read, and nothing for Go", async () => {
    const go = new FakeGoEvents()
    vi.stubGlobal("fetch", routeFetch(go))
    try {
      const { dm, world } = await setup({
        ORYXA_API_ORIGIN: eventsOrigin,
        REALTIME_PURGE_SECRET: eventsSecret,
      })
      const adaTab = await join(dm, ada)
      adaTab.clear()
      await dm.noteRequest(note())
      await runDueJobs(dm)

      expect(dm.sql`SELECT * FROM dm_messages`).toEqual([])
      expect(dm.sql`SELECT * FROM dm_reads`).toEqual([])
      expect(dm.jobs.filter((job) => job.callback === "drainEvents")).toEqual(
        []
      )
      expect(go.taken).toEqual([])
      // Nothing was said to either inbox about a message, either.
      expect(world.callsTo("InboxAgent", "noteMessage")).toEqual([])
      expect(world.callsTo("InboxAgent", "markRead")).toEqual([])
      // A message after the note is the conversation's first: seq 1.
      await send(dm, adaTab, "hello")
      expect(messagesOf(adaTab)).toEqual([expect.objectContaining({ seq: 1 })])
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it("keeps at most DM_REQUEST_LIMIT, the latest by where their lines stand", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    for (let index = 0; index < DM_REQUEST_LIMIT + 5; index += 1) {
      await dm.noteRequest(
        note({
          requestId: `irq_${String(index).padStart(3, "0")}`,
          changedAt: new Date(
            Date.parse("2026-09-24T08:00:00.000Z") + index * 1000
          ).toISOString(),
        })
      )
    }
    const kept = dm.sql<{
      request_id: string
    }>`SELECT request_id FROM dm_requests ORDER BY first_at`.map(
      (row) => row.request_id
    )
    expect(kept).toHaveLength(DM_REQUEST_LIMIT)
    expect(kept[0]).toBe("irq_005")
    expect(kept.at(-1)).toBe(
      `irq_${String(DM_REQUEST_LIMIT + 4).padStart(3, "0")}`
    )
    // A request that fell past the limit as it arrived is kept, and told, by no one.
    adaTab.clear()
    expect(
      await dm.noteRequest(
        note({
          requestId: "irq_ancient",
          changedAt: "2026-09-24T07:00:00.000Z",
        })
      )
    ).toBe("noted")
    expect(adaTab.of("dm-request")).toEqual([])
    expect(dm.sql`SELECT * FROM dm_requests`).toHaveLength(DM_REQUEST_LIMIT)
  })

  it("keeps its two people's requests apart from a message's order, and a socket sees them beside the messages", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    await send(dm, adaTab, "before")
    vi.setSystemTime(new Date("2026-09-24T09:01:00.000Z"))
    await dm.noteRequest(note({ changedAt: "2026-09-24T09:00:30.000Z" }))
    await send(dm, adaTab, "after")
    const snapshot = (await join(dm, bo)).last("dm-snapshot")
    expect(
      (snapshot?.messages as Array<{ seq: number; body: string }>).map(
        (message) => [message.seq, message.body]
      )
    ).toEqual([
      [1, "before"],
      [2, "after"],
    ])
    expect(snapshot?.requests).toEqual([
      line({
        changedAt: "2026-09-24T09:00:30.000Z",
        at: "2026-09-24T09:00:30.000Z",
      }),
    ])
  })

  it("refuses a note that is not its own, read as whatever arrived over RPC", async () => {
    const { dm } = await setup()
    for (const bad of [
      note({ ownerId: "user-cy" }),
      note({ askerId: "user-cy" }),
      note({ ownerId: ada }),
      note({ revision: 0 }),
      note({ revision: 1.5 }),
      note({ requestId: "irq~1" }),
      note({ origin: "Slack" }),
      note({ state: "expired" as RequestNote["state"] }),
      note({ changedAt: "2026-09-24T08:59:00Z" }),
      undefined as unknown as RequestNote,
      "irq_1" as unknown as RequestNote,
    ]) {
      await expect(dm.noteRequest(bad)).rejects.toThrow(TypeError)
    }
    expect(dm.sql`SELECT * FROM dm_requests`).toEqual([])
  })

  it("is emptied of its notes with the messages when the organization goes", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    await send(dm, adaTab, "one")
    await dm.noteRequest(note())
    await dm.purge()
    expect(dm.sql`SELECT * FROM dm_requests`).toEqual([])
    const again = await join(dm, bo)
    expect(again.last("dm-snapshot")).not.toHaveProperty("requests")
  })

  describe("when one of its people left", () => {
    const removedAt = "2026-09-24T09:00:30.000Z"

    it("takes the notes whose lines stand at or before then, and empties itself when nothing else stays", async () => {
      const { dm, world } = await setup()
      // Only a note, with nobody having opened the conversation.
      await dm.noteRequest(note({ changedAt: "2026-09-24T09:00:10.000Z" }))
      // The inboxes were told directly, by the Worker.
      const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, bo))
      await inbox.noteRequest(
        { kind: "dm", name: dmName, peerId: ada },
        note({ changedAt: "2026-09-24T09:00:10.000Z" })
      )
      vi.setSystemTime(new Date("2026-09-24T09:05:00.000Z"))

      expect(await dm.purge({ userId: ada, removedAt })).toEqual({
        emptied: true,
      })
      expect(dm.sql`SELECT * FROM dm_requests`).toEqual([])
    })

    it("keeps a note newer than then, as a new run, restates both inboxes by it, and sends the sockets opened since", async () => {
      const { dm, world } = await setup()
      const boOld = await join(dm, bo)
      await send(dm, boOld, "before")
      await dm.noteRequest(
        note({ requestId: "irq_old", changedAt: "2026-09-24T09:00:10.000Z" })
      )
      // Ada leaves at 09:00:30 and is back at 09:01, asked again since.
      vi.setSystemTime(new Date("2026-09-24T09:01:00.000Z"))
      const adaNew = await join(dm, ada)
      await dm.noteRequest(
        note({
          requestId: "irq_new",
          askerId: bo,
          ownerId: ada,
          revision: 2,
          state: "held",
          changedAt: "2026-09-24T09:01:00.000Z",
        })
      )
      adaNew.clear()

      expect(await dm.purge({ userId: ada, removedAt })).toEqual({
        emptied: false,
      })

      // The old request and the old message went; the new request stays.
      expect(dm.sql`SELECT request_id, first_at FROM dm_requests`).toEqual([
        { request_id: "irq_new", first_at: "2026-09-24T09:01:00.000Z" },
      ])
      expect(dm.sql`SELECT * FROM dm_messages`).toEqual([])
      expect(boOld.sent.at(-1)).toEqual({ type: "dm-gone" })
      expect(adaNew.closed).toBeUndefined()
      const run = adaNew.last("dm-snapshot")
      expect(run?.requests).toEqual([
        line({
          requestId: "irq_new",
          askerId: bo,
          ownerId: ada,
          revision: 2,
          state: "held",
          changedAt: "2026-09-24T09:01:00.000Z",
          at: "2026-09-24T09:01:00.000Z",
        }),
      ])
      // Both inboxes say what stays: the request alone, listed by it.
      expect(await inboxOf(world, ada)).toEqual([
        {
          kind: "dm",
          name: dmName,
          peerId: bo,
          lastMessage: {
            authorId: bo,
            body: "Asked your agent · held",
            createdAt: "2026-09-24T09:01:00.000Z",
          },
          unread: 0,
          updatedAt: "2026-09-24T09:01:00.000Z",
        },
      ])
      expect((await inboxOf(world, bo))[0].lastMessage?.body).toBe(
        "You asked their agent · held"
      )
    })
  })
})
