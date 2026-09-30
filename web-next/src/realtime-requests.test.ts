import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { DirectMessageAgent } from "./direct-message-agent"
import { InboxAgent } from "./inbox-agent"
import { directMessageName, inboxName } from "./lib/dms/names"
import { signEvents } from "./realtime-events"
import { handlePurge, signPurge } from "./realtime-purge"
import {
  REALTIME_REQUESTS_PATH,
  handleRequests,
  maxRequestNoteBodyBytes,
  parseRequestNote,
  signRequests,
} from "./realtime-requests"
import type { RequestNoteBody } from "./realtime-requests"
import { RealtimeRegistryAgent } from "./realtime-registry-agent"
import { World, openSocket } from "./test/durable-objects"
import type { Harness } from "./test/durable-objects"
import type { ConversationEnv } from "./conversation-env"

vi.mock(
  "agents",
  async () => (await import("./test/durable-objects")).agentsModule
)

const secret = "0123456789abcdef0123456789abcdef"
const org = "org_sim"
const ada = "user-ada"
const bo = "user-bo"
const dmName = directMessageName(org, ada, bo)

type Dm = DirectMessageAgent & Harness
type Inbox = InboxAgent & Harness

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

function envOf(world: World): ConversationEnv {
  return world.env as unknown as ConversationEnv
}

/** What Go sends for a request Ada made of Bo's agent at revision 1, less what `edit` changes. */
function bodyOf(edit: Partial<RequestNoteBody> = {}): RequestNoteBody {
  return {
    organization_id: org,
    request_id: "irq_1",
    revision: 1,
    asker_user_id: ada,
    owner_user_id: bo,
    origin: "agent",
    state: "waiting",
    changed_at: "2026-09-24T11:59:00Z",
    ...edit,
  }
}

type Signing = {
  secret?: string
  timestamp?: string | number
  signature?: string
  /** What the signature is made over, when it is not the body sent. */
  signed?: string
  /** How the signature is made: a note's own (default), a purge's, an events request's, or none. */
  by?: "requests" | "purge" | "events"
  headers?: Record<string, string | null>
  method?: string
}

/** A request as Go makes one: JSON, the timestamp and the note's signature. */
async function signedRequest(
  note: RequestNoteBody | string = bodyOf(),
  signing: Signing = {}
): Promise<Request> {
  const body = typeof note === "string" ? note : JSON.stringify(note)
  const timestamp = String(signing.timestamp ?? nowSeconds)
  const key = signing.secret ?? secret
  const over = signing.signed ?? body
  const make =
    signing.by === "purge"
      ? signPurge
      : signing.by === "events"
        ? signEvents
        : signRequests
  const headers = new Headers({
    "Content-Type": "application/json",
    "X-Oryxa-Timestamp": timestamp,
    "X-Oryxa-Signature":
      signing.signature ?? (await make(key, timestamp, over)),
  })
  for (const [name, value] of Object.entries(signing.headers ?? {})) {
    if (value === null) headers.delete(name)
    else headers.set(name, value)
  }
  return new Request(`http://app.test${REALTIME_REQUESTS_PATH}`, {
    method: signing.method ?? "POST",
    headers,
    ...(signing.method === "GET" ? {} : { body }),
  })
}

async function errorOf(response: Response) {
  const body = await response.json<{ error: { code: string } }>()
  return body.error.code
}

async function send(
  world: World,
  note: RequestNoteBody | string = bodyOf(),
  signing: Signing = {},
  at = now
) {
  return handleRequests(await signedRequest(note, signing), envOf(world), at)
}

/** The DM's object, which a note made. */
function existingDm(world: World): Dm {
  const dm = world.existing<Dm>("DirectMessageAgent", dmName)
  if (!dm) throw new Error("the conversation was never made")
  return dm
}

async function inboxOf(world: World, userId: string) {
  const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, userId))
  return inbox.listConversations()
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date", "setTimeout", "clearTimeout"] })
  vi.setSystemTime(new Date(now))
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

