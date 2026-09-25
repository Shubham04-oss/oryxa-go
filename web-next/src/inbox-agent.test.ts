import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { DirectMessageAgent } from "./direct-message-agent"
import { INBOX_SNAPSHOT_LIMIT, InboxAgent } from "./inbox-agent"
import type { ConversationEntry, NotedMessage } from "./inbox-agent"
import {
  directMessageName,
  inboxName,
  sideConversationName,
} from "./lib/dms/names"
import { MAX_UNREAD, conversationGoneCloseCode } from "./lib/dms/protocol"
import { RealtimeRegistryAgent, purgeMemoryMs } from "./realtime-registry-agent"
import { SideConversationAgent } from "./side-conversation-agent"
import {
  World,
  drain,
  openSocket,
  runDueJobs,
  say,
  settle,
} from "./test/durable-objects"
import type { Harness } from "./test/durable-objects"

vi.mock(
  "agents",
  async () => (await import("./test/durable-objects")).agentsModule
)

const org = "org_sim"
const ada = "user-ada"
const bo = "user-bo"
const cy = "user-cy"

type Inbox = InboxAgent & Harness
type Registry = RealtimeRegistryAgent & Harness
type Dm = DirectMessageAgent & Harness

function makeWorld() {
  return new World({
    DirectMessageAgent,
    SideConversationAgent,
    InboxAgent,
    RealtimeRegistryAgent,
  } as unknown as ConstructorParameters<typeof World>[0])
}

/** Ada's inbox, as the tests reach it. */
async function setup() {
  const world = makeWorld()
  const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, ada))
  return { world, inbox }
}

function dmWith(peerId: string): ConversationEntry {
  return { kind: "dm", name: directMessageName(org, ada, peerId), peerId }
}

/** Ada's side conversation with `peerId` in a teamspace. */
function sideWith(
  peerId: string,
  teamspaceId = "tsp_launch"
): ConversationEntry {
  return {
    kind: "side",
    name: sideConversationName(org, teamspaceId, ada, peerId),
    peerId,
    teamspaceId,
  }
}

