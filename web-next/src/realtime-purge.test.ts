import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { DirectMessageAgent } from "./direct-message-agent"
import { InboxAgent } from "./inbox-agent"
import { directMessageName, inboxName } from "./lib/dms/names"
import {
  REALTIME_PURGE_PATH,
  handlePurge,
  maxPurgeBodyBytes,
  parsePurgeRequest,
  purgeWindowSeconds,
  signPurge,
} from "./realtime-purge"
import type { PurgeRequest } from "./realtime-purge"
import { RealtimeRegistryAgent, purgeMemoryMs } from "./realtime-registry-agent"
import { World, drain, openSocket, say } from "./test/durable-objects"
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
      InboxAgent,
      RealtimeRegistryAgent,
    } as unknown as ConstructorParameters<typeof World>[0],
    vars
  )
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

const memberLeft: PurgeRequest = {
  event_id: "evt_member_removed_1",
  organization_id: org,
  user_id: admin,
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(now)
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe("the purge's signature", () => {
  it("signs as Go does: v1= and the hex HMAC-SHA256 of `{timestamp}.{body}`", async () => {
    // Computed apart from this code, with Node's crypto and with openssl.
    const body = JSON.stringify({
      event_id: "evt_1",
      organization_id: "org_sim",
      user_id: "user-admin",
    })
    expect(await signPurge(secret, "1790000000", body)).toBe(
      "v1=ec7fbc93691e7a657756945e18b349991bbd8a91c15d3c21818b683a39618fd8"
    )
  })

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

  it("refuses every purge while the Worker has no secret of at least 32 characters", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {})
    for (const vars of [
      {},
      { REALTIME_PURGE_SECRET: "" },
      { REALTIME_PURGE_SECRET: "short-secret" },
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

  it("refuses a signed body that is not a purge, never reading a missing user_id as the whole organization", async () => {
    const world = makeWorld()
    await talk(world, admin, member)
    for (const body of [
      "not json",
      "[]",
      JSON.stringify({ event_id: "evt_1", organization_id: org }),
      JSON.stringify({
        event_id: "evt_1",
        organization_id: org,
        user_id: null,
      }),
      JSON.stringify({ event_id: "", organization_id: org, user_id: admin }),
      JSON.stringify({
        event_id: "x".repeat(201),
        organization_id: org,
        user_id: admin,
      }),
      JSON.stringify({
        event_id: "evt_1",
        organization_id: "org~sim",
        user_id: admin,
      }),
      JSON.stringify({
        event_id: "evt_1",
        organization_id: org,
        user_id: "user admin",
      }),
    ]) {
      expect(
        await codeOf(await purge(world, await purgeRequest(body)))
      ).toEqual([400, "invalid_request"])
    }
    expect(bodiesIn(world, admin, member)).toEqual(["hello"])
    expect(
      parsePurgeRequest(new TextEncoder().encode(JSON.stringify(memberLeft)))
    ).toEqual(memberLeft)
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
    const purging = registry.purge("evt_during", admin)
    await drain()
    await registry.register(admin)
    release()
    expect(await purging).toBe("purged")
    expect(await registry.members()).toEqual([admin, member])
  })

  it("purges a whole organization for an empty user_id", async () => {
    const world = makeWorld()
    await talk(world, admin, member)
    await talk(world, owner, admin)
    await talk(world, member, owner)

    const response = await purge(
      world,
      await purgeRequest({
        event_id: "evt_organization_deleted_1",
        organization_id: org,
        user_id: "",
      })
    )

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

  it("does nothing for an event it has carried out, so a replay cannot empty what came after it", async () => {
    const world = makeWorld()
    await talk(world, admin, member, "before")
    // The same request twice: its timestamp, body and signature.
    const request = await purgeRequest(memberLeft)
    const replay = await purgeRequest(memberLeft)
    expect(replay.headers.get("X-Oryxa-Signature")).toBe(
      request.headers.get("X-Oryxa-Signature")
    )
    expect((await purge(world, request)).status).toBe(204)

    // The admin is let back in and talks again, within the window.
    await talk(world, admin, member, "after")
    expect((await purge(world, replay)).status).toBe(204)
    expect(bodiesIn(world, admin, member)).toEqual(["after"])
    expect(await listed(world, member)).toEqual([
      directMessageName(org, admin, member),
    ])

    // A new event for the same member is a purge.
    const again = await purgeRequest({
      ...memberLeft,
      event_id: "evt_member_removed_2",
    })
    expect((await purge(world, again)).status).toBe(204)
    expect(bodiesIn(world, admin, member)).toEqual([])
  })

  it(`forgets an event after ${purgeMemoryMs / 3_600_000} hours`, async () => {
    const world = makeWorld()
    await talk(world, admin, member, "before")
    expect((await purge(world, await purgeRequest(memberLeft))).status).toBe(
      204
    )
    await talk(world, admin, member, "a day later")
    vi.setSystemTime(now + purgeMemoryMs + 1_000)
    const later = Math.floor(Date.now() / 1000)
    const response = await purge(
      world,
      await purgeRequest(memberLeft, { timestamp: later }),
      Date.now()
    )
    expect(response.status).toBe(204)
    expect(bodiesIn(world, admin, member)).toEqual([])
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

  it("answers 503 when the organization's registry cannot be reached", async () => {
    const world = makeWorld()
    vi.spyOn(console, "error").mockImplementation(() => {})
    world.failing.add(org)
    const response = await purge(world, await purgeRequest(memberLeft))
    expect(await codeOf(response)).toEqual([503, "unavailable"])
  })
})

// The Go side's own vectors (the relay that turns auth.member.removed.v1 and
// auth.organization.deleted.v1 into purges): the key is the secret's
// characters as written, never hex-decoded, and the signature is lowercase.
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
      body: '{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f"}',
      signature:
        "v1=a6f400c72318e7a142e5fcc51f04f98fad8ffae7864549a902cadfa96b584067",
    },
    organizationDeleted: {
      body: '{"event_id":"evt_Qm4sT8vY1bN6cX3zL0pK7g","organization_id":"org_2fQx9LmR7vTk4WcA","user_id":""}',
      signature:
        "v1=c35da0ee0a92d26cf07903fcfd3118d42b0517920abe31cf13a0f818b9004314",
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

  async function goWorld() {
    const world = makeWorld({ REALTIME_PURGE_SECRET: goSecret })
    const name = directMessageName(goOrganization, removedUser, otherUser)
    const dm = await world.object<Dm>("DirectMessageAgent", name)
    const socket = await openSocket(dm, {
      organizationId: goOrganization,
      userId: otherUser,
    })
    await say(dm, socket, {
      type: "dm-send",
      clientId: "client-go-vector",
      body: "Welcome aboard",
    })
    const inboxOf = (userId: string) =>
      world.object<Inbox>("InboxAgent", inboxName(goOrganization, userId))
    return { world, dm, inboxOf }
  }

  it("signs each of them as Go does", async () => {
    for (const vector of Object.values(vectors)) {
      expect(await signPurge(goSecret, goTimestamp, vector.body)).toBe(
        vector.signature
      )
    }
  })

  it("carries out a member's removal, 204 again for the same event", async () => {
    const { world, dm, inboxOf } = await goWorld()
    for (let attempt = 0; attempt < 2; attempt += 1) {
      const response = await handlePurge(
        goRequest(vectors.memberRemoved),
        world.env as unknown as ConversationEnv,
        goNow
      )
      expect(response.status).toBe(204)
      expect(await response.text()).toBe("")
    }
    expect(dm.sql`SELECT * FROM dm_messages`).toEqual([])
    expect(await (await inboxOf(otherUser)).listConversations()).toEqual([])
    expect(await (await inboxOf(removedUser)).listConversations()).toEqual([])
    expect(world.callsTo("RealtimeRegistryAgent", "purge")).toHaveLength(2)
  })

  it("carries out an organization's deletion, 204 again for the same event", async () => {
    const { world, dm, inboxOf } = await goWorld()
    for (let attempt = 0; attempt < 2; attempt += 1) {
      const response = await handlePurge(
        goRequest(vectors.organizationDeleted),
        world.env as unknown as ConversationEnv,
        goNow
      )
      expect(response.status).toBe(204)
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

  it("answers 401 for a changed signature, or the right one outside the window", async () => {
    const { world, dm } = await goWorld()
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
      { body: "Welcome aboard" },
    ])
  })
})
