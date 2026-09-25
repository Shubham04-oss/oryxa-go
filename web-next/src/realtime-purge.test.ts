import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { DirectMessageAgent } from "./direct-message-agent"
import { InboxAgent } from "./inbox-agent"
import {
  directMessageName,
  inboxName,
  sideConversationName,
} from "./lib/dms/names"
import {
  REALTIME_PURGE_PATH,
  handlePurge,
  maxPurgeBodyBytes,
  parsePurgeRequest,
  purgeWindowSeconds,
  removalTime,
  signPurge,
} from "./realtime-purge"
import type { PurgeRequest } from "./realtime-purge"
import { RealtimeRegistryAgent, purgeMemoryMs } from "./realtime-registry-agent"
import { SideConversationAgent } from "./side-conversation-agent"
import { encodeRoster } from "./lib/side/protocol"
import { World, drain, openSocket, say, settle } from "./test/durable-objects"
import type { Harness } from "./test/durable-objects"
import type { ConversationEnv } from "./conversation-env"

vi.mock(
  "agents",
  async () => (await import("./test/durable-objects")).agentsModule
)

const secret = "0123456789abcdef0123456789abcdef"
const org = "org_sim"
const admin = "user-admin"
const member = "user-member"
const owner = "user-owner"

type Dm = DirectMessageAgent & Harness
type Inbox = InboxAgent & Harness
type Registry = RealtimeRegistryAgent & Harness

const now = new Date("2026-09-24T12:00:00.000Z").getTime()
const nowSeconds = Math.floor(now / 1000)

function makeWorld(
  vars: Record<string, unknown> = { REALTIME_PURGE_SECRET: secret }
) {
  return new World(
    {
      DirectMessageAgent,
      SideConversationAgent,
      InboxAgent,
      RealtimeRegistryAgent,
    } as unknown as ConstructorParameters<typeof World>[0],
    vars
  )
}

type Side = SideConversationAgent & Harness

/**
 * `from` says something to `to` in their side conversation in the
 * teamspace, over a socket of it, as a page does. Go is a stand-in with no
 * requests to read.
 */
async function sideTalk(
  world: World,
  from: string,
  to: string,
  body = "hello",
  teamspace = "tsp_launch"
) {
  const side = await world.object<Side>(
    "SideConversationAgent",
    sideConversationName(org, teamspace, from, to)
  )
  const socket = await openSocket(
    side,
    { organizationId: org, userId: from },
    {
      "X-Oryxa-Cookie": `oryxa_access=${from}`,
      "X-Oryxa-Side-Roster": encodeRoster({
        teamspaceName: "Launch room",
        paused: false,
        people: [],
        contributions: [],
      }),
    }
  )
  clientIds += 1
  await say(side, socket, {
    type: "side-send",
    clientId: `client-${String(clientIds).padStart(4, "0")}`,
    body,
  })
  return { side, socket }
}

let clientIds = 0

/** `from` says something to `to`, over a socket of the DM, as a page does. */
async function talk(world: World, from: string, to: string, body = "hello") {
  const dm = await world.object<Dm>(
    "DirectMessageAgent",
    directMessageName(org, from, to)
  )
  const socket = await openSocket(dm, { organizationId: org, userId: from })
  clientIds += 1
  await say(dm, socket, {
    type: "dm-send",
    clientId: `client-${String(clientIds).padStart(4, "0")}`,
    body,
  })
  return { dm, socket }
}

async function listed(world: World, userId: string) {
  const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, userId))
  return (await inbox.listConversations()).map(
    (conversation) => conversation.name
  )
}

function bodiesIn(world: World, userA: string, userB: string) {
  const dm = world.existing<Dm>(
    "DirectMessageAgent",
    directMessageName(org, userA, userB)
  )
  if (!dm) return []
  return dm.sql<{ body: string }>`SELECT body FROM dm_messages`.map(
    (row) => row.body
  )
}

type Signing = {
  secret?: string
  timestamp?: string | number
  signature?: string
  headers?: Record<string, string>
  method?: string
}

async function purgeRequest(
  body: PurgeRequest | string,
  {
    secret: key = secret,
    timestamp = nowSeconds,
    signature,
    headers = {},
    method = "POST",
  }: Signing = {}
) {
  const raw = typeof body === "string" ? body : JSON.stringify(body)
  return new Request(`http://localhost:3004${REALTIME_PURGE_PATH}`, {
    method,
    headers: {
      "Content-Type": "application/json",
      "X-Oryxa-Timestamp": String(timestamp),
      "X-Oryxa-Signature":
        signature ?? (await signPurge(key, String(timestamp), raw)),
      ...headers,
    },
    body: method === "GET" ? undefined : raw,
  })
}

async function purge(world: World, request: Request, at = now) {
  return handlePurge(request, world.env as unknown as ConversationEnv, at)
}

async function codeOf(response: Response) {
  const body: { error?: { code?: string } } = await response.json()
  return [response.status, body.error?.code]
}

/** The admin left at 12:00: everything the tests say before then is theirs to lose. */
const memberLeft: PurgeRequest = {
  event_id: "evt_member_removed_1",
  organization_id: org,
  scope: "member",
  user_id: admin,
  removed_at: "2026-09-24T12:00:00Z",
}

const organizationDeleted: PurgeRequest = {
  event_id: "evt_organization_deleted_1",
  organization_id: org,
  scope: "organization",
  user_id: "",
  removed_at: "2026-09-24T12:00:00Z",
}

/** Moves the clock on by `ms`. */
function later(ms: number) {
  vi.setSystemTime(Date.now() + ms)
}

/**
 * The next purge of `userId`'s inbox (`purge`, or `purgeTeamspace`) never
 * answers: the registry's work is cut off there (a deploy, an eviction, or
 * the runtime ending it once Go gave up), and nothing is raised. Later
 * purges of it run. Resolves once it is cut off, after the Worker's own
 * checks, which are not all promises.
 */
