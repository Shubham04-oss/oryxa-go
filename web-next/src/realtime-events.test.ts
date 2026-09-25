import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { DirectMessageAgent } from "./direct-message-agent"
import { InboxAgent } from "./inbox-agent"
import { directMessageName } from "./lib/dms/names"
import { encodeRoster } from "./lib/side/protocol"
import {
  REALTIME_EVENTS_PATH,
  eventsBody,
  eventsRequestLimits,
  eventsTiming,
  realtimeEventTypes,
  realtimeEventsConfig,
  signEvents,
} from "./realtime-events"
import { minPurgeSecretLength, signPurge } from "./realtime-purge"
import { RealtimeRegistryAgent } from "./realtime-registry-agent"
import { SideConversationAgent } from "./side-conversation-agent"
import {
  World,
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

type Dm = DirectMessageAgent & Harness
type Side = SideConversationAgent & Harness

// The contract's golden vectors (REALTIME.md, "Conversation events"): Go's
// half checks the same bodies, byte for byte, and the same signatures.
const golden = {
  secret: "5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84",
  timestamp: "1790208000",
  posted: {
    body: '{"events":[{"id":"evt_golden_posted_1","type":"realtime.message.posted.v1","detail":{"organization_id":"org_1","conversation":{"kind":"direct","id":"org_1~user-a~user-b"},"participant_user_ids":["user-a","user-b"],"message":{"id":"msg_1","seq":1},"author":{"kind":"member","user_id":"user-a"},"posted_at":"2026-09-24T10:00:00.000Z"}}]}',
    signature:
      "v1=75442e7357021586eb3717f2c97b091546d26cc31bcfdecafe571f36e7836cfa",
    purgeStyle:
      "v1=a8cb24541792769a083259a5396d3d53bbd95c34cd72296d65acaf993e8f583e",
  },
  read: {
    body: '{"events":[{"id":"evt_golden_read_1","type":"realtime.conversation.read.v1","detail":{"organization_id":"org_1","conversation":{"kind":"side","id":"org_1~tsp_1~user-a~user-b","teamspace_id":"tsp_1"},"reader_user_id":"user-b","read_through_seq":7,"read_at":"2026-09-24T10:05:00.123Z"}}]}',
    signature:
      "v1=68488d8dd95e857bc0550ff45cbad1cacb526bd14b30b6cdad7b12216d52adfc",
    purgeStyle:
      "v1=6478f2e3e872191fa991b20d7569b715ed46a1ea533b5590717ab300e5119d8d",
  },
}

type GoldenEvent = { id: string; type: string; detail: unknown }

/** A golden body's one event, its detail as the JSON it was written as. */
function goldenEvent(body: string): {
  id: string
  type: string
  detail: string
} {
  const [event] = (JSON.parse(body) as { events: GoldenEvent[] }).events
  return {
    id: event.id,
    type: event.type,
    detail: JSON.stringify(event.detail),
  }
}

const org = "org_sim"
const ada = "user-ada"
const bo = "user-bo"
const dmName = directMessageName(org, ada, bo)

let go: FakeGoEvents

function makeWorld(
  vars: Record<string, unknown> = {
    ORYXA_API_ORIGIN: eventsOrigin,
    REALTIME_PURGE_SECRET: eventsSecret,
  }
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

async function setup() {
  const world = makeWorld()
  const dm = await world.object<Dm>("DirectMessageAgent", dmName)
  return { world, dm }
}

function join(dm: Dm, userId: string) {
  return openSocket(dm, { organizationId: org, userId })
}

let clientIds = 0

async function send(dm: Dm, socket: FakeSocket, body: string) {
  clientIds += 1
  await say(dm, socket, {
    type: "dm-send",
    clientId: `client-${String(clientIds).padStart(4, "0")}`,
    body,
  })
}

let eventIds = 0

/** Puts `count` events in a DM's outbox as it records them, oldest first, recorded at `createdAt`. */
function enqueue(dm: Dm, count: number, createdAt = Date.now()): string[] {
  const ids: string[] = []
  for (let index = 0; index < count; index += 1) {
    eventIds += 1
    const id = `evt_test_${String(eventIds).padStart(5, "0")}`
    const detail = JSON.stringify({
      organization_id: org,
      conversation: { kind: "direct", id: dmName },
      participant_user_ids: [ada, bo],
      message: { id: `dmm_${eventIds}`, seq: eventIds },
      author: { kind: "member", user_id: ada },
      posted_at: new Date(createdAt).toISOString(),
    })
    dm.sql`INSERT INTO event_outbox (id, type, detail, created_at)
      VALUES (${id}, ${realtimeEventTypes.messagePosted}, ${detail},
        ${createdAt})`
    ids.push(id)
  }
  return ids
}

function outbox(object: Harness) {
  return object.sql<{
    id: string
  }>`SELECT id FROM event_outbox ORDER BY position`
}

/** Go's wait as the outbox keeps it: one value. */
function backoffOf(object: Harness) {
  const rows = object.sql<{
    value: string
  }>`SELECT value FROM event_meta WHERE key = 'backoff'`
  return rows.map((row) => JSON.parse(row.value) as unknown)
}

type Db = {
  prepare: (sql: string) => { all: (...values: unknown[]) => unknown[] }
}

/**
 * Runs `work` and counts what the object's SQL did meanwhile: the
 * statements it ran, each write with the rows it changed, and those rows in
 * all.
 */
async function sqlOf(object: Harness, work: () => Promise<unknown>) {
  const holder = object as unknown as { db: Db }
  const db = holder.db
  const statements: string[] = []
  const writes: Array<{ sql: string; changed: number }> = []
  holder.db = {
    prepare(sql) {
      const statement = db.prepare(sql)
      return {
        all(...values) {
          const result = statement.all(...values)
          const text = sql.replace(/\s+/g, " ").trim()
          statements.push(text)
          if (/^\s*(INSERT|UPDATE|DELETE|REPLACE)\b/i.test(sql)) {
            const [{ changed }] = db
              .prepare("SELECT changes() AS changed")
              .all() as Array<{ changed: number }>
            writes.push({ sql: text, changed })
          }
          return result
        },
      }
    },
  }
  try {
    await work()
  } finally {
    holder.db = db
  }
  const rows = writes.reduce((total, write) => total + write.changed, 0)
  return { rows, statements, writes }
}

/** The rows `writes` changed in `table`. */
function rowsIn(
  writes: ReadonlyArray<{ sql: string; changed: number }>,
  table: string
): number {
  return writes
    .filter((write) => new RegExp(`\\b${table}\\b`).test(write.sql))
    .reduce((total, write) => total + write.changed, 0)
}

function deadLetters(object: Harness) {
  return object.sql<{
    id: string
    status: number
    answer: string
    dead_at: number
  }>`SELECT id, status, answer, dead_at FROM event_dead_letters ORDER BY id`
}

/** How long each drain set is from now, in seconds. */
function drainsSet(dm: Dm): number[] {
  return dm.jobs
    .filter((job) => job.callback === "drainEvents")
    .map((job) => (job.time * 1000 - Date.now()) / 1000)
}

/** The ids of the drains set (a running one included, as the SDK lists it). */
function drainIds(object: Harness): string[] {
  return object.jobs
    .filter((job) => job.callback === "drainEvents")
    .map((job) => job.id)
}

/** Moves the clock to the drain that is set and runs it. */
async function runNextDrain(dm: Dm): Promise<void> {
  const [job] = dm.jobs.filter((each) => each.callback === "drainEvents")
  vi.setSystemTime(job.time * 1000)
  await runDueJobs(dm)
}

let errors: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date", "setTimeout", "clearTimeout"] })
  vi.setSystemTime(new Date("2026-09-24T09:00:00.000Z"))
  go = new FakeGoEvents()
  vi.stubGlobal("fetch", routeFetch(go))
  errors = vi.spyOn(console, "error").mockImplementation(() => undefined)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("realtimeEventsConfig", () => {
  it("is on only with Go's origin and the purge's secret, trimmed as Go trims it", () => {
    for (const env of [
      {},
      { ORYXA_API_ORIGIN: eventsOrigin },
      { REALTIME_PURGE_SECRET: eventsSecret },
      { ORYXA_API_ORIGIN: eventsOrigin, REALTIME_PURGE_SECRET: " \n\t" },
      { ORYXA_API_ORIGIN: "", REALTIME_PURGE_SECRET: eventsSecret },
      { ORYXA_API_ORIGIN: "not a url", REALTIME_PURGE_SECRET: eventsSecret },
    ]) {
      expect(realtimeEventsConfig(env)).toBeUndefined()
    }
    expect(
      realtimeEventsConfig({
        ORYXA_API_ORIGIN: ` ${eventsOrigin} `,
        REALTIME_PURGE_SECRET: `\n${eventsSecret}  `,
      })
    ).toEqual({
      url: new URL(`${eventsOrigin}${REALTIME_EVENTS_PATH}`),
      secret: eventsSecret,
    })
  })

  it("is off with a secret shorter than a purge's, as Go would take none it signs", () => {
    const short = "x".repeat(minPurgeSecretLength - 1)
    // The whitespace around it never counts.
    for (const secret of [short, ` ${short}\n`]) {
      expect(
        realtimeEventsConfig({
          ORYXA_API_ORIGIN: eventsOrigin,
          REALTIME_PURGE_SECRET: secret,
        })
      ).toBeUndefined()
    }
    const least = "x".repeat(minPurgeSecretLength)
    expect(
      realtimeEventsConfig({
        ORYXA_API_ORIGIN: eventsOrigin,
        REALTIME_PURGE_SECRET: ` ${least} `,
      })
    ).toEqual({
      url: new URL(`${eventsOrigin}${REALTIME_EVENTS_PATH}`),
      secret: least,
    })
  })
})

describe("signEvents: the golden vectors", () => {
  it("signs both golden bodies exactly as Go checks them, and never as a purge", async () => {
    for (const vector of [golden.posted, golden.read]) {
      const signature = await signEvents(
        golden.secret,
        golden.timestamp,
        vector.body
      )
      expect(signature).toBe(vector.signature)
      expect(signature).not.toBe(vector.purgeStyle)
      // The same bytes, as bytes.
      expect(
        await signEvents(
          golden.secret,
          golden.timestamp,
          new TextEncoder().encode(vector.body)
        )
      ).toBe(vector.signature)
      // The refused signature is a purge's signing string: never this call's.
      expect(
        await signPurge(golden.secret, golden.timestamp, vector.body)
      ).toBe(vector.purgeStyle)
    }
  })

  it("writes each golden body byte for byte from its event", () => {
    for (const vector of [golden.posted, golden.read]) {
      expect(eventsBody([goldenEvent(vector.body)])).toBe(vector.body)
    }
  })
})

describe("EventOutbox: the call", () => {
  it("sends each golden body as it is, to Go's events path, with its signature and never a purge's", async () => {
    vi.setSystemTime(Number(golden.timestamp) * 1000)
    go = new FakeGoEvents(golden.secret)
    vi.stubGlobal("fetch", routeFetch(go))
    const world = makeWorld({
      ORYXA_API_ORIGIN: eventsOrigin,
      REALTIME_PURGE_SECRET: golden.secret,
    })
    const dm = await world.object<Dm>(
      "DirectMessageAgent",
      "org_1~user-a~user-b"
    )
    const side = await world.object<Side>(
      "SideConversationAgent",
      "org_1~tsp_1~user-a~user-b"
    )
    for (const [object, vector] of [
      [dm, golden.posted],
      [side, golden.read],
    ] as const) {
      const { id, type, detail } = goldenEvent(vector.body)
      object.sql`INSERT INTO event_outbox (id, type, detail, created_at)
        VALUES (${id}, ${type}, ${detail}, ${Date.now()})`
      await object.drainEvents()
    }

    expect(
      go.requests.map((request) => ({
        url: request.url,
        method: request.method,
        contentType: request.headers["content-type"],
        timestamp: request.headers["x-oryxa-timestamp"],
        signature: request.headers["x-oryxa-signature"],
        body: request.body,
        status: request.status,
      }))
    ).toEqual(
      [golden.posted, golden.read].map((vector) => ({
        url: `${eventsOrigin}${REALTIME_EVENTS_PATH}`,
        method: "POST",
        contentType: "application/json",
        timestamp: golden.timestamp,
        signature: vector.signature,
        body: vector.body,
        status: 204,
      }))
    )
    for (const [request, vector] of go.requests.map(
      (each, index) => [each, [golden.posted, golden.read][index]] as const
    )) {
      expect(request.headers["x-oryxa-signature"]).not.toBe(vector.purgeStyle)
    }
    expect(outbox(dm)).toEqual([])
    expect(outbox(side)).toEqual([])
  })

  it("writes a DM's message and a side conversation's read in the golden vectors' shape", async () => {
    const world = makeWorld()
    vi.setSystemTime(new Date("2026-09-24T10:00:00.000Z"))
    const dm = await world.object<Dm>(
      "DirectMessageAgent",
      "org_1~user-a~user-b"
    )
    const tab = await openSocket(dm, {
      organizationId: "org_1",
      userId: "user-a",
    })
    await send(dm, tab, "Morning")
    const [message] = dm.sql<{ id: string }>`SELECT id FROM dm_messages`
    const [posted] = dm.sql<{
      type: string
      detail: string
    }>`SELECT type, detail FROM event_outbox WHERE reader_id IS NULL`
    const expected = goldenEvent(golden.posted.body)
    expect(posted).toEqual({
      type: expected.type,
      detail: expected.detail.replace('"msg_1"', JSON.stringify(message.id)),
    })

    const side = await world.object<Side>(
      "SideConversationAgent",
      "org_1~tsp_1~user-a~user-b"
    )
    const headers = {
      "X-Oryxa-Side-Roster": encodeRoster({
        teamspaceName: "Launch room",
        paused: false,
        people: [],
        contributions: [],
      }),
    }
    const a = await openSocket(
      side,
      { organizationId: "org_1", userId: "user-a" },
      { ...headers, "X-Oryxa-Cookie": "oryxa_access=a" }
    )
    const b = await openSocket(
      side,
      { organizationId: "org_1", userId: "user-b" },
      { ...headers, "X-Oryxa-Cookie": "oryxa_access=b" }
    )
    for (let line = 1; line <= 7; line += 1) {
      clientIds += 1
      await say(side, a, {
        type: "side-send",
        clientId: `client-${String(clientIds).padStart(4, "0")}`,
        body: `Line ${line}`,
      })
    }
    vi.setSystemTime(new Date("2026-09-24T10:05:00.123Z"))
    await say(side, b, { type: "side-read", seq: 7 })
    const [read] = side.sql<{
      type: string
      detail: string
    }>`SELECT type, detail FROM event_outbox WHERE reader_id = 'user-b'`
    const { type, detail } = goldenEvent(golden.read.body)
    expect(read).toEqual({ type, detail })
  })
})

describe("EventOutbox: draining", () => {
  it(`sends at most ${eventsRequestLimits.events} events a request, oldest first, each drain a second after the last`, async () => {
    const { dm } = await setup()
    const ids = enqueue(dm, 250)

    await dm.drainEvents()
    expect(go.requests.map((request) => request.events.length)).toEqual([100])
    expect(go.requests[0].events.map((event) => event.id)).toEqual(
      ids.slice(0, 100)
    )
    // The rest go on the next drain, a second on.
    expect(drainsSet(dm)).toEqual([1])
    await runDueJobs(dm)
    expect(go.requests).toHaveLength(1)

    await runNextDrain(dm)
    await runNextDrain(dm)
    expect(go.requests.map((request) => request.events.length)).toEqual([
      100, 100, 50,
    ])
    expect(go.taken.map((event) => event.id)).toEqual(ids)
    const gaps = go.requests
      .slice(1)
      .map((request, index) => request.at - go.requests[index].at)
    expect(gaps).toEqual([1000, 1000])
    // Taken: they leave the outbox, and nothing more is set.
    expect(outbox(dm)).toEqual([])
    expect(drainsSet(dm)).toEqual([])
  })

  it("sends what a DM records once it records it, and deletes what Go took", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    await send(dm, adaTab, "one")
    // Set for now, the drain goes at once. Ada's read through her own
    // message is its event's to say.
    expect(drainsSet(dm)).toEqual([0])
    await runDueJobs(dm)
    expect(go.taken.map((event) => event.type)).toEqual([
      realtimeEventTypes.messagePosted,
    ])
    expect(outbox(dm)).toEqual([])
    expect(drainsSet(dm)).toEqual([])

    // The next message, within the second: its drain is a second after the last.
    vi.advanceTimersByTime(300)
    await send(dm, adaTab, "two")
    expect(drainsSet(dm)).toEqual([0.7])
    await runNextDrain(dm)
    expect(go.requests).toHaveLength(2)
    expect(go.requests[1].at - go.requests[0].at).toBe(1000)
  })

  it("moves what Go refused (400 or 413) to the dead letters with its answer, keeps them seven days, and never sends them again", async () => {
    const { dm } = await setup()
    go.answers.push(
      Response.json(
        {
          error: {
            code: "invalid_request",
            message: "events[0].detail.message.seq must be at least 1",
          },
        },
        { status: 400 }
      ),
      413
    )
    const refused = enqueue(dm, 2)
    await dm.drainEvents()
    const refusedAt = Date.now()
    expect(outbox(dm)).toEqual([])
    expect(deadLetters(dm)).toEqual(
      refused.map((id) => ({
        id,
        status: 400,
        answer:
          '{"error":{"code":"invalid_request","message":"events[0].detail.message.seq must be at least 1"}}',
        dead_at: refusedAt,
      }))
    )
    expect(errors).toHaveBeenCalledWith(
      expect.stringContaining("Go refused 2 events (400)"),
      expect.objectContaining({ ids: refused })
    )

    vi.advanceTimersByTime(1000)
    const tooLarge = enqueue(dm, 1)
    await dm.drainEvents()
    expect(deadLetters(dm).map((row) => [row.id, row.status])).toEqual([
      ...refused.map((id) => [id, 400]),
      [tooLarge[0], 413],
    ])

    // Kept for seven days: a drain is set to let the oldest go then.
    expect(drainsSet(dm)).toEqual([eventsTiming.retentionMs / 1000 - 1])
    vi.setSystemTime(refusedAt + eventsTiming.retentionMs - 1)
    await runDueJobs(dm)
    expect(deadLetters(dm)).toHaveLength(3)
    await runNextDrain(dm)
    expect(deadLetters(dm).map((row) => row.id)).toEqual(tooLarge)
    await runNextDrain(dm)
    expect(deadLetters(dm)).toEqual([])
    expect(drainsSet(dm)).toEqual([])
    // Go was sent each once.
    expect(go.requests.map((request) => request.status)).toEqual([400, 413])
  })

  it("sends again what a 400 or 413 refused without Go's own refusal: a proxy's page, or another error", async () => {
    const { dm } = await setup()
    go.answers.push(
      new Response("<html><body>400 Bad Request</body></html>", {
        status: 400,
        headers: { "Content-Type": "text/html" },
      }),
      new Response(null, { status: 413 }),
      Response.json({ error: { code: "bad_request" } }, { status: 400 }),
      Response.json({ error: "invalid_request" }, { status: 413 }),
      Response.json({ code: "invalid_request" }, { status: 400 })
    )
    const ids = enqueue(dm, 2)
    await dm.drainEvents()
    for (let attempt = 1; attempt <= 4; attempt += 1) {
      expect(deadLetters(dm)).toEqual([])
      expect(outbox(dm).map((row) => row.id)).toEqual(ids)
      await runNextDrain(dm)
    }
    expect(errors).toHaveBeenCalledWith(
      expect.stringContaining(
        "Go did not take 2 events (answered 400, but not as Go refuses a request)"
      ),
      expect.anything()
    )
    // Then Go takes them, with the same ids.
    await runNextDrain(dm)
    expect(go.requests.map((request) => request.status)).toEqual([
      400, 413, 400, 413, 400, 204,
    ])
    expect(go.taken.map((event) => event.id)).toEqual(ids)
    expect(deadLetters(dm)).toEqual([])
    expect(outbox(dm)).toEqual([])
  })

  it("waits while Go's realtime is off: a 404 is sent again, never refused", async () => {
    const { dm } = await setup()
    go.answers.push(404, 404)
    const ids = enqueue(dm, 1)
    await dm.drainEvents()
    expect(deadLetters(dm)).toEqual([])
    expect(backoffOf(dm)).toEqual([{ failures: 1, until: Date.now() + 1000 }])
    await runNextDrain(dm)
    expect(backoffOf(dm)).toEqual([{ failures: 2, until: Date.now() + 2000 }])
    await runNextDrain(dm)
    expect(go.requests.map((request) => request.status)).toEqual([
      404, 404, 204,
    ])
    expect(go.taken.map((event) => event.id)).toEqual(ids)
    expect(errors).toHaveBeenCalledWith(
      expect.stringContaining("Go did not take 1 events (Go answered 404)"),
      expect.anything()
    )
  })

  it("runs one drain at a time: one that comes while another is under way sends nothing, and the first sets the next", async () => {
    const { dm } = await setup()
    go.answers.push(503)
    const ids = enqueue(dm, 1)
    const release = go.hold()
    const first = dm.drainEvents()
    await vi.waitFor(() => expect(go.requests).toHaveLength(1))
    // As a second schedule, or the platform's retry of an alarm, firing
    // while the first is still waiting for Go.
    await dm.drainEvents()
    expect(go.requests).toHaveLength(1)
    release()
    await first
    // Go did not take it: the first set one drain, and the second none.
    expect(drainIds(dm)).toHaveLength(1)
    await runNextDrain(dm)
    expect(go.requests).toHaveLength(2)
    expect(go.taken.map((event) => event.id)).toEqual(ids)
    expect(drainIds(dm)).toEqual([])
  })

  it("never follows a redirect: it is not Go's answer, and the events go again", async () => {
    const { dm } = await setup()
    const elsewhere = `${eventsOrigin}/v1/realtime/elsewhere`
    go.answers.push(
      new Response(null, { status: 307, headers: { Location: elsewhere } })
    )
    const ids = enqueue(dm, 1)
    await dm.drainEvents()
    // Followed, the request would have gone on to `elsewhere` and been
    // taken there.
    expect(go.requests.map((request) => [request.url, request.status])).toEqual(
      [[`${eventsOrigin}${REALTIME_EVENTS_PATH}`, 307]]
    )
    expect(outbox(dm).map((row) => row.id)).toEqual(ids)
    expect(drainsSet(dm)).toEqual([1])
    await runNextDrain(dm)
    expect(go.taken.map((event) => event.id)).toEqual(ids)
    expect(outbox(dm)).toEqual([])
  })

  it("gives up on a request Go has not answered in ten seconds, and sends its events again", async () => {
    const { dm } = await setup()
    const release = go.hold()
    const ids = enqueue(dm, 1)
    const draining = dm.drainEvents()
    await vi.waitFor(() => expect(go.requests).toHaveLength(1))
    // Counted from when it was sent (waiting moved the clock on a little).
    vi.advanceTimersByTime(
      go.requests[0].at + eventsTiming.requestTimeoutMs - 1 - Date.now()
    )
    await drain()
    expect(go.requests[0].aborted).toBeUndefined()

    vi.advanceTimersByTime(1)
    await draining
    expect(go.requests[0].aborted).toEqual(
      expect.objectContaining({ name: "TimeoutError" })
    )
    expect(errors).toHaveBeenCalledWith(
      expect.stringContaining(
        "Go did not take 1 events (Go did not answer within 10 s)"
      ),
      expect.anything()
    )
    expect(outbox(dm).map((row) => row.id)).toEqual(ids)
    // A second on, in the whole second the SDK keeps.
    const [wait] = drainsSet(dm)
    expect(wait).toBeGreaterThanOrEqual(1)
    expect(wait).toBeLessThan(2)
    release()
    await runNextDrain(dm)
    expect(go.taken.map((event) => event.id)).toEqual(ids)
  })

  it("waits after any other answer or none, from one second doubling to five minutes, and never under a second", async () => {
    const { dm } = await setup()
    go.answers.push(
      401,
      503,
      500,
      502,
      new TypeError("fetch failed"),
      503,
      503,
      503,
      503,
      503,
      503
    )
    const [id] = enqueue(dm, 1)
    await dm.drainEvents()
    const waits: number[] = []
    for (let attempt = 1; attempt <= 11; attempt += 1) {
      expect(outbox(dm)).toEqual([{ id }])
      expect(backoffOf(dm)).toEqual([
        { failures: attempt, until: Date.now() + drainsSet(dm)[0] * 1000 },
      ])
      waits.push(...drainsSet(dm))
      await runNextDrain(dm)
    }
    expect(waits).toEqual([1, 2, 4, 8, 16, 32, 64, 128, 256, 300, 300])
    expect(go.requests.map((request) => request.status)).toEqual([
      401,
      503,
      500,
      502,
      undefined,
      503,
      503,
      503,
      503,
      503,
      503,
      204,
    ])
    // The same event, under the same id, every time.
    expect(
      go.requests.map((request) => request.events.map((event) => event.id))
    ).toEqual(Array.from({ length: 12 }, () => [id]))
    expect(outbox(dm)).toEqual([])
    expect(drainsSet(dm)).toEqual([])
    // Taken: Go is waited for no more.
    expect(backoffOf(dm)).toEqual([])
    expect(errors).toHaveBeenCalledWith(
      expect.stringContaining("Go did not take 1 events (Go answered 401)"),
      expect.anything()
    )
  })

  it("keeps Go's wait as one value: what Go does not take, and a message meanwhile, cost as much with a thousand events waiting as with one", async () => {
    const costs: Array<Record<string, number>> = []
    const statements: string[] = []
    for (const waiting of [1, 1_000]) {
      const { dm } = await setup()
      const adaTab = await join(dm, ada)
      enqueue(dm, waiting)
      go.answers.push(503, 503)
      const refused = await sqlOf(dm, () => dm.drainEvents())
      const refusedAgain = await sqlOf(dm, () => runNextDrain(dm))
      const recorded = await sqlOf(dm, () => send(dm, adaTab, "meanwhile"))
      costs.push({
        refused: refused.rows,
        refusedAgain: refusedAgain.rows,
        recorded: recorded.rows,
      })
      statements.push(
        ...refused.statements,
        ...refusedAgain.statements,
        ...recorded.statements
      )
      expect(go.requests.map((request) => request.status)).toEqual([503, 503])
      go = new FakeGoEvents()
      vi.stubGlobal("fetch", routeFetch(go))
    }
    expect(costs[1]).toEqual(costs[0])
    // Nothing reads the outbox or the dead letters whole.
    expect(
      statements.filter(
        (statement) =>
          /event_(outbox|dead_letters)/.test(statement) &&
          /\b(MAX|MIN|COUNT)\s*\(/i.test(statement)
      )
    ).toEqual([])
  })

  it("keeps what is recorded while it waits waiting with it, in the same request", async () => {
    const { dm } = await setup()
    go.answers.push(503, 503, 503)
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    await send(dm, adaTab, "one")
    await runDueJobs(dm)
    await runNextDrain(dm)
    await runNextDrain(dm)
    expect(drainsSet(dm)).toEqual([4])

    // No drain of its own, and no sooner: it waits with the rest, behind
    // them, even across a restart.
    await send(dm, adaTab, "two")
    await say(dm, boTab, { type: "dm-read", seq: 2 })
    expect(drainsSet(dm)).toEqual([4])
    dm.jobs = []
    await dm.onStart()
    expect(drainsSet(dm)).toEqual([4])
    await runNextDrain(dm)
    const together = go.requests[3]
    expect(
      together.events.map((event) => [
        event.type,
        (event.detail.message as { seq: number } | undefined)?.seq ??
          event.detail.read_through_seq,
      ])
    ).toEqual([
      [realtimeEventTypes.messagePosted, 1],
      [realtimeEventTypes.messagePosted, 2],
      [realtimeEventTypes.conversationRead, 2],
    ])
    expect(together.events.at(-1)?.detail.reader_user_id).toBe(bo)
    expect(together.status).toBe(204)
    expect(outbox(dm)).toEqual([])
  })

  it("costs a DM's message two outbox rows and one schedule: no read of its author's, and nothing in event_meta", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    const schedule = vi.spyOn(
      dm as unknown as {
        schedule: (when: Date, callback: string) => Promise<{ id: string }>
      },
      "schedule"
    )
    const { writes } = await sqlOf(dm, async () => {
      await send(dm, adaTab, "one")
      await runDueJobs(dm)
    })
    expect(go.taken).toHaveLength(1)
    // Its event in, then out once Go took it.
    expect(rowsIn(writes, "event_outbox")).toBe(2)
    expect(rowsIn(writes, "event_meta")).toBe(0)
    expect(rowsIn(writes, "event_dead_letters")).toBe(0)
    expect(dm.sql`SELECT * FROM event_meta`).toEqual([])
    // One drain was set, and it has run.
    expect(
      schedule.mock.calls.filter(([, callback]) => callback === "drainEvents")
    ).toHaveLength(1)
    expect(drainsSet(dm)).toEqual([])
  })

  it("never moves a drain that is set, and sets none while one as early is: what is recorded meanwhile goes with it", async () => {
    const { dm } = await setup()
    const cancel = vi.spyOn(
      dm as unknown as { cancelSchedule: (id: string) => Promise<boolean> },
      "cancelSchedule"
    )
    go.answers.push(400)
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    await send(dm, adaTab, "refused")
    await runDueJobs(dm)
    // A dead letter: a drain is set to let it go in seven days.
    const drains = () => drainIds(dm)
    const [later] = drains()
    expect(drainsSet(dm)).toEqual([eventsTiming.retentionMs / 1000])

    // A message: a drain for it, sooner, beside the later one.
    vi.advanceTimersByTime(5_000)
    await send(dm, adaTab, "two")
    expect(drains()).toEqual([later, expect.any(String)])
    const [, sooner] = drains()
    // More, before it runs: they go with it.
    await say(dm, boTab, { type: "dm-read", seq: 2 })
    await send(dm, boTab, "three")
    expect(drains()).toEqual([later, sooner])
    await runDueJobs(dm)
    expect(go.requests.at(-1)?.events.map((event) => event.type)).toEqual([
      realtimeEventTypes.messagePosted,
      realtimeEventTypes.conversationRead,
      realtimeEventTypes.messagePosted,
    ])
    // The later one is still the one set, as it was: never cancelled.
    expect(drains()).toEqual([later])
    expect(cancel.mock.calls.map(([id]) => id)).not.toContain(later)
    expect(cancel.mock.calls.map(([id]) => id)).not.toContain(sooner)
  })

  it("sets no second drain after a restart, nor while one runs that Go keeps waiting", async () => {
    const { world, dm } = await setup()
    go.answers.push(503)
    const adaTab = await join(dm, ada)
    await send(dm, adaTab, "one")
    await runDueJobs(dm)
    expect(drainsSet(dm)).toEqual([1])
    const restarted = await world.restart<Dm>("DirectMessageAgent", dmName)
    expect(drainsSet(restarted)).toEqual([1])

    // Go holds the next request: what is recorded meanwhile sets nothing,
    // and the drain sets the one after as it ends.
    const [running] = drainIds(restarted)
    const release = go.hold()
    vi.advanceTimersByTime(1_000)
    const draining = runDueJobs(restarted)
    await vi.waitFor(() => expect(go.requests).toHaveLength(2))
    await send(restarted, await join(restarted, bo), "meanwhile")
    expect(drainIds(restarted)).toEqual([running])
    release()
    await draining
    // The drain that ran is gone; the one it set for what came meanwhile
    // is the only one.
    const [next, ...more] = drainIds(restarted)
    expect(more).toEqual([])
    expect(next).not.toBe(running)
    await runNextDrain(restarted)
    expect(go.taken.map((event) => event.type)).toEqual([
      realtimeEventTypes.messagePosted,
      realtimeEventTypes.messagePosted,
    ])
    expect(drainsSet(restarted)).toEqual([])
  })

  it("makes an earlier build's tables again as they are now, with what they held, and sends it in order", async () => {
    const { world, dm } = await setup()
    // As the build before kept them: a wait and a count of tries on every
    // event, and the ids indexed.
    dm.sql`DROP TABLE event_outbox`
    dm.sql`DROP TABLE event_dead_letters`
    dm.sql`CREATE TABLE event_outbox (
      position INTEGER PRIMARY KEY,
      id TEXT NOT NULL UNIQUE,
      type TEXT NOT NULL,
      detail TEXT NOT NULL,
      reader_id TEXT UNIQUE,
      created_at INTEGER NOT NULL,
      attempts INTEGER NOT NULL DEFAULT 0,
      next_at INTEGER NOT NULL
    )`
    dm.sql`CREATE TABLE event_dead_letters (
      id TEXT PRIMARY KEY,
      type TEXT NOT NULL,
      detail TEXT NOT NULL,
      created_at INTEGER NOT NULL,
      attempts INTEGER NOT NULL,
      status INTEGER NOT NULL,
      answer TEXT NOT NULL,
      dead_at INTEGER NOT NULL
    )`
    const now = Date.now()
    const detail = (seq: number) =>
      JSON.stringify({
        organization_id: org,
        conversation: { kind: "direct", id: dmName },
        participant_user_ids: [ada, bo],
        message: { id: `dmm_${seq}`, seq },
        author: { kind: "member", user_id: ada },
        posted_at: new Date(now).toISOString(),
      })
    const read = JSON.stringify({
      organization_id: org,
      conversation: { kind: "direct", id: dmName },
      reader_user_id: bo,
      read_through_seq: 1,
      read_at: new Date(now).toISOString(),
    })
    dm.sql`INSERT INTO event_outbox
        (position, id, type, detail, reader_id, created_at, attempts, next_at)
      VALUES
        (4, 'evt_early_1', ${realtimeEventTypes.messagePosted}, ${detail(1)}, NULL, ${now}, 2, ${now}),
        (7, 'evt_early_2', ${realtimeEventTypes.conversationRead}, ${read}, ${bo}, ${now}, 2, ${now}),
        (9, 'evt_early_3', ${realtimeEventTypes.messagePosted}, ${detail(2)}, NULL, ${now}, 0, ${now})`
    dm.sql`INSERT INTO event_dead_letters
        (id, type, detail, created_at, attempts, status, answer, dead_at)
      VALUES
        ('evt_dead_2', ${realtimeEventTypes.messagePosted}, ${detail(0)}, ${now - 2}, 1, 400, '{}', ${now - 2}),
        ('evt_dead_1', ${realtimeEventTypes.messagePosted}, ${detail(0)}, ${now - 1}, 1, 400, '{}', ${now - 1})`

    const restarted = await world.restart<Dm>("DirectMessageAgent", dmName)
    const columns = (table: string) =>
      restarted
        .sql<{
          name: string
        }>([`PRAGMA table_info(${table})`] as unknown as TemplateStringsArray)
        .map((column) => column.name)
    expect(columns("event_outbox")).toEqual([
      "position",
      "id",
      "type",
      "detail",
      "reader_id",
      "created_at",
    ])
    expect(columns("event_dead_letters")).toEqual([
      "id",
      "type",
      "detail",
      "created_at",
      "status",
      "answer",
      "dead_at",
    ])
    // Only reads are indexed, by their reader.
    expect(
      restarted.sql`SELECT name, sql FROM sqlite_master
        WHERE type = 'index' AND tbl_name = 'event_outbox'`
    ).toEqual([
      {
        name: "event_outbox_by_reader",
        sql: expect.stringContaining(
          "(reader_id) WHERE reader_id IS NOT NULL"
        ) as unknown,
      },
    ])
    expect(
      restarted.sql`SELECT name FROM sqlite_master WHERE name LIKE '%earlier%'`
    ).toEqual([])
    // What they held, in the same order.
    expect(
      restarted.sql`SELECT position, id, reader_id FROM event_outbox
        ORDER BY position`
    ).toEqual([
      { position: 4, id: "evt_early_1", reader_id: null },
      { position: 7, id: "evt_early_2", reader_id: bo },
      { position: 9, id: "evt_early_3", reader_id: null },
    ])
    expect(
      restarted.sql`SELECT id FROM event_dead_letters ORDER BY rowid`
    ).toEqual([{ id: "evt_dead_2" }, { id: "evt_dead_1" }])

    // It goes on as ever: Bo's newer read takes his old one's place, and
    // it all goes, oldest first.
    restarted.sql`INSERT INTO dm_messages
        (seq, id, author_id, client_id, body, created_at)
      VALUES (1, 'dmm_1', ${ada}, 'client-early', 'Before',
        ${new Date(now).toISOString()})`
    const boTab = await join(restarted, bo)
    await say(restarted, boTab, { type: "dm-read", seq: 1 })
    await runDueJobs(restarted)
    expect(go.taken.map((event) => event.id)).toEqual([
      "evt_early_1",
      "evt_early_3",
      expect.stringMatching(/^evt_[0-9a-f]{32}$/),
    ])
    expect(go.taken.at(-1)?.detail).toMatchObject({
      reader_user_id: bo,
      read_through_seq: 1,
    })
    expect(outbox(restarted)).toEqual([])
  })

  it("drops what Go has not taken in seven days, with an error log", async () => {
    const { dm } = await setup()
    go.answers.push(...Array.from({ length: 20 }, () => 503))
    const recordedAt = Date.now()
    const [stale] = enqueue(dm, 1)
    await dm.drainEvents()
    // Go stays away: the drain comes back every five minutes at most.
    while (drainsSet(dm)[0] < 300) await runNextDrain(dm)
    expect(outbox(dm)).toHaveLength(1)

    vi.setSystemTime(recordedAt + eventsTiming.retentionMs)
    const younger = enqueue(dm, 1, recordedAt + 1)
    const sent = go.requests.length
    await runDueJobs(dm)
    expect(errors).toHaveBeenCalledWith(
      expect.stringContaining("dropped 1 events Go did not take in 7 days"),
      expect.objectContaining({ conversation: dmName, ids: [stale] })
    )
    // The one a millisecond younger still went.
    expect(go.requests.slice(sent).map((request) => request.events)).toEqual([
      [expect.objectContaining({ id: younger[0] })],
    ])
  })

  it("sends what waits after a restart that lost its schedule", async () => {
    const { world, dm } = await setup()
    const adaTab = await join(dm, ada)
    await send(dm, adaTab, "before the restart")
    // As a restart that lost the drain it had set.
    dm.jobs = []
    const restarted = await world.restart<Dm>("DirectMessageAgent", dmName)
    expect(drainsSet(restarted)).toEqual([0])
    await runDueJobs(restarted)
    expect(go.taken.map((event) => event.type)).toEqual([
      realtimeEventTypes.messagePosted,
    ])
    expect(outbox(restarted)).toEqual([])
  })
})

describe("EventOutbox: purges", () => {
  it("takes the events waiting and the dead letters with the conversation, lets its drain go, and never sends them", async () => {
    const { dm } = await setup()
    go.answers.push(400, 503)
    const adaTab = await join(dm, ada)
    const boTab = await join(dm, bo)
    await send(dm, adaTab, "refused")
    await say(dm, boTab, { type: "dm-read", seq: 1 })
    await runDueJobs(dm)
    vi.advanceTimersByTime(1000)
    await send(dm, adaTab, "waiting")
    await say(dm, boTab, { type: "dm-read", seq: 2 })
    await runDueJobs(dm)
    expect(deadLetters(dm)).toHaveLength(2)
    expect(outbox(dm)).toHaveLength(2)
    // The dead letters' drain, in seven days, and the waiting events'.
    expect(drainsSet(dm)).toEqual([eventsTiming.retentionMs / 1000 - 1, 1])

    await dm.purge()
    expect(outbox(dm)).toEqual([])
    expect(deadLetters(dm)).toEqual([])
    expect(drainsSet(dm)).toEqual([])
    vi.advanceTimersByTime(eventsTiming.maxRetryMs * 2)
    await runDueJobs(dm)
    expect(go.requests).toHaveLength(2)
  })

  it("takes them with a member's purge that keeps the conversation, and sends what is said after", async () => {
    const { dm } = await setup()
    go.answers.push(503)
    const adaTab = await join(dm, ada)
    await send(dm, adaTab, "before she left")
    await runDueJobs(dm)
    vi.setSystemTime(new Date("2026-09-24T09:01:00.000Z"))
    const boTab = await join(dm, bo)
    await say(dm, boTab, { type: "dm-read", seq: 1 })
    await send(dm, boTab, "after")
    expect(outbox(dm)).toHaveLength(3)

    expect(
      await dm.purge({ userId: ada, removedAt: "2026-09-24T09:00:30.000Z" })
    ).toEqual({ emptied: false })
    expect(outbox(dm)).toEqual([])
    expect(drainsSet(dm)).toEqual([])

    await send(dm, boTab, "welcome back")
    await runNextDrain(dm)
    expect(go.taken.map((event) => event.detail)).toEqual([
      expect.objectContaining({
        message: expect.objectContaining({ seq: 3 }) as unknown,
        author: { kind: "member", user_id: bo },
      }),
    ])
  })

  it("sends nothing a drain had read and was signing when the purge came", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    await send(dm, adaTab, "about to go")
    // The drain reads what waits, then signs it.
    const draining = runDueJobs(dm)
    await dm.purge()
    await draining
    expect(go.requests).toEqual([])
    expect(outbox(dm)).toEqual([])
  })

  it("waits two seconds at most for a request on its way, then gives it up, and keeps nothing it carried", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    await send(dm, adaTab, "on its way")
    // Go never answers.
    go.hold()
    const draining = runDueJobs(dm)
    await vi.waitFor(() => expect(go.requests).toHaveLength(1))

    let purged = false
    const purging = dm.purge().then(() => {
      purged = true
    })
    await drain()
    vi.advanceTimersByTime(eventsTiming.purgeWaitMs - 1)
    await drain()
    expect(purged).toBe(false)
    expect(go.requests[0].aborted).toBeUndefined()

    vi.advanceTimersByTime(1)
    await purging
    await draining
    expect(go.requests[0].aborted).toEqual(
      expect.objectContaining({ name: "AbortError" })
    )
    // Given up on, it is not sent again: nothing it carried stays.
    expect(outbox(dm)).toEqual([])
    expect(deadLetters(dm)).toEqual([])
    expect(backoffOf(dm)).toEqual([])
    expect(drainsSet(dm)).toEqual([])
    expect(errors).not.toHaveBeenCalledWith(
      expect.stringContaining("Go did not take"),
      expect.anything()
    )
  })

  it("ends once Go has answered the request on its way, and keeps nothing it carried", async () => {
    const { dm } = await setup()
    const adaTab = await join(dm, ada)
    await send(dm, adaTab, "on its way")
    const release = go.hold()
    const draining = runDueJobs(dm)
    await vi.waitFor(() => expect(go.requests).toHaveLength(1))

    let purged = false
    const purging = dm.purge().then(() => {
      purged = true
    })
    // The purge goes as far as it can: what waited is gone, and it waits
    // for Go's answer.
    await drain()
    expect(outbox(dm)).toEqual([])
    expect(purged).toBe(false)

    // Go refuses it: after the purge, nothing it carried comes back.
    go.answers.push(400)
    release()
    await purging
    await draining
    expect(outbox(dm)).toEqual([])
    expect(deadLetters(dm)).toEqual([])
    expect(drainsSet(dm)).toEqual([])
    vi.advanceTimersByTime(eventsTiming.maxRetryMs)
    await runDueJobs(dm)
    expect(go.requests).toHaveLength(1)
  })
})