// The vectors Go pins in realtime/internal/requests/requests_test.go: the
// same secret, timestamp and bodies, and the signatures computed outside
// both languages (`printf '%s' "requests.$timestamp.$body" | openssl dgst
// -sha256 -hmac "$secret"`).
const golden = {
  secret: "5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84",
  timestamp: "1790208000",
  vectors: [
    {
      body: '{"organization_id":"org_1","request_id":"irq_golden_1","revision":1,"asker_user_id":"user-a","owner_user_id":"user-b","origin":"agent","state":"waiting","changed_at":"2026-09-24T10:00:00Z"}',
      signature:
        "v1=6efcec28a265a8e2c70d1a03881c12b7c43808663c62263cf23bbe8a91ad51f7",
      purgeSigned:
        "v1=16be9eafa65062c650af54c73a245257e0a0fa7de9e04a9ddb72c0321dbd8b7b",
      eventsSigned:
        "v1=fdd14f29a48994bbf995dae4c418db61aa81613c296f801a447ce64950869a7c",
    },
    {
      body: '{"organization_id":"org_1","request_id":"irq_golden_1","revision":2,"asker_user_id":"user-a","owner_user_id":"user-b","origin":"slack","state":"approved","changed_at":"2026-09-24T10:00:01.123Z"}',
      signature:
        "v1=90d5150be5363039161c4ea3eef17f3817e4129925c3caa98aa8ba1051b7656b",
      purgeSigned:
        "v1=5932e4b56b95ba214b3936588b1e0f724f2f03583ac9c2debc456896e37f5cc0",
      eventsSigned:
        "v1=c4879ba81942450723e44d9f58d3c1797c5db3db735cb18c122c57e87c84de81",
    },
  ],
}

describe("the signature", () => {
  it("is Go's: the golden vectors, made over requests. and the timestamp and the body", async () => {
    for (const vector of golden.vectors) {
      expect(
        await signRequests(golden.secret, golden.timestamp, vector.body)
      ).toBe(vector.signature)
      // The other two directions sign the same body differently.
      expect(
        await signPurge(golden.secret, golden.timestamp, vector.body)
      ).toBe(vector.purgeSigned)
      expect(
        await signEvents(golden.secret, golden.timestamp, vector.body)
      ).toBe(vector.eventsSigned)
      expect(vector.signature).not.toBe(vector.purgeSigned)
      expect(vector.signature).not.toBe(vector.eventsSigned)
    }
  })

  it("takes Go's own signed bodies, at the timestamp Go signed them", async () => {
    for (const vector of golden.vectors) {
      const world = makeWorld({ REALTIME_PURGE_SECRET: golden.secret })
      const request = new Request(`http://app.test${REALTIME_REQUESTS_PATH}`, {
        method: "POST",
        headers: {
          "X-Oryxa-Timestamp": golden.timestamp,
          "X-Oryxa-Signature": vector.signature,
        },
        body: vector.body,
      })
      const response = await handleRequests(
        request,
        envOf(world),
        Number(golden.timestamp) * 1000
      )
      expect(response.status).toBe(204)
    }
  })

  it("refuses a purge's signature of a note, and an events request's, whoever made them", async () => {
    for (const vector of golden.vectors) {
      for (const signature of [vector.purgeSigned, vector.eventsSigned]) {
        const world = makeWorld({ REALTIME_PURGE_SECRET: golden.secret })
        const request = new Request(
          `http://app.test${REALTIME_REQUESTS_PATH}`,
          {
            method: "POST",
            headers: {
              "X-Oryxa-Timestamp": golden.timestamp,
              "X-Oryxa-Signature": signature,
            },
            body: vector.body,
          }
        )
        const response = await handleRequests(
          request,
          envOf(world),
          Number(golden.timestamp) * 1000
        )
        expect(response.status).toBe(401)
        expect(await errorOf(response)).toBe("invalid_signature")
        expect(world.calls).toEqual([])
      }
    }
  })

  it("refuses a purge's signature over a valid note, and a note's over a valid purge, each on the other's route", async () => {
    const world = makeWorld()
    // A note whose body is a perfectly good one, signed as a purge would be.
    const asPurge = await send(world, bodyOf(), { by: "purge" })
    expect(asPurge.status).toBe(401)
    expect(await errorOf(asPurge)).toBe("invalid_signature")

    // A purge whose body is a perfectly good one, signed as a note would be.
    const purgeBody = JSON.stringify({
      event_id: "evt_1",
      organization_id: org,
      scope: "member",
      user_id: ada,
      removed_at: "2026-09-24T10:00:00Z",
    })
    const timestamp = String(nowSeconds)
    const asNote = await handlePurge(
      new Request("http://app.test/internal/realtime/purge", {
        method: "POST",
        headers: {
          "X-Oryxa-Timestamp": timestamp,
          "X-Oryxa-Signature": await signRequests(secret, timestamp, purgeBody),
        },
        body: purgeBody,
      }),
      envOf(world),
      now
    )
    expect(asNote.status).toBe(401)
    expect(await errorOf(asNote)).toBe("invalid_signature")

    // A purge's own signature is good on its own route, which is what the
    // two refusals above are not.
    const ownPurge = await handlePurge(
      new Request("http://app.test/internal/realtime/purge", {
        method: "POST",
        headers: {
          "X-Oryxa-Timestamp": timestamp,
          "X-Oryxa-Signature": await signPurge(secret, timestamp, purgeBody),
        },
        body: purgeBody,
      }),
      envOf(world),
      now
    )
    expect(ownPurge.status).toBe(204)
    // An events signature of the note is neither.
    const asEvents = await send(world, bodyOf(), { by: "events" })
    expect(asEvents.status).toBe(401)
    expect(world.callsTo("DirectMessageAgent", "noteRequest")).toEqual([])
  })
})