function cutOffInboxPurge(
  userId: string,
  method: "purge" | "purgeTeamspace" = "purge"
): Promise<void> {
  const purgeInbox = InboxAgent.prototype[method] as (
    this: InboxAgent,
    ...args: unknown[]
  ) => Promise<unknown>
  let cut = false
  return new Promise((resolve) => {
    vi.spyOn(InboxAgent.prototype, method).mockImplementation(function (
      this: InboxAgent,
      ...args: unknown[]
    ) {
      if (cut || this.name !== inboxName(org, userId)) {
        return purgeInbox.apply(this, args) as never
      }
      cut = true
      resolve()
      return new Promise<never>(() => {})
    })
  })
}

/** How many objects of `binding` were ever made. */
function made(world: World, binding: string) {
  return (world.env[binding] as { objects: Map<string, unknown> }).objects.size
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(now)
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe("the purge's signature", () => {
  it("refuses a request not signed with the secret, over this very body and timestamp", async () => {
    const world = makeWorld()
    await talk(world, admin, member)
    const raw = JSON.stringify(memberLeft)
    const signature = await signPurge(secret, String(nowSeconds), raw)
    const refusals: Array<[Request, string]> = [
      [
        await purgeRequest(memberLeft, { secret: `${secret}x` }),
        "invalid_signature",
      ],
      [
        await purgeRequest({ ...memberLeft, user_id: member }, { signature }),
        "invalid_signature",
      ],
      [
        await purgeRequest(memberLeft, {
          timestamp: nowSeconds - 1,
          signature,
        }),
        "invalid_signature",
      ],
      [
        await purgeRequest(memberLeft, { signature: signature.slice(3) }),
        "invalid_signature",
      ],
      [
        await purgeRequest(memberLeft, {
          signature: `v2=${signature.slice(3)}`,
        }),
        "invalid_signature",
      ],
      [
        await purgeRequest(memberLeft, { signature: `${signature}00` }),
        "invalid_signature",
      ],
      [
        await purgeRequest(memberLeft, {
          headers: { "X-Oryxa-Signature": "" },
        }),
        "unauthorized",
      ],
      [
        await purgeRequest(memberLeft, {
          headers: { "X-Oryxa-Timestamp": "" },
        }),
        "unauthorized",
      ],
    ]
    for (const [request, code] of refusals) {
      expect(await codeOf(await purge(world, request))).toEqual([401, code])
    }
    expect(bodiesIn(world, admin, member)).toEqual(["hello"])
    expect(world.callsTo("RealtimeRegistryAgent", "purge")).toEqual([])

    // Upper-case hex is the same signature.
    const upper = await purgeRequest(memberLeft, {
      signature: `v1=${signature.slice(3).toUpperCase()}`,
    })
    expect((await purge(world, upper)).status).toBe(204)
  })

  it(`takes a timestamp only within ${purgeWindowSeconds} seconds of now, either way`, async () => {
    const world = makeWorld()
    for (const timestamp of [
      nowSeconds - purgeWindowSeconds - 1,
      nowSeconds + purgeWindowSeconds + 1,
      "1e9",
      "-5",
      "0",
    ]) {
      const response = await purge(
        world,
        await purgeRequest(memberLeft, { timestamp })
      )
      expect(await codeOf(response)).toEqual([401, "stale_request"])
    }
    for (const timestamp of [
      nowSeconds - purgeWindowSeconds,
      nowSeconds + purgeWindowSeconds,
    ]) {
      const response = await purge(
        world,
        await purgeRequest(
          { ...memberLeft, event_id: `evt_${timestamp}` },
          { timestamp }
        )
      )
      expect(response.status).toBe(204)
    }
  })

  it("trims the Worker's secret as Go does, so one stored with a newline still verifies", async () => {
    for (const stored of [`${secret}\n`, `  ${secret}\r\n`, `\t${secret} `]) {
      const world = makeWorld({ REALTIME_PURGE_SECRET: stored })
      await talk(world, admin, member)
      const response = await purge(world, await purgeRequest(memberLeft))
      expect(response.status).toBe(204)
      expect(bodiesIn(world, admin, member)).toEqual([])
    }
  })

  it("refuses every purge while the Worker has no secret of at least 32 characters", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {})
    for (const vars of [
      {},
      { REALTIME_PURGE_SECRET: "" },
      { REALTIME_PURGE_SECRET: "short-secret" },
      // Its length is the trimmed one's.
      { REALTIME_PURGE_SECRET: " ".repeat(40) },
      { REALTIME_PURGE_SECRET: `short-secret${" ".repeat(30)}\n` },
    ]) {
      const world = makeWorld(vars)
      const response = await purge(world, await purgeRequest(memberLeft))
      expect(await codeOf(response)).toEqual([503, "unavailable"])
      expect(world.callsTo("RealtimeRegistryAgent", "purge")).toEqual([])
    }
  })
})