function message(
  seq: number,
  authorId: string,
  body = `line ${seq}`,
  createdAt = `2026-09-24T09:00:${String(seq).padStart(2, "0")}.000Z`
): NotedMessage {
  return { seq, authorId, body, createdAt }
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(new Date("2026-09-24T10:00:00.000Z"))
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe("InboxAgent: sockets", () => {
  it("opens only for its own member, sends the list, and ignores what a page says", async () => {
    const { inbox } = await setup()
    await inbox.noteMessage(dmWith(bo), message(1, bo, "Hi Ada"))

    const refusals: Array<Record<string, string>> = [
      { "X-Oryxa-User": bo },
      { "X-Oryxa-User": "" },
      { "X-Oryxa-Organization": "org_other" },
    ]
    for (const headers of refusals) {
      const refused = await openSocket(
        inbox,
        { organizationId: org, userId: ada },
        headers
      )
      expect(refused.closed).toEqual({ code: 1008, reason: "Unauthorized" })
      expect(refused.sent).toEqual([])
    }

    const socket = await openSocket(inbox, { organizationId: org, userId: ada })
    expect(socket.sent).toEqual([
      {
        type: "inbox-snapshot",
        conversations: [
          {
            kind: "dm",
            name: directMessageName(org, ada, bo),
            peerId: bo,
            lastMessage: {
              authorId: bo,
              body: "Hi Ada",
              createdAt: "2026-09-24T09:00:01.000Z",
            },
            unread: 1,
            updatedAt: "2026-09-24T09:00:01.000Z",
          },
        ],
        unreadTotal: 1,
      },
    ])
    socket.clear()
    await say(inbox, socket, { type: "inbox-remove", name: "anything" })
    await say(inbox, socket, { type: "cf_agent_state", state: {} })
    expect(socket.sent).toEqual([])
    expect(await inbox.listConversations()).toHaveLength(1)
  })

  it("sends at most INBOX_SNAPSHOT_LIMIT conversations, the latest first, with every unread counted", async () => {
    const { inbox, world } = await setup()
    vi.spyOn(console, "error").mockImplementation(() => {})
    world.failing.add(org) // no registry: not what this test is about
    for (let index = 0; index <= INBOX_SNAPSHOT_LIMIT; index += 1) {
      const peer = `user-p${String(index).padStart(4, "0")}`
      await inbox.noteMessage(
        dmWith(peer),
        message(
          1,
          peer,
          "hello",
          new Date(Date.UTC(2026, 8, 1) + index * 1000).toISOString()
        )
      )
    }
    const socket = await openSocket(inbox, { organizationId: org, userId: ada })
    const [snapshot] = socket.of("inbox-snapshot")
    const conversations = snapshot.conversations as Array<{ peerId: string }>
    expect(conversations).toHaveLength(INBOX_SNAPSHOT_LIMIT)
    expect(conversations[0].peerId).toBe(
      `user-p${String(INBOX_SNAPSHOT_LIMIT).padStart(4, "0")}`
    )
    expect(snapshot.unreadTotal).toBe(INBOX_SNAPSHOT_LIMIT + 1)
  })
})

describe("InboxAgent: what the conversations tell it", () => {
  it("counts messages from others after what the member read, once each, whatever the order they arrive in", async () => {
    const { inbox } = await setup()
    const socket = await openSocket(inbox, { organizationId: org, userId: ada })
    socket.clear()
    const bosDm = dmWith(bo)

    await inbox.noteMessage(bosDm, message(1, bo), message(2, bo))
    await inbox.noteMessage(bosDm, message(2, bo)) // told twice
    await inbox.noteMessage(bosDm, message(4, bo))
    await inbox.markRead(bosDm.name, 2)
    await inbox.noteMessage(bosDm, message(3, bo)) // late, after the read
    await inbox.noteMessage(bosDm, message(1, bo)) // old, already read
    expect(await inbox.listConversations()).toEqual([
      expect.objectContaining({
        unread: 2,
        lastMessage: expect.objectContaining({ body: "line 4" }) as unknown,
        updatedAt: "2026-09-24T09:00:04.000Z",
      }),
    ])

    // The member's own message reads through it.
    await inbox.noteMessage(bosDm, message(5, ada, "On it"))
    expect(await inbox.listConversations()).toEqual([
      expect.objectContaining({
        unread: 0,
        lastMessage: {
          authorId: ada,
          body: "On it",
          createdAt: "2026-09-24T09:00:05.000Z",
        },
      }),
    ])
    expect(
      socket.of("inbox-conversation").map((update) => update.unreadTotal)
    ).toEqual([2, 2, 3, 1, 2, 2, 0])
  })

  it("previews a message on one line, lists the latest conversation first, and totals the unread", async () => {
    const { inbox } = await setup()
    await inbox.noteMessage(
      dmWith(bo),
      message(1, bo, `First line\n\n  second ${"word ".repeat(60)}`)
    )
    await inbox.noteMessage(
      dmWith(cy),
      message(1, cy, "Later", "2026-09-24T09:30:00.000Z")
    )
    const [latest, earlier] = await inbox.listConversations()
    expect(latest.peerId).toBe(cy)
    expect(earlier.lastMessage?.body).toMatch(/^First line second word word/)
    expect(earlier.lastMessage?.body).not.toContain("\n")
    expect(Array.from(earlier.lastMessage?.body ?? "")).toHaveLength(160)
    expect(earlier.lastMessage?.body.endsWith("…")).toBe(true)
  })

  it(`counts at most ${MAX_UNREAD} unread messages in a conversation`, async () => {
    const { inbox } = await setup()
    const notes = Array.from({ length: MAX_UNREAD + 5 }, (_, index) =>
      message(index + 1, bo, "spam", "2026-09-24T09:00:00.000Z")
    )
    await inbox.noteMessage(dmWith(bo), ...notes)
    expect((await inbox.listConversations())[0].unread).toBe(MAX_UNREAD)
    await inbox.markRead(directMessageName(org, ada, bo), MAX_UNREAD)
    expect((await inbox.listConversations())[0].unread).toBe(5)
  })

  it("leaves a read of a conversation it does not list alone", async () => {
    const { inbox } = await setup()
    await inbox.markRead(directMessageName(org, ada, bo), 3)
    expect(await inbox.listConversations()).toEqual([])
  })

  it("lists a side conversation with its teamspace before its first message, and takes a conversation off", async () => {
    const { inbox } = await setup()
    const socket = await openSocket(inbox, { organizationId: org, userId: ada })
    const side: ConversationEntry = {
      kind: "side",
      name: sideConversationName(org, "tsp_launch", bo, ada),
      peerId: bo,
      teamspaceId: "tsp_launch",
      teamspaceName: "Launch room",
    }
    await inbox.upsertConversation(side)
    await inbox.upsertConversation({ ...side, teamspaceName: undefined })
    expect(await inbox.listConversations()).toEqual([
      {
        kind: "side",
        name: "org_sim~tsp_launch~user-ada~user-bo",
        peerId: bo,
        teamspaceId: "tsp_launch",
        teamspaceName: "Launch room",
        unread: 0,
        updatedAt: "2026-09-24T10:00:00.000Z",
      },
    ])

    await inbox.noteMessage(dmWith(cy), message(1, cy))
    await inbox.removeConversation(dmWith(cy).name)
    await inbox.removeConversation(dmWith(cy).name)
    expect(socket.of("inbox-removed")).toEqual([
      { type: "inbox-removed", name: dmWith(cy).name, unreadTotal: 0 },
    ])
    expect(await inbox.listConversations()).toHaveLength(1)
  })

  it("refuses a conversation that is not its member's, or a message its people did not write", async () => {
    const { inbox } = await setup()
    const refused: Array<ConversationEntry> = [
      // Not Ada's.
      { kind: "dm", name: directMessageName(org, bo, cy), peerId: bo },
      // Another organization's.
      { kind: "dm", name: directMessageName("org_other", ada, bo), peerId: bo },
      // The wrong other person.
      { kind: "dm", name: directMessageName(org, ada, bo), peerId: cy },
      { kind: "dm", name: directMessageName(org, ada, bo), peerId: ada },
      // Not a name at all.
      { kind: "dm", name: "org_sim~user-bo~user-ada", peerId: bo },
      {
        kind: "dm",
        name: directMessageName(org, ada, bo),
        peerId: bo,
        teamspaceId: "tsp_launch",
      },
      {
        kind: "side",
        name: sideConversationName(org, "tsp_launch", ada, bo),
        peerId: bo,
        teamspaceId: "tsp_other",
      },
      {
        kind: "side",
        name: sideConversationName(org, "tsp_launch", ada, bo),
        peerId: bo,
        teamspaceId: "tsp_launch",
        teamspaceName: "x".repeat(201),
      },
      {
        kind: "group" as "dm",
        name: directMessageName(org, ada, bo),
        peerId: bo,
      },
    ]
    for (const entry of refused) {
      await expect(inbox.noteMessage(entry, message(1, bo))).rejects.toThrow(
        TypeError
      )
      await expect(inbox.upsertConversation(entry)).rejects.toThrow(TypeError)
    }
    for (const note of [
      message(1, cy),
      message(0, bo),
      message(1.5, bo),
      { ...message(1, bo), createdAt: "yesterday" },
      { ...message(1, bo), authorId: "" },
    ]) {
      await expect(inbox.noteMessage(dmWith(bo), note)).rejects.toThrow(
        TypeError
      )
    }
    await expect(
      inbox.markRead(directMessageName(org, ada, bo), -1)
    ).rejects.toThrow(TypeError)
    expect(await inbox.listConversations()).toEqual([])
  })
})

describe("InboxAgent: conversations it knows but does not list", () => {
  it("keeps one unseen, registers for it, and forgets it with the conversation", async () => {
    const { inbox, world } = await setup()
    const socket = await openSocket(inbox, { organizationId: org, userId: ada })
    socket.clear()
    await inbox.knowConversation(dmWith(bo))
    await inbox.knowConversation(dmWith(bo))
    expect(await inbox.listConversations()).toEqual([])
    expect(socket.sent).toEqual([])
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([ada])
    await expect(
      inbox.knowConversation({
        kind: "dm",
        name: directMessageName(org, bo, cy),
        peerId: bo,
      })
    ).rejects.toThrow(TypeError)

    await inbox.removeConversation(dmWith(bo).name)
    expect(inbox.sql`SELECT * FROM inbox_known`).toEqual([])
    expect(socket.sent).toEqual([])
  })

  it("purges one it only knows, and one that arrives while it purges the others", async () => {
    const world = makeWorld()
    const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, ada))
    await inbox.noteMessage(dmWith(bo), message(1, bo))
    await inbox.knowConversation(dmWith(cy))
    const release = world.hold(directMessageName(org, ada, bo))
    const purging = inbox.purge()
    await drain()
    // A conversation opens meanwhile.
    await inbox.knowConversation(dmWith("user-dee"))
    release()
    await purging

    expect(
      world
        .callsTo("DirectMessageAgent", "purge")
        .map((call) => call.name)
        .sort()
    ).toEqual(
      [
        directMessageName(org, ada, bo),
        directMessageName(org, ada, cy),
        directMessageName(org, ada, "user-dee"),
      ].sort()
    )
    expect(inbox.sql`SELECT * FROM inbox_known`).toEqual([])
    expect(await inbox.listConversations()).toEqual([])
  })
})