describe("what the Worker checks first", () => {
  it("takes a POST only", async () => {
    const world = makeWorld()
    for (const method of ["GET", "PUT", "DELETE"]) {
      const response = await handleRequests(
        await signedRequest(bodyOf(), { method }),
        envOf(world),
        now
      )
      expect(response.status).toBe(405)
      expect(response.headers.get("Allow")).toBe("POST")
      expect(await errorOf(response)).toBe("method_not_allowed")
    }
  })

  it("is off without a secret of at least 32 characters, trimmed", async () => {
    for (const vars of [
      {},
      { REALTIME_PURGE_SECRET: "" },
      { REALTIME_PURGE_SECRET: "   " },
      { REALTIME_PURGE_SECRET: "short" },
      { REALTIME_PURGE_SECRET: ` ${"x".repeat(31)} ` },
    ]) {
      vi.spyOn(console, "error").mockImplementation(() => undefined)
      const response = await send(makeWorld(vars))
      expect(response.status).toBe(503)
      expect(await errorOf(response)).toBe("unavailable")
    }
  })

  it("trims the secret as Go does", async () => {
    const world = makeWorld({ REALTIME_PURGE_SECRET: `\n${secret}\n` })
    expect((await send(world)).status).toBe(204)
  })

  it("wants both headers, then a timestamp within five minutes, then a signature in its form", async () => {
    const world = makeWorld()
    const cases: Array<[Signing, number, string]> = [
      [{ headers: { "X-Oryxa-Timestamp": null } }, 401, "unauthorized"],
      [{ headers: { "X-Oryxa-Signature": null } }, 401, "unauthorized"],
      [{ timestamp: "soon" }, 401, "stale_request"],
      [{ timestamp: "" }, 401, "unauthorized"],
      [{ timestamp: "12345678901234" }, 401, "stale_request"],
      [{ timestamp: nowSeconds - 301 }, 401, "stale_request"],
      [{ timestamp: nowSeconds + 301 }, 401, "stale_request"],
      [{ signature: "v2=" + "0".repeat(64) }, 401, "invalid_signature"],
      [{ signature: "v1=" + "0".repeat(63) }, 401, "invalid_signature"],
      [{ signature: "v1=" + "g".repeat(64) }, 401, "invalid_signature"],
      [{ signature: "0".repeat(64) }, 401, "invalid_signature"],
    ]
    for (const [signing, status, code] of cases) {
      const response = await send(world, bodyOf(), signing)
      expect([response.status, await errorOf(response)]).toEqual([status, code])
    }
    expect(world.calls).toEqual([])
  })

  it("takes a request signed at either edge of its window, and no older or newer", async () => {
    for (const timestamp of [nowSeconds - 300, nowSeconds + 300]) {
      const response = await send(makeWorld(), bodyOf(), { timestamp })
      expect(response.status).toBe(204)
    }
    for (const timestamp of [nowSeconds - 301, nowSeconds + 301]) {
      const response = await send(makeWorld(), bodyOf(), { timestamp })
      expect(response.status).toBe(401)
      expect(await errorOf(response)).toBe("stale_request")
    }
  })

  it("refuses a body over 4 KB before it reads it whole, even one Go signed", async () => {
    const world = makeWorld()
    const huge = JSON.stringify(bodyOf({ origin: "a".repeat(5_000) }))
    expect(huge.length).toBeGreaterThan(maxRequestNoteBodyBytes)
    const response = await send(world, huge)
    expect(response.status).toBe(413)
    expect(await errorOf(response)).toBe("too_large")
    expect(world.calls).toEqual([])
  })

  it("compares the signature in the form it came, either case of hex", async () => {
    const world = makeWorld()
    const body = JSON.stringify(bodyOf())
    const upper = `v1=${(await signRequests(secret, String(nowSeconds), body)).slice(3).toUpperCase()}`
    expect((await send(world, body, { signature: upper })).status).toBe(204)
  })
})