describe("the purge's request", () => {
  it("takes only a POST", async () => {
    const world = makeWorld()
    const response = await purge(
      world,
      await purgeRequest(memberLeft, { method: "GET" })
    )
    expect(response.status).toBe(405)
    expect(response.headers.get("Allow")).toBe("POST")
  })

  it("refuses a signed body that is not a purge: the scope decides, and user_id must agree with it", async () => {
    const world = makeWorld()
    await talk(world, admin, member)
    const { scope: _scope, ...withoutScope } = memberLeft
    const { removed_at: _removedAt, ...withoutRemovedAt } = memberLeft
    const { user_id: _userId, ...withoutUserId } = memberLeft
    for (const body of [
      "not json",
      "[]",
      // Contract v1: no scope, no removed_at.
      { event_id: "evt_1", organization_id: org, user_id: admin },
      { event_id: "evt_1", organization_id: org, user_id: "" },
      withoutScope,
      { ...withoutScope, user_id: "" },
      { ...memberLeft, scope: "team" },
      { ...memberLeft, scope: "Member" },
      // A member's purge names the member.
      { ...memberLeft, user_id: "" },
      withoutUserId,
      { ...memberLeft, user_id: null },
      { ...memberLeft, user_id: "user admin" },
      // An organization's names nobody.
      { ...organizationDeleted, user_id: admin },
      { ...organizationDeleted, user_id: undefined },
      withoutRemovedAt,
      { ...memberLeft, removed_at: "" },
      { ...memberLeft, removed_at: "yesterday" },
      { ...memberLeft, removed_at: 1790208000 },
      { ...memberLeft, removed_at: "2026-02-30T10:00:00Z" },
      { ...memberLeft, event_id: "" },
      { ...memberLeft, event_id: "x".repeat(201) },
      { ...memberLeft, organization_id: "org~sim" },
    ]) {
      expect(
        await codeOf(
          await purge(
            world,
            await purgeRequest(
              typeof body === "string" ? body : JSON.stringify(body)
            )
          )
        )
      ).toEqual([400, "invalid_request"])
    }
    expect(bodiesIn(world, admin, member)).toEqual(["hello"])
    expect(world.callsTo("RealtimeRegistryAgent", "purge")).toEqual([])
    for (const request of [memberLeft, organizationDeleted]) {
      expect(
        parsePurgeRequest(new TextEncoder().encode(JSON.stringify(request)))
      ).toEqual(request)
    }
  })

  it("refuses a body over 4 KB before checking anything in it", async () => {
    const world = makeWorld()
    const body = JSON.stringify({
      ...memberLeft,
      padding: "x".repeat(maxPurgeBodyBytes),
    })
    expect(await codeOf(await purge(world, await purgeRequest(body)))).toEqual([
      413,
      "too_large",
    ])
  })
})

describe("removed_at", () => {
  it("takes RFC 3339 with Z or an offset and 0 to 9 fractional digits, down to its millisecond", () => {
    for (const [value, instant] of [
      ["2026-09-24T10:00:00Z", "2026-09-24T10:00:00.000Z"],
      ["2026-09-24t10:00:00z", "2026-09-24T10:00:00.000Z"],
      ["2026-09-24T10:00:00.1Z", "2026-09-24T10:00:00.100Z"],
      ["2026-09-24T10:00:00.12Z", "2026-09-24T10:00:00.120Z"],
      ["2026-09-24T10:00:00.123Z", "2026-09-24T10:00:00.123Z"],
      ["2026-09-24T10:00:00.1234Z", "2026-09-24T10:00:00.123Z"],
      ["2026-09-24T10:00:00.123456789Z", "2026-09-24T10:00:00.123Z"],
      ["2026-09-24T10:00:00.999999999Z", "2026-09-24T10:00:00.999Z"],
      ["2026-09-24T15:30:00.5+05:30", "2026-09-24T10:00:00.500Z"],
      ["2026-09-24T05:00:00-05:00", "2026-09-24T10:00:00.000Z"],
      ["2024-02-29T23:59:59Z", "2024-02-29T23:59:59.000Z"],
    ]) {
      expect(removalTime(value)).toBe(instant)
    }
  })

  it("refuses what is not a real time in that form", () => {
    for (const value of [
      "2026-02-30T10:00:00Z",
      "2025-02-29T10:00:00Z",
      "2026-13-01T10:00:00Z",
      "2026-00-10T10:00:00Z",
      "2026-09-24T24:00:00Z",
      "2026-09-24T10:60:00Z",
      "2026-09-24T10:00:60Z",
      "2026-09-24T10:00:00+24:00",
      "2026-09-24T10:00:00.1234567890Z",
      "2026-09-24T10:00:00.Z",
      "2026-09-24T10:00:00",
      "2026-09-24 10:00:00Z",
      "2026-09-24T10:00Z",
      "2026-9-24T10:00:00Z",
      "1969-12-31T23:59:59Z",
      " 2026-09-24T10:00:00Z",
      "",
      undefined,
      1790208000,
    ]) {
      expect(removalTime(value)).toBeUndefined()
    }
  })
})