describe("InboxAgent: the organization's registry", () => {
  it("registers with its first conversation, once", async () => {
    const { inbox, world } = await setup()
    await inbox.noteMessage(dmWith(bo), message(1, bo))
    await inbox.noteMessage(dmWith(cy), message(1, cy))
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([ada])
    expect(world.callsTo("RealtimeRegistryAgent", "register")).toHaveLength(1)
  })

  it("registers again on a schedule when the registry could not be reached", async () => {
    const { inbox, world } = await setup()
    vi.spyOn(console, "error").mockImplementation(() => {})
    world.failing.add(org)
    await inbox.noteMessage(dmWith(bo), message(1, bo))
    expect(inbox.jobs.map((job) => job.callback)).toEqual(["retryRegistration"])

    world.failing.delete(org)
    vi.advanceTimersByTime(30_000)
    await runDueJobs(inbox)
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([ada])
    expect(inbox.jobs).toEqual([])
  })

  it("does not remember a registration a purge overtook, and registers again with its next conversation", async () => {
    const { inbox, world } = await setup()
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    // The registry stores Ada's registration, but its answer is slow to
    // come back.
    let answer!: () => void
    const answered = new Promise<void>((resolve) => {
      answer = resolve
    })
    const register = RealtimeRegistryAgent.prototype.register
    vi.spyOn(
      RealtimeRegistryAgent.prototype,
      "register"
    ).mockImplementationOnce(async function (
      this: RealtimeRegistryAgent,
      userId: string
    ) {
      await register.call(this, userId)
      await answered
    })
    const noting = inbox.noteMessage(dmWith(bo), message(1, bo))
    await drain()
    expect(await registry.members()).toEqual([ada])

    // Meanwhile Ada leaves: her purge empties the inbox, and the registry
    // lets her go.
    expect(
      await registry.purge("evt_ada_left", {
        scope: "member",
        userId: ada,
        removedAt: "2026-09-24T10:00:00.000Z",
      })
    ).toBe("purged")
    expect(await registry.members()).toEqual([])
    answer()
    await noting
    expect(
      inbox.sql`SELECT value FROM inbox_meta WHERE key = 'registered'`
    ).toEqual([])

    // She is let back in and talks again: an organization's purge finds her.
    await inbox.noteMessage(dmWith(cy), message(1, cy))
    expect(await registry.members()).toEqual([ada])
  })

  it("registers again on a schedule for a conversation it only knows", async () => {
    const { inbox, world } = await setup()
    vi.spyOn(console, "error").mockImplementation(() => {})
    world.failing.add(org)
    await inbox.knowConversation(dmWith(bo))
    expect(inbox.jobs.map((job) => job.callback)).toEqual(["retryRegistration"])

    world.failing.delete(org)
    vi.advanceTimersByTime(30_000)
    await runDueJobs(inbox)
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([ada])
  })

  it("tries a registration that failed again when its member opens the inbox", async () => {
    const { inbox, world } = await setup()
    vi.spyOn(console, "error").mockImplementation(() => {})
    world.failing.add(org)
    await inbox.noteMessage(dmWith(bo), message(1, bo))
    world.failing.delete(org)

    // Before its schedule comes round.
    const socket = await openSocket(inbox, { organizationId: org, userId: ada })
    expect(socket.of("inbox-snapshot")).toHaveLength(1)
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([ada])
    expect(
      inbox.sql`SELECT value FROM inbox_meta WHERE key = 'registered'`
    ).toEqual([{ value: "1" }])

    // An inbox with no conversation has nothing to register.
    const cysInbox = await world.object<Inbox>("InboxAgent", inboxName(org, cy))
    await openSocket(cysInbox, { organizationId: org, userId: cy })
    expect(await registry.members()).toEqual([ada])
  })

  it("schedules a registration that failed with nothing scheduled once it starts again", async () => {
    const { inbox, world } = await setup()
    vi.spyOn(console, "error").mockImplementation(() => {})
    world.failing.add(org)
    vi.spyOn(inbox, "schedule").mockRejectedValueOnce(
      new Error("storage unavailable")
    )
    await expect(inbox.noteMessage(dmWith(bo), message(1, bo))).rejects.toThrow(
      "storage unavailable"
    )
    expect(inbox.jobs).toEqual([])
    world.failing.delete(org)

    const restarted = await world.restart<Inbox>(
      "InboxAgent",
      inboxName(org, ada)
    )
    // Scheduled, not tried: a start may be for a purge.
    expect(restarted.jobs.map((job) => job.callback)).toEqual([
      "retryRegistration",
    ])
    expect(world.callsTo("RealtimeRegistryAgent", "register")).toEqual([])
    vi.advanceTimersByTime(30_000)
    await runDueJobs(restarted)
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([ada])
  })

  it("never registers for a purge, even when its start scheduled a registration: emptied, it lets the schedule go", async () => {
    const { inbox, world } = await setup()
    vi.spyOn(console, "error").mockImplementation(() => {})
    world.failing.add(org)
    vi.spyOn(inbox, "schedule").mockRejectedValueOnce(
      new Error("storage unavailable")
    )
    await expect(inbox.noteMessage(dmWith(bo), message(1, bo))).rejects.toThrow(
      "storage unavailable"
    )
    world.failing.delete(org)

    // The purge starts it again, then empties it.
    const restarted = await world.restart<Inbox>(
      "InboxAgent",
      inboxName(org, ada)
    )
    expect(
      await restarted.purge({ removedAt: "2026-09-24T10:00:00.000Z" })
    ).toEqual({ emptied: true })
    expect(restarted.jobs).toEqual([])
    vi.advanceTimersByTime(30_000)
    await runDueJobs(restarted)
    expect(world.callsTo("RealtimeRegistryAgent", "register")).toEqual([])
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([])
  })
})