describe("a forged or replayed request", () => {
  it("refuses a body Go did not sign, whatever headers it carries", async () => {
    const world = makeWorld()
    const signedBody = JSON.stringify(bodyOf())
    const timestamp = String(nowSeconds)
    const forged = JSON.stringify(bodyOf({ owner_user_id: "user-cy" }))
    // The signature of one body on another.
    const swapped = await send(world, forged, {
      signature: await signRequests(secret, timestamp, signedBody),
    })
    expect(swapped.status).toBe(401)
    expect(await errorOf(swapped)).toBe("invalid_signature")
    // A body altered by a byte: a space, a key moved.
    for (const altered of [
      ` ${signedBody}`,
      `${signedBody} `,
      signedBody.replace('"revision":1', '"revision":9'),
      signedBody.replace('"state":"waiting"', '"state":"answered"'),
    ]) {
      const response = await send(world, altered, {
        signature: await signRequests(secret, timestamp, signedBody),
      })
      expect(response.status).toBe(401)
    }
    // Signed with another secret, or over another timestamp.
    expect(
      (await send(world, signedBody, { secret: `${secret}x` })).status
    ).toBe(401)
    expect(
      (
        await send(world, signedBody, {
          signature: await signRequests(
            secret,
            String(nowSeconds - 1),
            signedBody
          ),
        })
      ).status
    ).toBe(401)
    expect(world.calls).toEqual([])
  })

  it("does nothing the second time a signed request is replayed inside its window", async () => {
    const world = makeWorld()
    const dm = await world.object<Dm>("DirectMessageAgent", dmName)
    const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, bo))
    const adaDm = await openSocket(dm, { organizationId: org, userId: ada })
    const boInbox = await openSocket(inbox, { organizationId: org, userId: bo })
    adaDm.clear()
    boInbox.clear()

    const request = await signedRequest()
    // The same bytes and headers again: nothing about the time differs.
    const copy = await signedRequest()
    expect((await handleRequests(request, envOf(world), now)).status).toBe(204)
    expect(adaDm.of("dm-request")).toHaveLength(1)
    expect(boInbox.of("inbox-conversation")).toHaveLength(1)
    // Go's retry, or somebody replaying what they saw, a minute later.
    expect(
      (await handleRequests(copy, envOf(world), now + 60_000)).status
    ).toBe(204)
    expect(adaDm.of("dm-request")).toHaveLength(1)
    expect(boInbox.of("inbox-conversation")).toHaveLength(1)
    expect(dm.sql`SELECT * FROM dm_requests`).toHaveLength(1)
  })
})