describe("purging", () => {
  it("purges a member: their DMs are emptied and leave both inboxes, their inbox empties, and the registry lets them go", async () => {
    const world = makeWorld()
    const { socket: adminSocket } = await talk(
      world,
      admin,
      member,
      "admin to member"
    )
    await talk(world, owner, admin, "owner to admin")
    await talk(world, member, owner, "member to owner")
    const adminsInbox = await world.object<Inbox>(
      "InboxAgent",
      inboxName(org, admin)
    )
    const adminList = await openSocket(adminsInbox, {
      organizationId: org,
      userId: admin,
    })
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([admin, member, owner])

    const response = await purge(world, await purgeRequest(memberLeft))

    expect(response.status).toBe(204)
    expect(bodiesIn(world, admin, member)).toEqual([])
    expect(bodiesIn(world, owner, admin)).toEqual([])
    expect(bodiesIn(world, member, owner)).toEqual(["member to owner"])
    expect(await listed(world, admin)).toEqual([])
    expect(await listed(world, member)).toEqual([
      directMessageName(org, member, owner),
    ])
    expect(await listed(world, owner)).toEqual([
      directMessageName(org, member, owner),
    ])
    expect(await registry.members()).toEqual([member, owner])
    expect(adminSocket.closed?.code).toBe(4404)
    expect(adminList.closed?.code).toBe(4404)
  })

  it("keeps what is newer than removed_at: that conversation stays, restated in both inboxes, and the member stays registered", async () => {
    const world = makeWorld()
    await talk(world, admin, member, "before")
    await talk(world, owner, admin, "old news")
    // The admin left at 12:00 and is back a minute later.
    later(60_000)
    await talk(world, admin, member, "after")
    const membersInbox = await world.object<Inbox>(
      "InboxAgent",
      inboxName(org, member)
    )
    expect((await membersInbox.listConversations())[0].unread).toBe(2)

    expect((await purge(world, await purgeRequest(memberLeft))).status).toBe(
      204
    )

    expect(bodiesIn(world, admin, member)).toEqual(["after"])
    expect(bodiesIn(world, owner, admin)).toEqual([])
    const conversation = directMessageName(org, admin, member)
    expect(await listed(world, admin)).toEqual([conversation])
    expect(await listed(world, owner)).toEqual([])
    expect(await membersInbox.listConversations()).toEqual([
      expect.objectContaining({
        name: conversation,
        lastMessage: expect.objectContaining({
          authorId: admin,
          body: "after",
        }) as unknown,
        unread: 1,
      }),
    ])
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toContain(admin)
  })

  it("reaches the side conversations a member is in, as their DMs: emptied, or restated with what is newer", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json({ requests: [] }))
    )
    const world = makeWorld({
      REALTIME_PURGE_SECRET: secret,
      ORYXA_API_ORIGIN: "http://go.test",
    })
    const { socket: adminSocket } = await sideTalk(
      world,
      admin,
      owner,
      "in the launch room, before"
    )
    await sideTalk(world, member, admin, "member, before")
    later(60_000)
    await sideTalk(world, member, admin, "member, after")
    const bodies = (userA: string, userB: string) => {
      const side = world.existing<Side>(
        "SideConversationAgent",
        sideConversationName(org, "tsp_launch", userA, userB)
      )
      if (!side) return undefined
      return side.sql<{
        body: string
      }>`SELECT body FROM side_items ORDER BY seq`.map((row) => row.body)
    }

    expect((await purge(world, await purgeRequest(memberLeft))).status).toBe(
      204
    )

    expect(bodies(admin, owner)).toEqual([])
    expect(adminSocket.closed?.code).toBe(4404)
    expect(await listed(world, owner)).toEqual([])
    const kept = sideConversationName(org, "tsp_launch", admin, member)
    expect(bodies(admin, member)).toEqual(["member, after"])
    expect(await listed(world, member)).toEqual([kept])
    expect(await listed(world, admin)).toEqual([kept])
    vi.unstubAllGlobals()
  })

  it("reaches a DM opened without a message: its sockets close, so the member who left cannot write in it", async () => {
    const world = makeWorld()
    const dm = await world.object<Dm>(
      "DirectMessageAgent",
      directMessageName(org, admin, member)
    )
    const adminSocket = await openSocket(dm, {
      organizationId: org,
      userId: admin,
    })
    const memberSocket = await openSocket(dm, {
      organizationId: org,
      userId: member,
    })

    expect((await purge(world, await purgeRequest(memberLeft))).status).toBe(
      204
    )
    for (const socket of [adminSocket, memberSocket]) {
      expect(socket.closed?.code).toBe(4404)
    }
    const membersInbox = await world.object<Inbox>(
      "InboxAgent",
      inboxName(org, member)
    )
    expect(membersInbox.sql`SELECT * FROM inbox_known`).toEqual([])
    expect(dm.sql`SELECT * FROM dm_meta`).toEqual([])
  })

  it("keeps a member found by the registry when their inbox registers again while it is purged", async () => {
    const world = makeWorld()
    await talk(world, admin, member)
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    const release = world.hold(inboxName(org, admin))
    const purging = registry.purge("evt_during", {
      scope: "member",
      userId: admin,
      removedAt: "2026-09-24T12:00:00.000Z",
    })
    await drain()
    await registry.register(admin)
    release()
    expect(await purging).toBe("purged")
    expect(await registry.members()).toEqual([admin, member])
  })

  it("purges a whole organization, newer messages too", async () => {
    const world = makeWorld()
    await talk(world, admin, member)
    await talk(world, owner, admin)
    later(60_000)
    await talk(world, member, owner)

    const response = await purge(world, await purgeRequest(organizationDeleted))

    expect(response.status).toBe(204)
    for (const [userA, userB] of [
      [admin, member],
      [owner, admin],
      [member, owner],
    ]) {
      expect(bodiesIn(world, userA, userB)).toEqual([])
    }
    for (const userId of [admin, member, owner]) {
      expect(await listed(world, userId)).toEqual([])
    }
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([])
  })

  it("does nothing for an event it has carried out, and a later purge keeps what came after its removal", async () => {
    const world = makeWorld()
    await talk(world, admin, member, "before")
    // The same request twice: its timestamp, body and signature.
    const request = await purgeRequest(memberLeft)
    const replay = await purgeRequest(memberLeft)
    expect(replay.headers.get("X-Oryxa-Signature")).toBe(
      request.headers.get("X-Oryxa-Signature")
    )
    expect((await purge(world, request)).status).toBe(204)

    // The admin is let back in and talks again.
    later(60_000)
    await talk(world, admin, member, "after")
    const inboxPurges = world.callsTo("InboxAgent", "purge").length
    expect((await purge(world, replay)).status).toBe(204)
    expect(world.callsTo("InboxAgent", "purge")).toHaveLength(inboxPurges)
    expect(bodiesIn(world, admin, member)).toEqual(["after"])

    // Another event for the same removal keeps what came after it.
    const retold = await purgeRequest(
      { ...memberLeft, event_id: "evt_member_removed_2" },
      { timestamp: Math.floor(Date.now() / 1000) }
    )
    expect((await purge(world, retold, Date.now())).status).toBe(204)
    expect(bodiesIn(world, admin, member)).toEqual(["after"])
    expect(await listed(world, member)).toEqual([
      directMessageName(org, admin, member),
    ])

    // A later removal takes it.
    later(60_000)
    const leftAgain = await purgeRequest(
      {
        ...memberLeft,
        event_id: "evt_member_removed_3",
        removed_at: new Date(Date.now()).toISOString(),
      },
      { timestamp: Math.floor(Date.now() / 1000) }
    )
    expect((await purge(world, leftAgain, Date.now())).status).toBe(204)
    expect(bodiesIn(world, admin, member)).toEqual([])
    expect(await listed(world, member)).toEqual([])
  })

  it(`forgets an event after ${purgeMemoryMs / 3_600_000} hours, and running it again keeps what came after`, async () => {
    const world = makeWorld()
    await talk(world, admin, member, "before")
    expect((await purge(world, await purgeRequest(memberLeft))).status).toBe(
      204
    )
    later(purgeMemoryMs + 1_000)
    await talk(world, admin, member, "a day later")
    const inboxPurges = world.callsTo("InboxAgent", "purge").length
    const response = await purge(
      world,
      await purgeRequest(memberLeft, {
        timestamp: Math.floor(Date.now() / 1000),
      }),
      Date.now()
    )
    expect(response.status).toBe(204)
    // Forgotten, so carried out again: with nothing of its removal left.
    expect(world.callsTo("InboxAgent", "purge")).toHaveLength(inboxPurges + 1)
    expect(bodiesIn(world, admin, member)).toEqual(["a day later"])
  })

  it("answers 503 when part of a purge failed, and finishes it when Go sends the event again", async () => {
    const world = makeWorld()
    vi.spyOn(console, "error").mockImplementation(() => {})
    await talk(world, admin, member)
    await talk(world, owner, admin)
    world.failing.add(directMessageName(org, owner, admin))

    const response = await purge(world, await purgeRequest(memberLeft))
    expect(await codeOf(response)).toEqual([503, "unavailable"])
    expect(bodiesIn(world, admin, member)).toEqual([])
    expect(bodiesIn(world, owner, admin)).toEqual(["hello"])
    expect(await listed(world, admin)).toEqual([
      directMessageName(org, owner, admin),
    ])
    // Still found by an organization's purge.
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toContain(admin)

    world.failing.clear()
    expect((await purge(world, await purgeRequest(memberLeft))).status).toBe(
      204
    )
    expect(bodiesIn(world, owner, admin)).toEqual([])
    expect(await listed(world, admin)).toEqual([])
    expect(await listed(world, owner)).toEqual([])
  })

  it("keeps a member registered until their inbox is purged, whether the purge fails part-way or is cut off, and the same event again purges them", async () => {
    const world = makeWorld()
    vi.spyOn(console, "error").mockImplementation(() => {})
    // The member's inbox was being reset as the admin wrote: only the
    // admin's inbox, and the registry through it, knows the DM.
    world.failing.add(inboxName(org, member))
    await talk(world, admin, member, "known through the admin only")
    world.failing.clear()
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([admin])
    const adminsInbox = await world.object<Inbox>(
      "InboxAgent",
      inboxName(org, admin)
    )
    const adminList = await openSocket(adminsInbox, {
      organizationId: org,
      userId: admin,
    })

    // The DM cannot be purged: the admin's purge fails part-way.
    world.failing.add(directMessageName(org, admin, member))
    expect(
      await codeOf(await purge(world, await purgeRequest(organizationDeleted)))
    ).toEqual([503, "unavailable"])
    expect(await registry.members()).toEqual([admin])
    world.failing.clear()

    // Sent again, it is cut off at the admin's inbox: no error, no answer.
    const cutOff = cutOffInboxPurge(admin)
    void purge(world, await purgeRequest(organizationDeleted))
    await cutOff
    await settle()
    expect(await registry.members()).toEqual([admin])
    expect(bodiesIn(world, admin, member)).toEqual([
      "known through the admin only",
    ])

    // Go heard nothing back and sends it again, to the registry restarted.
    const restarted = await world.restart<Registry>(
      "RealtimeRegistryAgent",
      org
    )
    expect(
      (await purge(world, await purgeRequest(organizationDeleted))).status
    ).toBe(204)
    expect(bodiesIn(world, admin, member)).toEqual([])
    expect(await listed(world, admin)).toEqual([])
    expect(adminList.closed?.code).toBe(4404)
    expect(await restarted.members()).toEqual([])
  })

  it("remembers an event only once every member is purged, and the same event again carries on with the members not yet done", async () => {
    const world = makeWorld()
    await talk(world, admin, member)
    await talk(world, member, owner)
    await talk(world, owner, admin)
    const cutOff = cutOffInboxPurge(owner)

    void purge(world, await purgeRequest(organizationDeleted))
    await cutOff
    await settle()
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(registry.sql`SELECT event_id FROM registry_purges`).toEqual([])
    // The admin and the member are done and let go; the owner is not.
    expect(await registry.members()).toEqual([owner])

    const restarted = await world.restart<Registry>(
      "RealtimeRegistryAgent",
      org
    )
    const before = world.callsTo("InboxAgent", "purge").length
    expect(
      (await purge(world, await purgeRequest(organizationDeleted))).status
    ).toBe(204)
    expect(
      world
        .callsTo("InboxAgent", "purge")
        .slice(before)
        .map((call) => call.name)
    ).toEqual([inboxName(org, owner)])
    expect(restarted.sql`SELECT event_id FROM registry_purges`).toEqual([
      { event_id: organizationDeleted.event_id },
    ])
    expect(await restarted.members()).toEqual([])

    // Done: the same event again does nothing.
    const done = world.callsTo("InboxAgent", "purge").length
    expect(
      (await purge(world, await purgeRequest(organizationDeleted))).status
    ).toBe(204)
    expect(world.callsTo("InboxAgent", "purge")).toHaveLength(done)
  })

  it("answers 204 to a member's purge after their organization's, registering nobody and making no conversation", async () => {
    const world = makeWorld()
    await talk(world, admin, member)
    expect(
      (await purge(world, await purgeRequest(organizationDeleted))).status
    ).toBe(204)
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    expect(await registry.members()).toEqual([])
    const registrations = world.callsTo("RealtimeRegistryAgent", "register")
    const conversations = [
      made(world, "DirectMessageAgent"),
      made(world, "SideConversationAgent"),
    ]
    const conversationCalls = [
      ...world.callsTo("DirectMessageAgent"),
      ...world.callsTo("SideConversationAgent"),
    ]

    // Go purges each member too: two whose inboxes the organization's purge
    // emptied, and one who never had an inbox.
    const never = "user-never"
    for (const userId of [admin, member, never]) {
      const response = await purge(
        world,
        await purgeRequest({
          ...memberLeft,
          event_id: `evt_member_removed_${userId}`,
          user_id: userId,
        })
      )
      expect(response.status).toBe(204)
    }

    expect(await registry.members()).toEqual([])
    expect(world.callsTo("RealtimeRegistryAgent", "register")).toEqual(
      registrations
    )
    expect([
      made(world, "DirectMessageAgent"),
      made(world, "SideConversationAgent"),
    ]).toEqual(conversations)
    expect([
      ...world.callsTo("DirectMessageAgent"),
      ...world.callsTo("SideConversationAgent"),
    ]).toEqual(conversationCalls)
    for (const userId of [admin, member, never]) {
      const inbox = await world.object<Inbox>(
        "InboxAgent",
        inboxName(org, userId)
      )
      expect(inbox.sql`SELECT * FROM inbox_meta`).toEqual([])
      expect(inbox.jobs).toEqual([])
    }
  })

  it("answers 503 when the organization's registry cannot be reached", async () => {
    const world = makeWorld()
    vi.spyOn(console, "error").mockImplementation(() => {})
    world.failing.add(org)
    const response = await purge(world, await purgeRequest(memberLeft))
    expect(await codeOf(response)).toEqual([503, "unavailable"])
  })
})

