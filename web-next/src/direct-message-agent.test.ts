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

function makeWorld() {
  return new World({
    DirectMessageAgent,
    InboxAgent,
    RealtimeRegistryAgent,
  } as unknown as ConstructorParameters<typeof World>[0])
}

async function setup() {
  const world = makeWorld()
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
    expect(socket.state).toEqual({ userId: bo, visible: false })
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
    for (const table of [
      "dm_messages",
      "dm_reads",
      "dm_inbox_sync",
      "dm_meta",
    ]) {
      expect(
        dm.sql([`SELECT * FROM ${table}`] as unknown as TemplateStringsArray)
      ).toEqual([])
    }
    await dm.purge()

    const again = await join(dm, ada)
    expect(again.last("dm-snapshot")).toEqual(
      expect.objectContaining({ conversation: "", messages: [], typing: [] })
    )
    await send(dm, again, "a new start")
    expect(messagesOf(again)).toEqual([expect.objectContaining({ seq: 1 })])
    const reopened = await join(dm, bo)
    expect(reopened.last("dm-snapshot")?.conversation).toMatch(/^dmc_/)
    expect(reopened.last("dm-snapshot")?.conversation).not.toBe(before)
  })
})