describe("the body", () => {
  it("reads only a note, in Go's shape, and refuses anything else with 400", async () => {
    const world = makeWorld()
    const bad: Array<[string, RequestNoteBody | string]> = [
      ["not JSON", "{"],
      ["not an object", "[]"],
      ["null", "null"],
      [
        "no organization",
        JSON.stringify({ ...bodyOf(), organization_id: undefined }),
      ],
      [
        "an organization a name cannot hold",
        bodyOf({ organization_id: "org~1" }),
      ],
      ["no request", JSON.stringify({ ...bodyOf(), request_id: undefined })],
      ["a request with a space", bodyOf({ request_id: "irq 1" })],
      ["a revision of 0", bodyOf({ revision: 0 })],
      ["a revision as text", JSON.stringify({ ...bodyOf(), revision: "1" })],
      ["a fractional revision", JSON.stringify({ ...bodyOf(), revision: 1.5 })],
      ["no asker", JSON.stringify({ ...bodyOf(), asker_user_id: undefined })],
      ["no owner", JSON.stringify({ ...bodyOf(), owner_user_id: "" })],
      ["one person twice", bodyOf({ owner_user_id: ada })],
      ["an origin in capitals", bodyOf({ origin: "Slack" })],
      ["a state nobody says", bodyOf({ state: "expired" })],
      ["a status instead of a state", bodyOf({ state: "pending" })],
      ["no time", JSON.stringify({ ...bodyOf(), changed_at: undefined })],
      ["a time that is not one", bodyOf({ changed_at: "yesterday" })],
      ["a 30th of February", bodyOf({ changed_at: "2026-02-30T10:00:00Z" })],
      ["a time before 1970", bodyOf({ changed_at: "1969-12-31T23:59:59Z" })],
    ]
    for (const [name, body] of bad) {
      const response = await send(world, body)
      expect([name, response.status, await errorOf(response)]).toEqual([
        name,
        400,
        "invalid_request",
      ])
    }
    expect(world.calls).toEqual([])
  })

  it("ignores keys the contract does not name, and reads times as a purge's are", () => {
    const parsed = parseRequestNote(
      new TextEncoder().encode(
        JSON.stringify({
          ...bodyOf({ changed_at: "2026-09-24T15:30:01.123456789+05:30" }),
          task: "ignored",
        })
      )
    )
    expect(parsed).toEqual({
      organizationId: org,
      note: {
        requestId: "irq_1",
        revision: 1,
        askerId: ada,
        ownerId: bo,
        origin: "agent",
        state: "waiting",
        changedAt: "2026-09-24T10:00:01.123Z",
      },
    })
  })

  it("refuses bytes that are not UTF-8", () => {
    expect(parseRequestNote(new Uint8Array([0x7b, 0xff, 0x7d]))).toBeUndefined()
  })
})