describe("InboxAgent: a conversation restated after a purge", () => {
  it("takes its object's word for the latest message, the read and what is unread", async () => {
    const { inbox } = await setup()
    const socket = await openSocket(inbox, { organizationId: org, userId: ada })
    await inbox.noteMessage(dmWith(bo), message(1, bo), message(2, bo))
    socket.clear()

    await inbox.restateConversation(dmWith(bo), {
      last: message(5, bo, "what stays", "2026-09-24T09:30:00.000Z"),
      readSeq: 3,
      unread: [4, 5],
    })
    expect(await inbox.listConversations()).toEqual([
      {
        kind: "dm",
        name: dmWith(bo).name,
        peerId: bo,
        lastMessage: {
          authorId: bo,
          body: "what stays",
          createdAt: "2026-09-24T09:30:00.000Z",
        },
        unread: 2,
        updatedAt: "2026-09-24T09:30:00.000Z",
      },
    ])
    expect(socket.last("inbox-conversation")?.unreadTotal).toBe(2)
    // What comes after goes on from there.
    await inbox.noteMessage(dmWith(bo), message(6, bo))
    expect((await inbox.listConversations())[0].unread).toBe(3)
  })

  it("keeps one with nothing left known, not listed", async () => {
    const { inbox } = await setup()
    const socket = await openSocket(inbox, { organizationId: org, userId: ada })
    await inbox.noteMessage(dmWith(bo), message(1, bo))
    socket.clear()
    await inbox.restateConversation(dmWith(bo), { readSeq: 0, unread: [] })
    expect(await inbox.listConversations()).toEqual([])
    expect(inbox.sql`SELECT name FROM inbox_known`).toEqual([
      { name: dmWith(bo).name },
    ])
    expect(socket.of("inbox-removed")).toEqual([
      { type: "inbox-removed", name: dmWith(bo).name, unreadTotal: 0 },
    ])
  })

  it("refuses a restatement that does not add up", async () => {
    const { inbox } = await setup()
    for (const state of [
      { readSeq: -1, unread: [] },
      { readSeq: 3, unread: [2] },
      { readSeq: 0, unread: [1.5] },
      { last: message(1, cy), readSeq: 0, unread: [] },
    ]) {
      await expect(
        inbox.restateConversation(dmWith(bo), state)
      ).rejects.toThrow(TypeError)
    }
  })
})