const launch = "tsp_launch"
const design = "tsp_design"

/** The bodies in two people's side conversation in a teamspace, oldest first; nothing for one never made. */
function sideBodies(
  world: World,
  teamspace: string,
  userA: string,
  userB: string
) {
  const side = world.existing<Side>(
    "SideConversationAgent",
    sideConversationName(org, teamspace, userA, userB)
  )
  if (!side) return undefined
  return side.sql<{
    body: string
  }>`SELECT body FROM side_items ORDER BY seq`.map((row) => row.body)
}

/** The events two people's side conversation in a teamspace still has to send Go. */
function outboxOf(
  world: World,
  teamspace: string,
  userA: string,
  userB: string
) {
  const side = world.existing<Side>(
    "SideConversationAgent",
    sideConversationName(org, teamspace, userA, userB)
  )
  if (!side) return []
  return side.sql<{ id: string }>`SELECT id FROM event_outbox`
}

/**
 * The three members' side conversations in the launch teamspace (the one
 * between the member and the owner written in after 12:00), the admin's
 * with the member in the design teamspace, and their DM, with Go's events
 * on. Go is a stand-in with no requests to read.
 */
async function teamspaceWorld() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => Response.json({ requests: [] }))
  )
  const world = makeWorld({
    REALTIME_PURGE_SECRET: secret,
    ORYXA_API_ORIGIN: "http://go.test",
  })
  const { socket: launchSocket } = await sideTalk(
    world,
    admin,
    member,
    "launch: admin to member"
  )
  await sideTalk(world, owner, admin, "launch: owner to admin")
  await sideTalk(world, admin, member, "design: admin to member", design)
  await talk(world, admin, member, "a DM")
  later(60_000)
  await sideTalk(world, member, owner, "launch: member to owner, later")
  const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
  return { world, registry, launchSocket }
}