describe("noting a request", () => {
  it("tells the two people's direct message and both their inboxes, each once", async () => {
    const world = makeWorld()
    const response = await send(world)
    expect(response.status).toBe(204)
    expect(response.body).toBeNull()

    expect(
      world.callsTo("DirectMessageAgent", "noteRequest").map((c) => c.name)
    ).toEqual([dmName])
    expect(
      world
        .callsTo("InboxAgent", "noteRequest")
        .map((call) => call.name)
        .sort()
    ).toEqual([inboxName(org, ada), inboxName(org, bo)])

    const dm = existingDm(world)
    expect(dm.sql`SELECT request_id, revision, state FROM dm_requests`).toEqual(
      [{ request_id: "irq_1", revision: 1, state: "waiting" }]
    )
    // Each person's inbox lists the conversation with the other, moved up to when it happened.
    expect(await inboxOf(world, ada)).toEqual([
      {
        kind: "dm",
        name: dmName,
        peerId: bo,
        lastMessage: {
          authorId: bo,
          body: "You asked their agent · waiting",
          createdAt: "2026-09-24T11:59:00.000Z",
        },
        unread: 0,
        updatedAt: "2026-09-24T11:59:00.000Z",
      },
    ])
    expect(await inboxOf(world, bo)).toEqual([
      {
        kind: "dm",
        name: dmName,
        peerId: ada,
        lastMessage: {
          authorId: ada,
          body: "Asked your agent · waiting",
          createdAt: "2026-09-24T11:59:00.000Z",
        },
        unread: 0,
        updatedAt: "2026-09-24T11:59:00.000Z",
      },
    ])
  })

  it("has a request between the same two people noted whichever of them asked", async () => {
    const world = makeWorld()
    expect(
      (
        await send(
          world,
          bodyOf({ request_id: "irq_2", asker_user_id: bo, owner_user_id: ada })
        )
      ).status
    ).toBe(204)
    expect((await inboxOf(world, ada)).at(0)?.lastMessage?.body).toBe(
      "Asked your agent · waiting"
    )
    expect((await inboxOf(world, bo)).at(0)?.lastMessage?.body).toBe(
      "You asked their agent · waiting"
    )
  })

  it("says only what was asked of it: nothing else from the body is kept", async () => {
    const world = makeWorld()
    await send(
      world,
      JSON.stringify({
        ...bodyOf({ origin: "slack" }),
        task: "the secret plan",
      })
    )
    const stored = JSON.stringify(
      existingDm(world).sql`SELECT * FROM dm_requests`
    )
    expect(stored).not.toContain("secret plan")
    expect(stored).toContain("slack")
  })

  it("keeps a request's highest revision, told twice, late or out of order", async () => {
    const world = makeWorld()
    const dm = await world.object<Dm>("DirectMessageAgent", dmName)
    const socket = await openSocket(dm, { organizationId: org, userId: ada })
    socket.clear()
    const at = (minutes: number) =>
      new Date(now - minutes * 60_000).toISOString()

    // Revision 3 comes first.
    await send(
      world,
      bodyOf({ revision: 3, state: "answered", changed_at: at(1) })
    )
    // Then 2, and 1, and 3 again.
    for (const [revision, state] of [
      [2, "approved"],
      [1, "waiting"],
      [3, "answered"],
    ] as const) {
      const response = await send(
        world,
        bodyOf({ revision, state, changed_at: at(4 - revision) })
      )
      expect(response.status).toBe(204)
    }
    expect(
      dm.sql`SELECT revision, state, first_at, changed_at FROM dm_requests`
    ).toEqual([
      {
        revision: 3,
        state: "answered",
        first_at: at(1),
        changed_at: at(1),
      },
    ])
    // The socket heard the line once.
    expect(socket.of("dm-request")).toHaveLength(1)
    expect(socket.last("dm-request")?.request).toMatchObject({
      revision: 3,
      state: "answered",
    })
    expect((await inboxOf(world, bo)).at(0)?.lastMessage?.body).toBe(
      "Asked your agent · answered"
    )
  })

  it("moves a line's state in place as the request goes on, and every socket hears each change", async () => {
    const world = makeWorld()
    const dm = await world.object<Dm>("DirectMessageAgent", dmName)
    const adaTab = await openSocket(dm, { organizationId: org, userId: ada })
    const boTab = await openSocket(dm, { organizationId: org, userId: bo })
    adaTab.clear()
    boTab.clear()

    const steps = [
      [1, "waiting", "2026-09-24T11:50:00Z"],
      [2, "approved", "2026-09-24T11:52:00Z"],
      [3, "held", "2026-09-24T11:55:00Z"],
      [4, "answered", "2026-09-24T11:58:00Z"],
    ] as const
    for (const [revision, state, changed_at] of steps) {
      expect(
        (await send(world, bodyOf({ revision, state, changed_at }))).status
      ).toBe(204)
    }
    for (const socket of [adaTab, boTab]) {
      expect(socket.of("dm-request").map((frame) => frame.request)).toEqual(
        steps.map(([revision, state, changedAt]) => ({
          requestId: "irq_1",
          revision,
          askerId: ada,
          ownerId: bo,
          origin: "agent",
          state,
          changedAt: new Date(changedAt).toISOString(),
          // Where the line stands does not move with the state.
          at: "2026-09-24T11:50:00.000Z",
        }))
      )
    }
    expect(dm.sql`SELECT request_id FROM dm_requests`).toHaveLength(1)
  })

  it("keeps a line for each request, and a socket that opens is sent them, oldest first", async () => {
    const world = makeWorld()
    await send(
      world,
      bodyOf({ request_id: "irq_b", changed_at: "2026-09-24T11:50:00Z" })
    )
    await send(
      world,
      bodyOf({ request_id: "irq_a", changed_at: "2026-09-24T11:40:00Z" })
    )
    await send(
      world,
      bodyOf({
        request_id: "irq_c",
        origin: "slack",
        state: "held",
        revision: 4,
        changed_at: "2026-09-24T11:56:00Z",
      })
    )
    const dm = await world.object<Dm>("DirectMessageAgent", dmName)
    const socket = await openSocket(dm, { organizationId: org, userId: bo })
    const snapshot = socket.last("dm-snapshot") as {
      requests: Array<Record<string, unknown>>
    }
    expect(snapshot.requests.map((request) => request.requestId)).toEqual([
      "irq_a",
      "irq_b",
      "irq_c",
    ])
    expect(snapshot.requests[2]).toEqual({
      requestId: "irq_c",
      revision: 4,
      askerId: ada,
      ownerId: bo,
      origin: "slack",
      state: "held",
      changedAt: "2026-09-24T11:56:00.000Z",
      at: "2026-09-24T11:56:00.000Z",
    })
    // The inbox's row says the latest: irq_c, held, in Slack.
    expect((await inboxOf(world, bo)).at(0)?.lastMessage?.body).toBe(
      "Asked your agent in Slack · held"
    )
  })

  it("never notes a request as later than now", async () => {
    const world = makeWorld()
    await send(world, bodyOf({ changed_at: "2099-01-01T00:00:00Z" }))
    const dm = existingDm(world)
    expect(dm.sql`SELECT first_at, changed_at FROM dm_requests`).toEqual([
      {
        first_at: "2026-09-24T12:00:00.000Z",
        changed_at: "2026-09-24T12:00:00.000Z",
      },
    ])
    expect((await inboxOf(world, ada)).at(0)?.updatedAt).toBe(
      "2026-09-24T12:00:00.000Z"
    )
  })

  it("answers 503 when an object could not be told, and a retry finishes what the first attempt did not", async () => {
    const world = makeWorld()
    vi.spyOn(console, "error").mockImplementation(() => undefined)
    const dm = await world.object<Dm>("DirectMessageAgent", dmName)
    const adaTab = await openSocket(dm, { organizationId: org, userId: ada })
    const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, bo))
    const boInbox = await openSocket(inbox, { organizationId: org, userId: bo })
    adaTab.clear()
    boInbox.clear()

    // Ada's inbox is reset mid-call; the DM and Bo's inbox take the note.
    world.failing.add(inboxName(org, ada))
    const failed = await send(world)
    expect(failed.status).toBe(503)
    expect(await errorOf(failed)).toBe("unavailable")
    expect(adaTab.of("dm-request")).toHaveLength(1)
    expect(boInbox.of("inbox-conversation")).toHaveLength(1)
    expect(await inboxOf(world, ada).catch(() => "unreachable")).toEqual([])

    // Go sends it again: the ones that took it take it as a repeat.
    world.failing.delete(inboxName(org, ada))
    expect((await send(world)).status).toBe(204)
    expect(adaTab.of("dm-request")).toHaveLength(1)
    expect(boInbox.of("inbox-conversation")).toHaveLength(1)
    expect((await inboxOf(world, ada)).at(0)?.lastMessage?.body).toBe(
      "You asked their agent · waiting"
    )
  })

  it("keeps a request's line and an inbox's row apart from messages: it is no unread and no seq", async () => {
    const world = makeWorld()
    await send(world)
    const dm = existingDm(world)
    expect(dm.sql`SELECT * FROM dm_messages`).toEqual([])
    for (const user of [ada, bo]) {
      const [conversation] = await inboxOf(world, user)
      expect(conversation.unread).toBe(0)
    }
    const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, bo))
    expect(inbox.sql`SELECT COUNT(*) AS n FROM inbox_unread`).toEqual([
      { n: 0 },
    ])
  })

  it("is refused by an object for a note that is not its own", async () => {
    const world = makeWorld()
    const dm = await world.object<Dm>("DirectMessageAgent", dmName)
    const note = {
      requestId: "irq_1",
      revision: 1,
      askerId: ada,
      ownerId: "user-cy",
      origin: "agent",
      state: "waiting" as const,
      changedAt: "2026-09-24T11:59:00.000Z",
    }
    await expect(dm.noteRequest(note)).rejects.toThrow(TypeError)
    await expect(dm.noteRequest({ ...note, ownerId: ada })).rejects.toThrow(
      TypeError
    )
    await expect(
      dm.noteRequest({ ...note, ownerId: bo, revision: 0 })
    ).rejects.toThrow(TypeError)
    const inbox = await world.object<Inbox>("InboxAgent", inboxName(org, ada))
    await expect(
      inbox.noteRequest({ kind: "dm", name: dmName, peerId: bo }, note)
    ).rejects.toThrow(TypeError)
    // Bo's request to Cy is not Ada's business, in the conversation with Bo.
    await expect(
      inbox.noteRequest(
        { kind: "dm", name: dmName, peerId: bo },
        { ...note, askerId: bo, ownerId: "user-cy" }
      )
    ).rejects.toThrow(TypeError)
    expect(dm.sql`SELECT * FROM dm_requests`).toEqual([])
    expect(inbox.sql`SELECT * FROM inbox_requests`).toEqual([])
  })
})