describe("InboxAgent: purge", () => {
  it("purging one member who left keeps what is newer, and ends only the sockets from before", async () => {
    const world = makeWorld()
    const adaBo = directMessageName(org, ada, bo)
    const adaCy = directMessageName(org, ada, cy)
    // 10:00: Ada talks with Bo and with Cy, and has her inbox open.
    const toBo = await world.object<Dm>("DirectMessageAgent", adaBo)
    const toBoSocket = await openSocket(toBo, {
      organizationId: org,
      userId: ada,
    })
    await say(toBo, toBoSocket, {
      type: "dm-send",
      clientId: "client-bo-1",
      body: "old",
    })
    const toCy = await world.object<Dm>("DirectMessageAgent", adaCy)
    const toCySocket = await openSocket(toCy, {
      organizationId: org,
      userId: ada,
    })
    await say(toCy, toCySocket, {
      type: "dm-send",
      clientId: "client-cy-1",
      body: "old",
    })
    const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, ada))
    const oldList = await openSocket(inbox, {
      organizationId: org,
      userId: ada,
    })
    // She leaves at 10:00:30 and is back at 10:01, writing to Bo again.
    vi.setSystemTime(new Date("2026-09-24T10:01:00.000Z"))
    const toBoAgain = await openSocket(toBo, {
      organizationId: org,
      userId: ada,
    })
    await say(toBo, toBoAgain, {
      type: "dm-send",
      clientId: "client-bo-2",
      body: "new",
    })
    const newList = await openSocket(inbox, {
      organizationId: org,
      userId: ada,
    })
    newList.clear()

    expect(
      await inbox.purge({ removedAt: "2026-09-24T10:00:30.000Z" })
    ).toEqual({
      emptied: false,
    })

    expect((await inbox.listConversations()).map((c) => c.name)).toEqual([
      adaBo,
    ])
    const cysInbox = await world.object<Inbox>("InboxAgent", inboxName(org, cy))
    expect(await cysInbox.listConversations()).toEqual([])
    expect(toCy.sql`SELECT * FROM dm_messages`).toEqual([])
    expect(toBo.sql`SELECT body FROM dm_messages`).toEqual([{ body: "new" }])
    expect(oldList.sent.at(-1)).toEqual({ type: "inbox-gone" })
    expect(oldList.closed?.code).toBe(conversationGoneCloseCode)
    expect(newList.closed).toBeUndefined()
    expect(newList.last("inbox-snapshot")).toEqual(
      expect.objectContaining({
        conversations: [expect.objectContaining({ name: adaBo })],
      })
    )
    // Still registered: something of hers stays.
    expect(
      inbox.sql`SELECT value FROM inbox_meta WHERE key = 'registered'`
    ).toEqual([{ value: "1" }])
  })

  it("empties each conversation, takes it off the other person's inbox, then empties itself and closes its sockets", async () => {
    const world = makeWorld()
    const adaBo = directMessageName(org, ada, bo)
    const adaCy = directMessageName(org, ada, cy)
    const boCy = directMessageName(org, bo, cy)
    for (const [name, author, peer] of [
      [adaBo, ada, bo],
      [adaCy, cy, ada],
      [boCy, bo, cy],
    ]) {
      const dm = await world.object<Dm>("DirectMessageAgent", name)
      const socket = await openSocket(dm, {
        organizationId: org,
        userId: author,
      })
      await say(dm, socket, {
        type: "dm-send",
        clientId: `client-${author}`,
        body: `from ${author} to ${peer}`,
      })
    }
    const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, ada))
    const socket = await openSocket(inbox, { organizationId: org, userId: ada })

    await inbox.purge()

    for (const name of [adaBo, adaCy]) {
      const dm = await world.object<Dm>("DirectMessageAgent", name)
      expect(dm.sql`SELECT * FROM dm_messages`).toEqual([])
    }
    const untouched = await world.object<Dm>("DirectMessageAgent", boCy)
    expect(untouched.sql`SELECT body FROM dm_messages`).toEqual([
      { body: `from ${bo} to ${cy}` },
    ])
    const bosInbox = await world.object<Inbox>("InboxAgent", inboxName(org, bo))
    const cysInbox = await world.object<Inbox>("InboxAgent", inboxName(org, cy))
    expect((await bosInbox.listConversations()).map((c) => c.name)).toEqual([
      boCy,
    ])
    expect((await cysInbox.listConversations()).map((c) => c.name)).toEqual([
      boCy,
    ])
    expect(await inbox.listConversations()).toEqual([])
    expect(inbox.sql`SELECT * FROM inbox_meta`).toEqual([])
    expect(socket.sent.at(-1)).toEqual({ type: "inbox-gone" })
    expect(socket.closed).toEqual({
      code: conversationGoneCloseCode,
      reason: "Gone",
    })
  })

  it("keeps what it could not purge listed and throws, so purging again finishes", async () => {
    const world = makeWorld()
    vi.spyOn(console, "error").mockImplementation(() => {})
    for (const peer of [bo, cy]) {
      const dm = await world.object<Dm>(
        "DirectMessageAgent",
        directMessageName(org, ada, peer)
      )
      const socket = await openSocket(dm, { organizationId: org, userId: peer })
      await say(dm, socket, {
        type: "dm-send",
        clientId: `client-${peer}`,
        body: "hello",
      })
    }
    const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, ada))
    world.failing.add(directMessageName(org, ada, cy))

    await expect(inbox.purge()).rejects.toThrow(
      "1 conversations could not be purged"
    )
    expect((await inbox.listConversations()).map((c) => c.peerId)).toEqual([cy])

    world.failing.clear()
    await inbox.purge()
    expect(await inbox.listConversations()).toEqual([])
    const cysInbox = await world.object<Inbox>("InboxAgent", inboxName(org, cy))
    expect(await cysInbox.listConversations()).toEqual([])
  })
})