/** The inboxes `method` was called on, from its `from`th call on, sorted. */
function inboxesCalled(world: World, method: string, from = 0) {
  return world
    .callsTo("InboxAgent", method)
    .slice(from)
    .map((call) => call.name)
    .sort()
}

describe("a teamspace's purge, through the registry", () => {
  it("empties every side conversation in the teamspace through every member's inbox, newer ones and their events too, and keeps every member registered", async () => {
    const { world, registry, launchSocket } = await teamspaceWorld()
    expect(await registry.members()).toEqual([admin, member, owner])
    expect(outboxOf(world, launch, admin, member)).toHaveLength(1)

    expect(await registry.purgeTeamspace("evt_launch_deleted", launch)).toBe(
      "purged"
    )

    for (const [userA, userB] of [
      [admin, member],
      [owner, admin],
      [member, owner],
    ]) {
      expect(sideBodies(world, launch, userA, userB)).toEqual([])
      expect(outboxOf(world, launch, userA, userB)).toEqual([])
    }
    expect(launchSocket.closed?.code).toBe(4404)
    expect(sideBodies(world, design, admin, member)).toEqual([
      "design: admin to member",
    ])
    expect(outboxOf(world, design, admin, member)).toHaveLength(1)
    expect(bodiesIn(world, admin, member)).toEqual(["a DM"])
    const kept = [
      sideConversationName(org, design, admin, member),
      directMessageName(org, admin, member),
    ].sort()
    expect((await listed(world, admin)).sort()).toEqual(kept)
    expect((await listed(world, member)).sort()).toEqual(kept)
    expect(await listed(world, owner)).toEqual([])
    expect(inboxesCalled(world, "purgeTeamspace")).toEqual(
      [admin, member, owner].map((user) => inboxName(org, user))
    )
    // Everybody is still in the organization: nobody leaves the registry,
    // no inbox forgets it registered, and the event's progress goes.
    expect(await registry.members()).toEqual([admin, member, owner])
    const ownersInbox = await world.object<Inbox>(
      "InboxAgent",
      inboxName(org, owner)
    )
    expect(
      ownersInbox.sql`SELECT value FROM inbox_meta WHERE key = 'registered'`
    ).toEqual([{ value: "1" }])
    expect(registry.sql`SELECT event_id FROM registry_purges`).toEqual([
      { event_id: "evt_launch_deleted" },
    ])
    expect(registry.sql`SELECT * FROM registry_purge_progress`).toEqual([])
  })

  it("remembers the event only once every member is done: cut off part-way, the same event again, to the registry restarted, carries on with the members not yet done", async () => {
    const { world, registry } = await teamspaceWorld()
    const cutOff = cutOffInboxPurge(owner, "purgeTeamspace")
    void registry.purgeTeamspace("evt_launch_deleted", launch)
    await cutOff
    await settle()
    expect(registry.sql`SELECT event_id FROM registry_purges`).toEqual([])
    expect(
      registry.sql`SELECT user_id FROM registry_purge_progress ORDER BY user_id`
    ).toEqual([{ user_id: admin }, { user_id: member }])
    expect(await registry.members()).toEqual([admin, member, owner])

    const restarted = await world.restart<Registry>(
      "RealtimeRegistryAgent",
      org
    )
    const before = world.callsTo("InboxAgent", "purgeTeamspace").length
    expect(await restarted.purgeTeamspace("evt_launch_deleted", launch)).toBe(
      "purged"
    )
    expect(inboxesCalled(world, "purgeTeamspace", before)).toEqual([
      inboxName(org, owner),
    ])
    expect(restarted.sql`SELECT event_id FROM registry_purges`).toEqual([
      { event_id: "evt_launch_deleted" },
    ])
    expect(restarted.sql`SELECT * FROM registry_purge_progress`).toEqual([])
    expect(await restarted.members()).toEqual([admin, member, owner])
    expect(await listed(world, owner)).toEqual([])

    // Done: the same event again does nothing.
    const done = world.callsTo("InboxAgent", "purgeTeamspace").length
    expect(await restarted.purgeTeamspace("evt_launch_deleted", launch)).toBe(
      "repeated"
    )
    expect(world.callsTo("InboxAgent", "purgeTeamspace")).toHaveLength(done)
  })

  it("throws when a member's side conversations could not all be purged, keeps the others' progress, and the same event again finishes the rest", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {})
    const { world, registry } = await teamspaceWorld()
    world.failing.add(sideConversationName(org, launch, member, owner))

    await expect(
      registry.purgeTeamspace("evt_launch_deleted", launch)
    ).rejects.toThrow(
      "2 of 3 members' side conversations in the teamspace were not purged"
    )
    expect(registry.sql`SELECT user_id FROM registry_purge_progress`).toEqual([
      { user_id: admin },
    ])
    expect(registry.sql`SELECT * FROM registry_purges`).toEqual([])
    expect(sideBodies(world, launch, member, owner)).toEqual([
      "launch: member to owner, later",
    ])

    world.failing.clear()
    const before = world.callsTo("InboxAgent", "purgeTeamspace").length
    expect(await registry.purgeTeamspace("evt_launch_deleted", launch)).toBe(
      "purged"
    )
    expect(inboxesCalled(world, "purgeTeamspace", before)).toEqual([
      inboxName(org, member),
      inboxName(org, owner),
    ])
    expect(sideBodies(world, launch, member, owner)).toEqual([])
    expect(await listed(world, owner)).toEqual([])
    expect(registry.sql`SELECT * FROM registry_purge_progress`).toEqual([])
    expect(await registry.members()).toEqual([admin, member, owner])
  })

  it("forgets what a purge under way had done after a day, and does those members again", async () => {
    const { world, registry } = await teamspaceWorld()
    const cutOff = cutOffInboxPurge(owner, "purgeTeamspace")
    void registry.purgeTeamspace("evt_launch_deleted", launch)
    await cutOff
    await settle()
    expect(registry.sql`SELECT * FROM registry_purge_progress`).toHaveLength(2)

    later(purgeMemoryMs + 1_000)
    const restarted = await world.restart<Registry>(
      "RealtimeRegistryAgent",
      org
    )
    const before = world.callsTo("InboxAgent", "purgeTeamspace").length
    expect(await restarted.purgeTeamspace("evt_launch_deleted", launch)).toBe(
      "purged"
    )
    expect(inboxesCalled(world, "purgeTeamspace", before)).toEqual(
      [admin, member, owner].map((user) => inboxName(org, user))
    )
    expect(restarted.sql`SELECT * FROM registry_purge_progress`).toEqual([])
  })

  it("refuses what is not a teamspace's purge", async () => {
    const world = makeWorld()
    await talk(world, admin, member)
    const registry = await world.object<Registry>("RealtimeRegistryAgent", org)
    for (const [eventId, teamspaceId] of [
      ["", launch],
      ["x".repeat(201), launch],
      ["evt_1", ""],
      ["evt_1", "tsp~launch"],
      ["evt_1", "x".repeat(129)],
    ]) {
      await expect(
        registry.purgeTeamspace(eventId, teamspaceId)
      ).rejects.toThrow(TypeError)
    }
    expect(world.callsTo("InboxAgent", "purgeTeamspace")).toEqual([])
  })
})

