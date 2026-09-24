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
import { RealtimeRegistryAgent } from "./realtime-registry-agent"
import { World, openSocket, runDueJobs, say } from "./test/durable-objects"
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
})

describe("InboxAgent: purge", () => {
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