describe("InboxAgent: a teamspace's purge", () => {
  const removedAt = "2026-09-24T10:00:00.000Z"

  /**
   * Ada's inbox: two side conversations in the launch teamspace (one
   * listed, with Bo, whose inbox lists it too, and one only known, with Cy),
   * one with Bo in another teamspace, and a DM with Bo. Her inbox is open.
   */
  async function adaInTwoTeamspaces() {
    const world = makeWorld()
    const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, ada))
    await inbox.noteMessage(sideWith(bo), message(1, bo, "launch, with Bo"))
    await inbox.knowConversation(sideWith(cy))
    await inbox.noteMessage(
      sideWith(bo, "tsp_design"),
      message(1, bo, "design, with Bo")
    )
    await inbox.noteMessage(dmWith(bo), message(1, bo, "a DM"))
    const bosInbox = await world.object<Inbox>("InboxAgent", inboxName(org, bo))
    await bosInbox.noteMessage(
      { ...sideWith(bo), peerId: ada },
      message(1, bo, "launch, with Bo")
    )
    const socket = await openSocket(inbox, { organizationId: org, userId: ada })
    socket.clear()
    return { world, inbox, bosInbox, socket }
  }

  /** The names an inbox lists, sorted. */
  async function names(inbox: Inbox) {
    return (await inbox.listConversations()).map((c) => c.name).sort()
  }

  /**
   * The next purge of the side conversation `name` never answers: the
   * inbox's work is cut off there (a deploy, an eviction), and nothing is
   * raised. Resolves once it is cut off.
   */
  function cutOffSidePurge(name: string): Promise<void> {
    const purgeSide = SideConversationAgent.prototype.purge
    let cut = false
    return new Promise((resolve) => {
      vi.spyOn(SideConversationAgent.prototype, "purge").mockImplementation(
        function (
          this: SideConversationAgent,
          ...args: Parameters<SideConversationAgent["purge"]>
        ) {
          if (cut || this.name !== name) return purgeSide.apply(this, args)
          cut = true
          resolve()
          return new Promise<never>(() => {})
        }
      )
    })
  }

  /** What the inbox remembers of the removals it carried out. */
  function removals(inbox: Inbox) {
    return inbox.sql`SELECT teamspace_id, removed_at FROM inbox_teamspace_removals`
  }

  it("purges only that teamspace's side conversations, by its member's rule, and keeps the inbox, its sockets and its registration", async () => {
    const { world, inbox, bosInbox, socket } = await adaInTwoTeamspaces()
    const purges = vi.spyOn(SideConversationAgent.prototype, "purge")

    expect(await inbox.purgeTeamspace("tsp_launch", removedAt)).toBe("purged")

    expect(
      world
        .callsTo("SideConversationAgent", "purge")
        .map((call) => call.name)
        .sort()
    ).toEqual([sideWith(bo).name, sideWith(cy).name].sort())
    expect(purges.mock.calls).toEqual([
      [{ userId: ada, removedAt }],
      [{ userId: ada, removedAt }],
    ])
    expect(world.callsTo("DirectMessageAgent")).toEqual([])
    expect(await names(inbox)).toEqual(
      [dmWith(bo).name, sideWith(bo, "tsp_design").name].sort()
    )
    expect(inbox.sql`SELECT * FROM inbox_known`).toEqual([])
    expect(await bosInbox.listConversations()).toEqual([])
    // The inbox stays open, told what went.
    expect(socket.closed).toBeUndefined()
    expect(socket.sent).toEqual([
      { type: "inbox-removed", name: sideWith(bo).name, unreadTotal: 2 },
    ])
    expect(
      inbox.sql`SELECT value FROM inbox_meta WHERE key = 'registered'`
    ).toEqual([{ value: "1" }])
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([ada, bo])
    expect(removals(inbox)).toEqual([
      { teamspace_id: "tsp_launch", removed_at: removedAt },
    ])
  })

  it("empties every side conversation of that teamspace without a removal, remembering nothing: a teamspace deleted", async () => {
    const { world, inbox, bosInbox } = await adaInTwoTeamspaces()
    const purges = vi.spyOn(SideConversationAgent.prototype, "purge")

    expect(await inbox.purgeTeamspace("tsp_launch")).toBe("purged")

    expect(purges.mock.calls).toEqual([[undefined], [undefined]])
    expect(world.callsTo("SideConversationAgent", "purge")).toHaveLength(2)
    expect(await names(inbox)).toEqual(
      [dmWith(bo).name, sideWith(bo, "tsp_design").name].sort()
    )
    expect(inbox.sql`SELECT * FROM inbox_known`).toEqual([])
    expect(await bosInbox.listConversations()).toEqual([])
    expect(removals(inbox)).toEqual([])
    // Sent again, it finds nothing left.
    expect(await inbox.purgeTeamspace("tsp_launch")).toBe("purged")
    expect(world.callsTo("SideConversationAgent", "purge")).toHaveLength(2)
  })

  it("carries out a removal once, whatever the event that brings it, and no earlier one; a later one, and one forgotten after a day, run", async () => {
    const { world, inbox } = await adaInTwoTeamspaces()
    await inbox.purgeTeamspace("tsp_launch", removedAt)
    const purged = world.callsTo("SideConversationAgent", "purge").length
    // Ada is back on the team, and Bo writes to her there again.
    await inbox.noteMessage(
      sideWith(bo),
      message(2, bo, "back", "2026-09-24T10:05:00.000Z")
    )

    // The same removal again (Go's retry, or a new event for it), or an
    // earlier one: nothing is purged again.
    for (const at of [removedAt, "2026-09-24T09:59:59.999Z"]) {
      expect(await inbox.purgeTeamspace("tsp_launch", at)).toBe("repeated")
    }
    expect(world.callsTo("SideConversationAgent", "purge")).toHaveLength(purged)
    expect(await names(inbox)).toContain(sideWith(bo).name)

    // A later removal is carried out, and remembered in its place.
    const later = "2026-09-24T10:10:00.000Z"
    expect(await inbox.purgeTeamspace("tsp_launch", later)).toBe("purged")
    expect(world.callsTo("SideConversationAgent", "purge")).toHaveLength(
      purged + 1
    )
    expect(removals(inbox)).toEqual([
      { teamspace_id: "tsp_launch", removed_at: later },
    ])

    // A day later it is forgotten: sent again, it is carried out.
    await inbox.noteMessage(
      sideWith(bo),
      message(3, bo, "again", "2026-09-24T10:15:00.000Z")
    )
    vi.setSystemTime(Date.now() + purgeMemoryMs + 1_000)
    expect(await inbox.purgeTeamspace("tsp_launch", later)).toBe("purged")
    expect(world.callsTo("SideConversationAgent", "purge")).toHaveLength(
      purged + 2
    )
  })

  it("compares the removal it remembers with a new one as instants, however the one it remembers is written", async () => {
    const { world, inbox } = await adaInTwoTeamspaces()
    // Remembered in a looser RFC 3339 form than it now writes: .9 s.
    inbox.sql`INSERT INTO inbox_teamspace_removals (teamspace_id, removed_at, done_at)
      VALUES ('tsp_launch', '2026-09-24T10:00:00.9Z', ${Date.now()})`

    // .95 s is later: it runs, and is remembered in the purge's own form.
    expect(
      await inbox.purgeTeamspace("tsp_launch", "2026-09-24T10:00:00.950Z")
    ).toBe("purged")
    expect(world.callsTo("SideConversationAgent", "purge")).toHaveLength(2)
    expect(removals(inbox)).toEqual([
      { teamspace_id: "tsp_launch", removed_at: "2026-09-24T10:00:00.950Z" },
    ])
    // .9 s, however written, is not.
    inbox.sql`UPDATE inbox_teamspace_removals SET removed_at = '2026-09-24T15:30:00.95+05:30'`
    expect(
      await inbox.purgeTeamspace("tsp_launch", "2026-09-24T10:00:00.900Z")
    ).toBe("repeated")
  })

  it("keeps the latest removal it carried out when an earlier one finishes after it", async () => {
    const { inbox } = await adaInTwoTeamspaces()
    const purgeSide = SideConversationAgent.prototype.purge
    let release!: () => void
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    let first = true
    vi.spyOn(SideConversationAgent.prototype, "purge").mockImplementation(
      async function (
        this: SideConversationAgent,
        ...args: Parameters<SideConversationAgent["purge"]>
      ) {
        if (first) {
          first = false
          await held
        }
        return purgeSide.apply(this, args)
      }
    )
    const earlier = inbox.purgeTeamspace("tsp_launch", removedAt)
    await drain()
    const later = "2026-09-24T10:10:00.000Z"
    expect(await inbox.purgeTeamspace("tsp_launch", later)).toBe("purged")
    release()
    expect(await earlier).toBe("purged")
    expect(removals(inbox)).toEqual([
      { teamspace_id: "tsp_launch", removed_at: later },
    ])
  })

  it("writes nothing and registers nobody for a teamspace it has nothing in", async () => {
    const { world, inbox } = await adaInTwoTeamspaces()
    expect(await inbox.purgeTeamspace("tsp_other", removedAt)).toBe("purged")
    expect(removals(inbox)).toEqual([])
    const cysInbox = await world.object<Inbox>("InboxAgent", inboxName(org, cy))
    for (const at of [removedAt, undefined]) {
      expect(await cysInbox.purgeTeamspace("tsp_launch", at)).toBe("purged")
    }
    expect(removals(cysInbox)).toEqual([])
    expect(cysInbox.sql`SELECT * FROM inbox_meta`).toEqual([])
    expect(cysInbox.jobs).toEqual([])
    expect(world.callsTo("SideConversationAgent")).toEqual([])
    expect(world.callsTo("RealtimeRegistryAgent", "register")).toHaveLength(2)
  })

  it("keeps what it could not purge and remembers nothing, so the same removal again finishes", async () => {
    const { world, inbox } = await adaInTwoTeamspaces()
    vi.spyOn(console, "error").mockImplementation(() => {})
    world.failing.add(sideWith(cy).name)

    await expect(inbox.purgeTeamspace("tsp_launch", removedAt)).rejects.toThrow(
      "1 conversations could not be purged"
    )
    expect(inbox.sql`SELECT name FROM inbox_known`).toEqual([
      { name: sideWith(cy).name },
    ])
    expect(removals(inbox)).toEqual([])

    world.failing.clear()
    expect(await inbox.purgeTeamspace("tsp_launch", removedAt)).toBe("purged")
    expect(inbox.sql`SELECT * FROM inbox_known`).toEqual([])
    expect(removals(inbox)).toEqual([
      { teamspace_id: "tsp_launch", removed_at: removedAt },
    ])
  })

  it("carries on after a restart cut it off, with only the conversations not yet done", async () => {
    const { world, inbox } = await adaInTwoTeamspaces()
    const cutOff = cutOffSidePurge(sideWith(cy).name)
    void inbox.purgeTeamspace("tsp_launch", removedAt)
    await cutOff
    await settle()
    expect(await names(inbox)).not.toContain(sideWith(bo).name)
    expect(removals(inbox)).toEqual([])

    const restarted = await world.restart<Inbox>(
      "InboxAgent",
      inboxName(org, ada)
    )
    const before = world.callsTo("SideConversationAgent", "purge").length
    expect(await restarted.purgeTeamspace("tsp_launch", removedAt)).toBe(
      "purged"
    )
    expect(
      world
        .callsTo("SideConversationAgent", "purge")
        .slice(before)
        .map((call) => call.name)
    ).toEqual([sideWith(cy).name])
    expect(restarted.sql`SELECT * FROM inbox_known`).toEqual([])
    expect(removals(restarted)).toEqual([
      { teamspace_id: "tsp_launch", removed_at: removedAt },
    ])
  })

  it("remembers nothing while the teamspace's conversations keep arriving, or one it emptied comes back", async () => {
    const { inbox } = await adaInTwoTeamspaces()
    const purgeSide = SideConversationAgent.prototype.purge
    // Each purge of one brings another, past every pass.
    let arrivals = 0
    const arriving = vi
      .spyOn(SideConversationAgent.prototype, "purge")
      .mockImplementation(async function (
        this: SideConversationAgent,
        ...args: Parameters<SideConversationAgent["purge"]>
      ) {
        const result = await purgeSide.apply(this, args)
        arrivals += 1
        await inbox.knowConversation(sideWith(`user-new-${arrivals}`))
        return result
      })
    await expect(inbox.purgeTeamspace("tsp_launch", removedAt)).rejects.toThrow(
      "side conversations kept arriving while the teamspace was purged"
    )
    expect(removals(inbox)).toEqual([])
    arriving.mockRestore()

    // Emptied and taken off, the conversation with Bo in the design
    // teamspace is noted again while the one with Cy there is purged.
    const design = sideWith(bo, "tsp_design")
    await inbox.knowConversation(sideWith(cy, "tsp_design"))
    const comingBack = vi
      .spyOn(SideConversationAgent.prototype, "purge")
      .mockImplementation(async function (
        this: SideConversationAgent,
        ...args: Parameters<SideConversationAgent["purge"]>
      ) {
        const result = await purgeSide.apply(this, args)
        if (this.name === sideWith(cy, "tsp_design").name) {
          await settle()
          expect(await names(inbox)).not.toContain(design.name)
          await inbox.noteMessage(design, message(2, bo, "still here"))
        }
        return result
      })
    await expect(inbox.purgeTeamspace("tsp_design")).rejects.toThrow(
      "side conversations kept arriving while the teamspace was purged"
    )
    comingBack.mockRestore()
    expect(await inbox.purgeTeamspace("tsp_design")).toBe("purged")
    expect(await names(inbox)).not.toContain(design.name)
  })

  it("refuses what is not a teamspace's purge", async () => {
    const { world, inbox } = await adaInTwoTeamspaces()
    for (const [teamspaceId, at] of [
      ["", removedAt],
      ["tsp~launch", removedAt],
      ["tsp launch", removedAt],
      ["x".repeat(129), removedAt],
      ["tsp_launch", "yesterday"],
      ["tsp_launch", ""],
      // Only the purge's own form: UTC, to the millisecond.
      ["tsp_launch", "2026-09-24T10:00:00Z"],
    ]) {
      await expect(inbox.purgeTeamspace(teamspaceId, at)).rejects.toThrow(
        TypeError
      )
    }
    expect(world.callsTo("SideConversationAgent")).toEqual([])
  })

  it("forgets the removals it carried out once a purge empties it", async () => {
    const { inbox } = await adaInTwoTeamspaces()
    await inbox.purgeTeamspace("tsp_launch", removedAt)
    expect(removals(inbox)).toHaveLength(1)
    expect(await inbox.purge()).toEqual({ emptied: true })
    expect(removals(inbox)).toEqual([])
  })
})