// The Go side's own vectors (the relay that turns auth.member.removed.v1 and
// auth.organization.deleted.v1 into purges), contract v2: the key is the
// secret's characters as written, never hex-decoded, and the signature is
// lowercase. Each was checked apart from this code, with openssl.
describe("Go's purge vectors", () => {
  const goSecret =
    "5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84"
  const goTimestamp = "1790208000"
  const goNow = Number(goTimestamp) * 1000
  const goOrganization = "org_2fQx9LmR7vTk4WcA"
  const removedUser = "6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f"
  const otherUser = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
  const vectors = {
    memberRemoved: {
      body: '{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"member","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f","removed_at":"2026-09-24T10:00:00Z"}',
      signature:
        "v1=ee12ee93bb00fb5e9901c4a0ac8ed193a85539bfb2c76241f9182f2fcc71b90e",
    },
    organizationDeleted: {
      body: '{"event_id":"evt_Qm4sT8vY1bN6cX3zL0pK7g","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"organization","user_id":"","removed_at":"2026-09-24T10:00:00Z"}',
      signature:
        "v1=ec0202dfc34ba5b946ce5e384b187d7358b3609549a049b3b3bfb51ef73d2177",
    },
    memberRemovedNanoseconds: {
      body: '{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"member","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f","removed_at":"2026-09-24T10:00:00.123456789Z"}',
      signature:
        "v1=0828ea12f32328f23603c3f910c57474459a38a6fe79fce8afcfa12bf5780548",
    },
  }

  /** The request exactly as Go sends it. */
  function goRequest(
    vector: { body: string; signature: string },
    signature = vector.signature
  ) {
    return new Request(`https://app.oryxa.in${REALTIME_PURGE_PATH}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "User-Agent": "oryxa-realtime/1",
        "X-Oryxa-Timestamp": goTimestamp,
        "X-Oryxa-Signature": signature,
      },
      body: vector.body,
    })
  }

  /**
   * The other member writes to the one who left, at each of `times`, on a
   * socket opened at the first of them.
   */
  async function goWorld(times: string[]) {
    const world = makeWorld({ REALTIME_PURGE_SECRET: goSecret })
    const name = directMessageName(goOrganization, removedUser, otherUser)
    const dm = await world.object<Dm>("DirectMessageAgent", name)
    vi.setSystemTime(new Date(times[0]))
    const socket = await openSocket(dm, {
      organizationId: goOrganization,
      userId: otherUser,
    })
    for (const [index, time] of times.entries()) {
      vi.setSystemTime(new Date(time))
      await say(dm, socket, {
        type: "dm-send",
        clientId: `client-go-vector-${index}`,
        body: `at ${time}`,
      })
    }
    const inboxOf = (userId: string) =>
      world.object<Inbox>("InboxAgent", inboxName(goOrganization, userId))
    const send = (vector: { body: string; signature: string }) =>
      handlePurge(
        goRequest(vector),
        world.env as unknown as ConversationEnv,
        goNow
      )
    return { world, dm, name, inboxOf, send }
  }

  it("signs each of them as Go does", async () => {
    for (const vector of Object.values(vectors)) {
      expect(await signPurge(goSecret, goTimestamp, vector.body)).toBe(
        vector.signature
      )
    }
  })

  it("carries out a member's removal, 204 again for the same event", async () => {
    const { world, dm, inboxOf, send } = await goWorld([
      "2026-09-24T09:00:00.000Z",
    ])
    for (let attempt = 0; attempt < 2; attempt += 1) {
      const response = await send(vectors.memberRemoved)
      expect(response.status).toBe(204)
      expect(await response.text()).toBe("")
    }
    expect(dm.sql`SELECT * FROM dm_messages`).toEqual([])
    expect(await (await inboxOf(otherUser)).listConversations()).toEqual([])
    expect(await (await inboxOf(removedUser)).listConversations()).toEqual([])
    expect(world.callsTo("RealtimeRegistryAgent", "purge")).toHaveLength(2)
    expect(world.callsTo("InboxAgent", "purge")).toHaveLength(1)
  })

  it("carries out an organization's deletion, newer messages too, 204 again for the same event", async () => {
    const { world, dm, inboxOf, send } = await goWorld([
      "2026-09-24T09:00:00.000Z",
      "2026-09-24T11:00:00.000Z",
    ])
    for (let attempt = 0; attempt < 2; attempt += 1) {
      expect((await send(vectors.organizationDeleted)).status).toBe(204)
    }
    expect(dm.sql`SELECT * FROM dm_messages`).toEqual([])
    for (const userId of [removedUser, otherUser]) {
      expect(await (await inboxOf(userId)).listConversations()).toEqual([])
    }
    const registry = await world.object<Registry>(
      "RealtimeRegistryAgent",
      goOrganization
    )
    expect(await registry.members()).toEqual([])
  })

  it("counts a message written in removed_at's millisecond as at or before it, and one written the next as newer", async () => {
    const { world, dm, name, inboxOf, send } = await goWorld([
      "2026-09-24T10:00:00.123Z",
      "2026-09-24T10:00:00.124Z",
    ])
    expect((await send(vectors.memberRemovedNanoseconds)).status).toBe(204)

    expect(
      dm.sql<{ created_at: string }>`SELECT created_at FROM dm_messages`
    ).toEqual([{ created_at: "2026-09-24T10:00:00.124Z" }])
    // The conversation stays, in both inboxes, with what stayed.
    for (const userId of [removedUser, otherUser]) {
      expect(await (await inboxOf(userId)).listConversations()).toEqual([
        expect.objectContaining({
          name,
          lastMessage: expect.objectContaining({
            createdAt: "2026-09-24T10:00:00.124Z",
          }) as unknown,
          unread: userId === removedUser ? 1 : 0,
        }),
      ])
    }
    const registry = await world.object<Registry>(
      "RealtimeRegistryAgent",
      goOrganization
    )
    expect(await registry.members()).toContain(removedUser)
  })

  it("answers 401 for a changed signature, or the right one outside the window", async () => {
    const { world, dm } = await goWorld(["2026-09-24T09:00:00.000Z"])
    const env = world.env as unknown as ConversationEnv
    const flipped = vectors.memberRemoved.signature.replace(/.$/, (last) =>
      last === "0" ? "1" : "0"
    )
    expect(
      (await handlePurge(goRequest(vectors.memberRemoved, flipped), env, goNow))
        .status
    ).toBe(401)
    expect(
      (
        await handlePurge(
          goRequest(vectors.memberRemoved),
          env,
          goNow + (purgeWindowSeconds + 1) * 1000
        )
      ).status
    ).toBe(401)
    expect(dm.sql<{ body: string }>`SELECT body FROM dm_messages`).toEqual([
      { body: "at 2026-09-24T09:00:00.000Z" },
    ])
  })
})
