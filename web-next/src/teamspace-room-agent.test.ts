import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { Mock } from "vitest"
import type { Connection, ConnectionContext } from "agents"
import { TeamspaceRoomAgent, roomTiming } from "./teamspace-room-agent"
import type { RoomConnectionState, RoomEnv } from "./teamspace-room-agent"
import type { RoomMessage, RoomSeat } from "./lib/rooms/client"
import type { TeamspaceRoomState } from "./lib/rooms/room-agent"
import { roomGoneCloseCode } from "./lib/rooms/room-agent"
import { seatsFor, teamspace } from "./test/teamspaces"

// The Agents SDK only runs in workerd. A stand-in base class records what
// the room asks of it: its synced state, its schedules, its sockets, and its
// storage (a SQLite-backed KV, kept here in a sorted map). `fetch` is Go.
const sdk = vi.hoisted(() => {
  class Kv {
    readonly map = new Map<string, unknown>()
    get<T>(key: string): T | undefined {
      const value = this.map.get(key)
      return value === undefined ? undefined : structuredClone(value as T)
    }
    put(key: string, value: unknown) {
      this.map.set(key, structuredClone(value))
    }
    delete(key: string) {
      return this.map.delete(key)
    }
    list<T>({
      prefix = "",
      reverse = false,
      limit = Infinity,
    }: { prefix?: string; reverse?: boolean; limit?: number } = {}) {
      const keys = [...this.map.keys()]
        .filter((key) => key.startsWith(prefix))
        .sort()
      if (reverse) keys.reverse()
      return keys
        .slice(0, limit)
        .map((key) => [key, structuredClone(this.map.get(key))] as [string, T])
    }
  }
  return { Kv, scheduleIds: { next: 0 } }
})

type Job = { id: string; callback: string; time: number; payload: unknown }

vi.mock("agents", () => {
  class Agent {
    name = "org_sim~tsp_launch"
    env: unknown
    ctx: { storage: { kv: InstanceType<typeof sdk.Kv> } }
    initialState: unknown = undefined
    stored: unknown = undefined
    sockets: Array<{ open: boolean; send: (data: string) => void }> = []
    jobs: Job[] = []
    constructor(
      ctx: { storage: { kv: InstanceType<typeof sdk.Kv> } },
      env: unknown
    ) {
      this.ctx = ctx
      this.env = env
    }
    get state() {
      return this.stored ?? this.initialState
    }
    validateStateChange(_state: unknown, _source: unknown) {}
    // As the SDK: sent to every socket open whose protocol is on.
    setState(state: unknown) {
      this.validateStateChange(state, "server")
      this.stored = state
      for (const open of this.getConnections()) {
        if (!this.isConnectionProtocolEnabled(open)) continue
        open.send(JSON.stringify({ type: "cf_agent_state", state }))
      }
    }
    isConnectionProtocolEnabled(_connection: unknown) {
      return true
    }
    getConnections() {
      return this.sockets.filter((held) => held.open)
    }
    // As the SDK: whole seconds, rounded down.
    async schedule(when: Date | number, callback: string, payload?: unknown) {
      sdk.scheduleIds.next += 1
      const time =
        when instanceof Date
          ? Math.floor(when.getTime() / 1000)
          : Math.floor((Date.now() + when * 1000) / 1000)
      const job = { id: `job-${sdk.scheduleIds.next}`, callback, time, payload }
      this.jobs.push(job)
      return job
    }
    async cancelSchedule(id: string) {
      const before = this.jobs.length
      this.jobs = this.jobs.filter((job) => job.id !== id)
      return this.jobs.length < before
    }
    async keepAliveWhile<T>(work: () => Promise<T>) {
      return work()
    }
  }
  // Another object, as the room reaches one: the test's stand-in in `env`.
  async function getAgentByName(
    namespace: { get: (name: string) => unknown },
    name: string
  ) {
    return namespace.get(name)
  }
  const MessageType = { CF_AGENT_STATE: "cf_agent_state" }
  return { Agent, MessageType, getAgentByName }
})

const app = "http://localhost:3004"
const go = "http://go.test"
const room = "/v1/organizations/org_sim/teamspaces/tsp_launch"
const space = teamspace()
const seats: RoomSeat[] = seatsFor(space, ["user-member"])

/** A socket, as the SDK hands the room one. */
function socket(id: string) {
  return {
    id,
    open: true,
    state: null as RoomConnectionState | null,
    sent: [] as Array<Record<string, unknown>>,
    closed: undefined as { code?: number; reason?: string } | undefined,
    setState(next: RoomConnectionState) {
      if (!this.open) throw new Error("closed")
      this.state = next
      return next
    },
    send(data: string) {
      if (!this.open) throw new Error("closed")
      this.sent.push(JSON.parse(data))
    },
    close(code?: number, reason?: string) {
      this.open = false
      this.closed = { code, reason }
    },
  }
}
type Socket = ReturnType<typeof socket>

function roomMessages(sock: Socket): number[] {
  return sock.sent.flatMap((message) =>
    message.type === "room-messages" || message.type === "room-snapshot"
      ? (message.messages as RoomMessage[]).map((item) => item.seq)
      : []
  )
}

function sentOf(sock: Socket, type: string) {
  return sock.sent.filter((message) => message.type === type)
}

function said(
  seq: number,
  author = "member",
  body = `line ${seq}`
): RoomMessage {
  const agent = author !== "member" && author !== "ada"
  return {
    id: `rmg_${seq}`,
    room_id: "tsp_launch",
    seq,
    author_handle: author,
    author_kind: agent ? "agent" : "person",
    author_user_id: agent ? "user-ada" : `user-${author}`,
    body,
    created_at: "2026-09-24T09:00:00Z",
  }
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function goError(status: number, code: string, message: string) {
  return json({ error: { code, message } }, status)
}

type GoCall = {
  method: string
  path: string
  cookie: string | null
  origin: string | null
  body: unknown
}

/**
 * How somebody stands with the team, as Go answers for their cookie: on it
 * (`member`); `left`, which Go answers 404 to for the teamspace and its room;
 * or `manager`, a manager of the team who is not on it, whom Go shows the
 * teamspace to with `is_member` false and still lets read the room. Only
 * those on the team have seats, unless `unseated` (no agent of their own).
 */
type Standing = "member" | "left" | "manager"

/**
 * Go's room, as agents/primary/internal/rooms answers: reads after `after`,
 * a catch-up, and a post that answers only when the test says so; and the
 * teamspace itself, which says whether the cookie is on the team. Each
 * member is their cookie: `oryxa_access=member` is user-member.
 */
function fakeGo(held: RoomMessage[] = []) {
  const calls: GoCall[] = []
  const refusals: Array<(call: GoCall) => Response | undefined> = []
  const standings = new Map<string, Standing>()
  const unseated = new Set<string>()
  const holds = new Map<string, Promise<void>>()
  // Go's `paused` on reads: unset, as a Go before read-only rooms sends it.
  let paused: boolean | undefined
  const flag = () => (paused === undefined ? {} : { paused })
  let answerPost: (call: GoCall) => Promise<Response> = async () =>
    json({ messages: [] }, 201)
  const standing = (member: string | undefined) =>
    (member && standings.get(member)) || "member"
  fetchMock.mockImplementation(
    async (input: URL | string, init: RequestInit = {}) => {
      const url = new URL(String(input))
      const headers = new Headers(init.headers)
      const call: GoCall = {
        method: init.method ?? "GET",
        path: `${url.pathname}${url.search}`,
        cookie: headers.get("Cookie"),
        origin: headers.get("Origin"),
        body: typeof init.body === "string" ? JSON.parse(init.body) : undefined,
      }
      calls.push(call)
      const member = /oryxa_access=([\w-]+)/.exec(call.cookie ?? "")?.[1]
      await holds.get(`${call.method} ${url.pathname} ${member}`)
      for (const refuse of refusals) {
        const refused = refuse(call)
        if (refused) return refused
      }
      if (url.pathname === "/v1/auth/me") {
        return json({ user: { id: `user-${member}` }, organizations: [] })
      }
      if (url.pathname.startsWith(room) && standing(member) === "left") {
        return goError(404, "not_found", "teamspace not found")
      }
      if (url.pathname === room) {
        return json({ ...space, is_member: standing(member) === "member" })
      }
      if (url.pathname === `${room}/catchup`) {
        return json({
          brief: { through_seq: 0 },
          messages: held.slice(0, 200),
          ...flag(),
        })
      }
      if (url.pathname === `${room}/messages` && call.method === "GET") {
        const after = Number(url.searchParams.get("after") ?? 0)
        const limit = Number(url.searchParams.get("limit") ?? 200)
        const seated = (userId: string) => {
          const name = userId.replace(/^user-/, "")
          return standing(name) === "member" && !unseated.has(name)
        }
        return json({
          messages: held.filter((item) => item.seq > after).slice(0, limit),
          seats: seats.filter((seat) =>
            seated(seat.speaks_for ?? seat.user_id)
          ),
          ...flag(),
        })
      }
      if (url.pathname === `${room}/messages` && call.method === "POST") {
        return answerPost(call)
      }
      return goError(404, "not_found", "no such route")
    }
  )
  return {
    held,
    calls,
    /** Room reads (and catch-ups) made with a member's cookie. */
    readsAs: (member: string) =>
      calls.filter(
        (call) =>
          call.method === "GET" &&
          call.path.startsWith(`${room}/`) &&
          call.cookie === `oryxa_access=${member}`
      ).length,
    /** Times Go was asked, with a member's cookie, whether they are on the team. */
    askedAbout: (member: string) =>
      calls.filter(
        (call) => call.path === room && call.cookie === `oryxa_access=${member}`
      ).length,
    refuse: (refusal: (call: GoCall) => Response | undefined) =>
      refusals.push(refusal),
    posts: () => calls.filter((call) => call.method === "POST"),
    /** What Go's reads say of `paused` from now on (undefined: nothing). */
    pause: (now: boolean | undefined) => {
      paused = now
    },
    onPost: (answer: (call: GoCall) => Promise<Response>) => {
      answerPost = answer
    },
    /** How `member` stands with the team from now on. */
    stand: (member: string, now: Standing) => standings.set(member, now),
    /** `member` has no seat, though on the team: no agent of their own. */
    unseat: (member: string) => unseated.add(member),
    /**
     * Go answers `method path` for `member` only once the returned release
     * is called: the call is made, and waits.
     */
    hold: (method: string, path: string, member: string) => {
      const gate = deferred<void>()
      holds.set(`${method} ${path} ${member}`, gate.promise)
      return () => {
        holds.delete(`${method} ${path} ${member}`)
        gate.resolve()
      }
    },
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((settle) => {
    resolve = settle
  })
  return { promise, resolve }
}

type Harness = TeamspaceRoomAgent & {
  sockets: Socket[]
  jobs: Job[]
  stored: TeamspaceRoomState | undefined
  ctx: { storage: { kv: InstanceType<typeof sdk.Kv> } }
}

/**
 * The organization's registry, as the room reaches it: the rooms registered
 * with it, by registry, and whether it answers.
 */
function fakeRegistry() {
  const registered: Array<{ registry: string; teamspaceId: string }> = []
  let answering = true
  return {
    registered,
    answers: (now: boolean) => {
      answering = now
    },
    get: (registry: string) => ({
      registerRoom: async (teamspaceId: string) => {
        if (!answering) throw new Error("Durable Object reset")
        registered.push({ registry, teamspaceId })
      },
    }),
  }
}

function makeRoom(registry = fakeRegistry()): Harness {
  const agent = new TeamspaceRoomAgent(
    { storage: { kv: new sdk.Kv() } } as unknown as DurableObjectState,
    {
      ORYXA_API_ORIGIN: go,
      RealtimeRegistryAgent: registry,
    } as unknown as RoomEnv
  )
  return agent as unknown as Harness
}

function upgrade(
  member: string,
  headers: Record<string, string> = {}
): ConnectionContext {
  return {
    request: new Request(
      `${app}/agents/teamspace-room-agent/org_sim~tsp_launch`,
      {
        headers: {
          Origin: app,
          "X-Oryxa-Cookie": `oryxa_access=${member}`,
          "X-Oryxa-Organization": "org_sim",
          "X-Oryxa-Teamspace": "tsp_launch",
          ...headers,
        },
      }
    ),
  }
}

let sockets = 0

/**
 * Opens a socket as `member`, and says whether its tab is visible, as the
 * page does (`null`: says nothing yet).
 */
async function join(
  agent: Harness,
  member: string,
  visible: boolean | null = true
): Promise<Socket> {
  sockets += 1
  const sock = socket(`socket-${sockets}`)
  agent.sockets.push(sock)
  await agent.onConnect(sock as unknown as Connection, upgrade(member))
  if (visible !== null) await see(agent, sock, visible)
  return sock
}

async function see(agent: Harness, sock: Socket, visible: boolean) {
  await agent.onMessage(
    sock as unknown as Connection,
    JSON.stringify({ type: "room-visibility", visible })
  )
}

function post(agent: Harness, sock: Socket, id: string, body: string) {
  return agent.onMessage(
    sock as unknown as Connection,
    JSON.stringify({ type: "room-post", id, body })
  )
}

async function leave(agent: Harness, sock: Socket) {
  sock.open = false
  await agent.onClose(sock as unknown as Connection)
}

const start = new Date("2026-09-24T09:00:00.000Z").getTime()

function later(ms: number) {
  vi.setSystemTime(Date.now() + ms)
}

/** The ticks set, as times from now in ms. */
function ticks(agent: Harness): number[] {
  return agent.jobs
    .filter((job) => job.callback === "tick")
    .map((job) => job.time * 1000 - Date.now())
}

/** Runs every schedule due by now, as the SDK's alarm does, oldest first. */
async function fire(agent: Harness) {
  for (;;) {
    const due = agent.jobs
      .filter((job) => job.time * 1000 <= Date.now())
      .sort((left, right) => left.time - right.time)
      .at(0)
    if (!due) return
    agent.jobs = agent.jobs.filter((job) => job !== due)
    const callback = (
      agent as unknown as Record<string, (...args: unknown[]) => Promise<void>>
    )[due.callback]
    await callback.call(agent, due.payload, { ...due, type: "scheduled" })
  }
}

/** Moves the clock on, a second at a time, running what comes due. */
async function run(agent: Harness, ms: number) {
  const end = Date.now() + ms
  while (Date.now() < end) {
    later(Math.min(1000, end - Date.now()))
    await fire(agent)
  }
}

let fetchMock: Mock<
  (input: URL | string, init?: RequestInit) => Promise<Response>
>

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] })
  vi.setSystemTime(start)
  fetchMock = vi.fn()
  vi.stubGlobal("fetch", fetchMock)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("TeamspaceRoomAgent: sockets", () => {
  it("keeps what the Worker routed the socket with, and learns who it is and that they are on the team without reading the room", async () => {
    const goRoom = fakeGo([said(1)])
    const registry = fakeRegistry()
    const agent = makeRoom(registry)
    const sock = await join(agent, "member", null)

    expect(sock.closed).toBeUndefined()
    expect(sock.state).toEqual({
      cookie: "oryxa_access=member",
      organizationId: "org_sim",
      teamspaceId: "tsp_launch",
      origin: app,
      connectedAt: new Date(start).toISOString(),
      visible: false,
      userId: "user-member",
    })
    // Reading marks the member present: nothing is read until the page says
    // whether anybody is looking. The teamspace itself marks nobody.
    expect(goRoom.calls.map((call) => [call.path, call.cookie])).toEqual([
      ["/v1/auth/me", "oryxa_access=member"],
      [room, "oryxa_access=member"],
    ])
    expect(sentOf(sock, "room-snapshot")).toEqual([])
    // Let in, the room is in its organization's registry, once.
    await join(agent, "ada", null)
    expect(registry.registered).toEqual([
      { registry: "org_sim", teamspaceId: "tsp_launch" },
    ])
  })

  it("closes a socket the Worker did not route, or routed for another teamspace", async () => {
    fakeGo()
    const agent = makeRoom()
    const refused: Array<Record<string, string>> = [
      { "X-Oryxa-Cookie": "" },
      { "X-Oryxa-Teamspace": "tsp_other" },
      { "X-Oryxa-Organization": "org_other" },
    ]
    for (const headers of refused) {
      const sock = socket("refused")
      await agent.onConnect(
        sock as unknown as Connection,
        upgrade("member", headers)
      )
      expect(sock.closed).toEqual({ code: 1008, reason: "Unauthorized" })
      expect(sock.state).toBeNull()
    }
  })

  it("answers the Worker's check for a refused socket, and refuses a page's change to its state", async () => {
    const agent = makeRoom()
    expect(agent.onRequest(new Request(`${app}/x`)).status).toBe(204)
    expect(
      agent.onRequest(new Request(`${app}/x`, { method: "POST" })).status
    ).toBe(405)
    expect(() =>
      agent.validateStateChange(
        { seats: [], here: ["user-ada"] },
        {} as Connection
      )
    ).toThrow()
    expect(() =>
      agent.validateStateChange({ seats: [], here: [] }, "server")
    ).not.toThrow()
  })
})

describe("TeamspaceRoomAgent: reading", () => {
  it("reads Go as a member once their tab is visible, and sends the room with its seats and who is here", async () => {
    const goRoom = fakeGo([said(1), said(2, "ada"), said(3, "juno")])
    const agent = makeRoom()
    const sock = await join(agent, "member")

    expect(
      goRoom.calls
        .filter((call) => call.path.startsWith(room))
        .map((call) => [call.path.replace(room, ""), call.cookie])
    ).toEqual([
      // Let in: Go has them on the team.
      ["", "oryxa_access=member"],
      ["/catchup", "oryxa_access=member"],
      ["/messages?after=0&limit=200", "oryxa_access=member"],
    ])
    expect(sentOf(sock, "room-snapshot")).toEqual([
      {
        type: "room-snapshot",
        messages: [said(1), said(2, "ada"), said(3, "juno")],
        pending: [],
        results: [],
      },
    ])
    expect(agent.stored).toEqual({ seats, here: ["user-member"] })
    // The room is read again on the slow schedule while anybody is looking.
    expect(ticks(agent)).toEqual([roomTiming.slowMs])
  })

  it("sends a hidden tab what the room holds without reading Go for it, and makes it wait when it holds nothing", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const waiting = await join(agent, "ada", false)

    expect(goRoom.readsAs("ada")).toBe(0)
    expect(sentOf(waiting, "room-snapshot")).toEqual([])
    expect(ticks(agent)).toEqual([])

    // Somebody who is looking reads it, and the hidden tab is sent it too.
    const member = await join(agent, "member")
    expect(roomMessages(member)).toEqual([1])
    expect(roomMessages(waiting)).toEqual([1])
    const late = await join(agent, "ada", false)
    expect(roomMessages(late)).toEqual([1])
    expect(goRoom.readsAs("ada")).toBe(0)
    expect(agent.stored?.here).toEqual(["user-member"])
  })

  it("sends every socket each new message once, by seq, whichever way it arrived", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada")

    goRoom.held.push(said(2, "ada"), said(3, "juno"))
    await run(agent, roomTiming.slowMs)
    await run(agent, roomTiming.slowMs)
    expect(roomMessages(member)).toEqual([1, 2, 3])
    expect(roomMessages(ada)).toEqual([1, 2, 3])

    // A post's answer carries messages a read already sent: nothing twice.
    goRoom.onPost(async () =>
      json({ messages: [said(3, "juno"), said(4)] }, 201)
    )
    goRoom.held.push(said(4))
    await post(agent, member, "post-1", "line 4")
    await run(agent, roomTiming.slowMs)
    expect(roomMessages(member)).toEqual([1, 2, 3, 4])
    expect(roomMessages(ada)).toEqual([1, 2, 3, 4])
  })
})

describe("TeamspaceRoomAgent: posting", () => {
  it("posts as the socket's member and relays the room as Go records it, fast while the post is out", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada")
    later(roomTiming.slowMs)
    const answer = deferred<Response>()
    goRoom.onPost(() => answer.promise)

    const posting = post(agent, member, "post-1", "  Is the rollout ready?  ")
    await vi.waitFor(() => expect(goRoom.posts()).toHaveLength(1))
    expect(goRoom.posts()[0]).toMatchObject({
      path: `${room}/messages`,
      cookie: "oryxa_access=member",
      origin: app,
      body: { body: "  Is the rollout ready?  " },
    })
    // Until Go records the member's message, the room is read every second.
    expect(Math.min(...ticks(agent))).toBeLessThanOrEqual(0)

    const mine = said(2, "member", "Is the rollout ready?")
    goRoom.held.push(mine)
    await run(agent, roomTiming.landingMs)
    expect(roomMessages(ada)).toEqual([1, 2])
    const landedAt = Date.now()

    // Then every couple of seconds, so each reply reaches every socket as
    // Go records it, long before the post answers.
    const reply = said(3, "juno", "Yes, every step is checked.")
    goRoom.held.push(reply)
    await run(agent, roomTiming.fastMs)
    expect(roomMessages(ada)).toEqual([1, 2, 3])
    expect(roomMessages(member)).toEqual([1, 2, 3])
    expect(Date.now() - landedAt).toBeLessThanOrEqual(roomTiming.fastMs)

    answer.resolve(json({ messages: [mine, reply] }, 201))
    await posting
    // The answer is merged by seq: nothing twice, and only the author hears it.
    expect(roomMessages(member)).toEqual([1, 2, 3])
    expect(sentOf(member, "room-posted")).toEqual([
      {
        type: "room-posted",
        result: { id: "post-1", ok: true, messages: [mine, reply] },
      },
    ])
    expect(sentOf(ada, "room-posted")).toEqual([])
    // Fast for a while after the post, then slow again.
    expect(ticks(agent)).toEqual([expect.any(Number)])
    expect(ticks(agent)[0]).toBeLessThanOrEqual(roomTiming.fastMs)
    await run(agent, roomTiming.afterPostMs + roomTiming.fastMs)
    expect(ticks(agent)[0]).toBeGreaterThan(roomTiming.fastMs)
  })

  it("tells every socket of the author, and one that reopens, what became of their post", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const first = await join(agent, "member")
    const answer = deferred<Response>()
    goRoom.onPost(() => answer.promise)

    const posting = post(agent, first, "post-1", "Hello")
    await vi.waitFor(() => expect(goRoom.posts()).toHaveLength(1))
    await leave(agent, first)
    const reopened = await join(agent, "member")
    expect(sentOf(reopened, "room-snapshot")[0]).toMatchObject({
      pending: ["post-1"],
      results: [],
    })
    // Said again by the reopened page: not posted twice.
    await post(agent, reopened, "post-1", "Hello")
    expect(goRoom.posts()).toHaveLength(1)

    answer.resolve(json({ messages: [said(2, "member", "Hello")] }, 201))
    await posting
    expect(sentOf(reopened, "room-posted")).toEqual([
      {
        type: "room-posted",
        result: {
          id: "post-1",
          ok: true,
          messages: [said(2, "member", "Hello")],
        },
      },
    ])

    // A page that reopens after it finished finds the result in its snapshot.
    await leave(agent, reopened)
    const again = await join(agent, "member")
    expect(sentOf(again, "room-snapshot")[0]).toMatchObject({
      pending: [],
      results: [{ id: "post-1", ok: true }],
    })
    await post(agent, again, "post-1", "Hello")
    expect(goRoom.posts()).toHaveLength(1)
    expect(sentOf(again, "room-posted")).toHaveLength(1)
    // Another member cannot read it by its id.
    const ada = await join(agent, "ada")
    await post(agent, ada, "post-1", "Hello")
    expect(sentOf(ada, "room-posted")).toEqual([])
  })

  it("reports Go's refusals of a post to its author, in Go's words, after reading what the room kept", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")

    goRoom.onPost(async () =>
      goError(403, "forbidden", "room is not available to this member")
    )
    await post(agent, member, "post-1", "Hello")
    expect(sentOf(member, "room-posted").at(-1)).toEqual({
      type: "room-posted",
      result: {
        id: "post-1",
        ok: false,
        status: 403,
        code: "forbidden",
        message: "room is not available to this member",
      },
    })
    // No seat is not "gone": the member still reads the room.
    expect(member.closed).toBeUndefined()

    // Go recorded the message, then the answer was lost: the page is sent
    // the message before it hears the post failed, so it can tell.
    goRoom.onPost(async () => {
      goRoom.held.push(said(2, "member", "Hello again"))
      return goError(502, "bad_gateway", "upstream unavailable")
    })
    await post(agent, member, "post-2", "Hello again")
    const types = member.sent.map((message) => message.type)
    const landed = member.sent.findIndex(
      (message) =>
        message.type === "room-messages" &&
        (message.messages as RoomMessage[])[0].seq === 2
    )
    expect(landed).toBeGreaterThan(-1)
    expect(types.lastIndexOf("room-posted")).toBeGreaterThan(landed)

    // Go never answered at all.
    goRoom.onPost(async () => {
      throw new TypeError("fetch failed")
    })
    await post(agent, member, "post-3", "Anyone?")
    expect(sentOf(member, "room-posted").at(-1)).toEqual({
      type: "room-posted",
      result: {
        id: "post-3",
        ok: false,
        status: 0,
        message: "The room could not be reached.",
      },
    })
  })

  it("never says anything with a cookie Go refused, and tells the page to reopen", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")

    goRoom.onPost(async () => goError(401, "unauthorized", "session expired"))
    await post(agent, member, "post-1", "Hello")
    expect(sentOf(member, "room-status")).toEqual([
      { type: "room-status", status: "session_expired" },
    ])
    expect(sentOf(member, "room-posted")).toEqual([
      {
        type: "room-posted",
        result: {
          id: "post-1",
          ok: false,
          status: 401,
          code: "session_expired",
          message: "Your session has ended.",
        },
      },
    ])
    await post(agent, member, "post-2", "Hello?")
    expect(goRoom.posts()).toHaveLength(1)
    expect(sentOf(member, "room-posted").at(-1)).toMatchObject({
      result: { id: "post-2", ok: false, code: "session_expired" },
    })
  })
})

describe("TeamspaceRoomAgent: scheduling", () => {
  it("reads only while somebody is looking, and lets go of an unused room's copy", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    expect(ticks(agent)).toHaveLength(1)

    await see(agent, member, false)
    const reads = goRoom.readsAs("member")
    await run(agent, 5 * roomTiming.slowMs)
    expect(goRoom.readsAs("member")).toBe(reads)
    expect(ticks(agent)).toEqual([])

    // Nobody here: the copy goes after a while, unless somebody comes back.
    await leave(agent, member)
    const forget = agent.jobs.find((job) => job.callback === "forgetIfIdle")
    expect(forget && forget.time * 1000 - Date.now()).toBe(roomTiming.forgetMs)
    const back = await join(agent, "member")
    expect(agent.jobs.some((job) => job.callback === "forgetIfIdle")).toBe(
      false
    )
    await leave(agent, back)
    await run(agent, roomTiming.forgetMs)
    expect(agent.ctx.storage.kv.list({ prefix: "room:" })).toEqual([])
    expect(agent.stored).toEqual({ seats: [], here: [] })
  })

  it("reads on a tick the SDK runs up to a second early, and never sets ticks less than a second apart", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    later(900)
    await join(agent, "member")
    const reads = goRoom.readsAs("member")
    // Due 12.9 s in, the SDK keeps 12 s: its tick runs 0.9 s early, and
    // still reads rather than setting itself again for the same second.
    expect(ticks(agent)).toEqual([roomTiming.slowMs - 900])
    vi.setSystemTime(start + roomTiming.slowMs)
    await fire(agent)
    expect(goRoom.readsAs("member")).toBe(reads + 1)
    expect(ticks(agent)).toEqual([roomTiming.slowMs])

    // However often a tick runs, the next is set at least a second on, and
    // there is only ever one.
    for (let index = 0; index < 20; index += 1) {
      await agent.tick(undefined, undefined)
    }
    expect(ticks(agent)).toHaveLength(1)
    expect(ticks(agent)[0]).toBeGreaterThanOrEqual(roomTiming.minTickGapMs)
  })
})

describe("TeamspaceRoomAgent: presence", () => {
  it("reads as each visible member well inside Go's 90 s, never as a hidden one, and at once as one who comes back", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada")
    expect(agent.stored?.here).toEqual(["user-ada", "user-member"])

    // Both visible: each is read at least every 30 s.
    const readTimes = { member: [] as number[], ada: [] as number[] }
    fetchMock.mockImplementation(
      (
        (original) =>
        async (input: URL | string, init: RequestInit = {}) => {
          const cookie = new Headers(init.headers).get("Cookie") ?? ""
          if (String(input).includes("/messages")) {
            if (cookie.endsWith("member")) readTimes.member.push(Date.now())
            if (cookie.endsWith("ada")) readTimes.ada.push(Date.now())
          }
          return original(input, init)
        }
      )(fetchMock.getMockImplementation()!)
    )
    await run(agent, 120_000)
    for (const times of [readTimes.member, readTimes.ada]) {
      const gaps = times.slice(1).map((time, index) => time - times[index])
      expect(times.length).toBeGreaterThanOrEqual(4)
      expect(Math.max(...gaps)).toBeLessThanOrEqual(roomTiming.heartbeatMs)
    }

    // Ada hides her tab: she is no longer here, and never read for.
    await see(agent, ada, false)
    expect(agent.stored?.here).toEqual(["user-member"])
    const adaReads = goRoom.readsAs("ada")
    await run(agent, 180_000)
    expect(goRoom.readsAs("ada")).toBe(adaReads)
    expect(goRoom.readsAs("member")).toBeGreaterThan(6)

    // She comes back: read at once, which tells Go she is here.
    await see(agent, ada, true)
    expect(goRoom.readsAs("ada")).toBe(adaReads + 1)
    expect(agent.stored?.here).toEqual(["user-ada", "user-member"])

    // One member with two tabs is one member: read as one of them.
    const second = await join(agent, "member")
    expect(sentOf(second, "room-snapshot")).toHaveLength(1)
    await leave(agent, member)
    expect(agent.stored?.here).toEqual(["user-ada", "user-member"])
  })
})

describe("TeamspaceRoomAgent: refusals", () => {
  it("tells a cookie's sockets its session ended and never uses it again", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada")

    goRoom.refuse((call) =>
      call.cookie === "oryxa_access=member"
        ? goError(401, "unauthorized", "session expired")
        : undefined
    )
    await run(agent, 2 * roomTiming.heartbeatMs)
    expect(sentOf(member, "room-status")).toEqual([
      { type: "room-status", status: "session_expired" },
    ])
    expect(sentOf(ada, "room-status")).toEqual([])
    expect(agent.stored?.here).toEqual(["user-ada"])
    const refused = goRoom.calls.filter(
      (call) => call.cookie === "oryxa_access=member"
    ).length
    await run(agent, 2 * roomTiming.heartbeatMs)
    expect(
      goRoom.calls.filter((call) => call.cookie === "oryxa_access=member")
    ).toHaveLength(refused)
    expect(goRoom.readsAs("ada")).toBeGreaterThan(2)
  })

  it("closes a member Go no longer lets in, and keeps the room for those it does", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada")

    goRoom.refuse((call) =>
      call.cookie === "oryxa_access=member" && call.path.includes("/messages")
        ? goError(404, "not_found", "room not found")
        : undefined
    )
    await run(agent, roomTiming.heartbeatMs)
    expect(sentOf(member, "room-status")).toEqual([
      { type: "room-status", status: "gone", message: "room not found" },
    ])
    expect(member.closed?.code).toBe(roomGoneCloseCode)
    // Ada is asked for the teamspace, which marks nobody present; she is on
    // the team, so the room keeps its copy.
    expect(
      goRoom.calls.some(
        (call) => call.path === room && call.cookie === "oryxa_access=ada"
      )
    ).toBe(true)
    expect(ada.closed).toBeUndefined()
    expect(agent.ctx.storage.kv.list({ prefix: "room:message:" })).toHaveLength(
      1
    )
  })

  it("lets go of its copy once nobody here may open the room: the teamspace was deleted", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada", false)

    goRoom.refuse(() => goError(404, "not_found", "teamspace not found"))
    await run(agent, roomTiming.heartbeatMs)
    for (const sock of [member, ada]) {
      expect(sentOf(sock, "room-status").at(-1)).toMatchObject({
        status: "gone",
      })
      expect(sock.closed?.code).toBe(roomGoneCloseCode)
    }
    expect(agent.ctx.storage.kv.list({ prefix: "room:" })).toEqual([])
    expect(agent.stored).toEqual({ seats: [], here: [] })
  })

  it("tells every socket a paused room is paused and stops reading until a member's read succeeds", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada", false)

    let paused = true
    goRoom.refuse((call) =>
      paused && call.path.startsWith(`${room}/`)
        ? goError(400, "invalid_request", "this room is paused")
        : undefined
    )
    await run(agent, roomTiming.heartbeatMs)
    for (const sock of [member, ada]) {
      expect(sentOf(sock, "room-status")).toEqual([
        {
          type: "room-status",
          status: "paused",
          message: "this room is paused",
        },
      ])
    }
    const reads = goRoom.calls.length
    await run(agent, 3 * roomTiming.heartbeatMs)
    expect(goRoom.calls).toHaveLength(reads)
    expect(ticks(agent)).toEqual([])

    // Resumed: the next member to look finds it open again.
    paused = false
    goRoom.held.push(said(2, "ada"))
    await see(agent, member, false)
    await see(agent, member, true)
    expect(sentOf(member, "room-status").at(-1)).toEqual({
      type: "room-status",
      status: "live",
    })
    expect(roomMessages(member)).toEqual([1, 2])
    expect(ticks(agent)).toHaveLength(1)
  })

  it("keeps reading a room Go says is paused, read-only: every socket is told paused, after the room for one that opens, and live once resumed", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")

    goRoom.pause(true)
    goRoom.held.push(said(2, "ada"))
    await run(agent, roomTiming.heartbeatMs)
    expect(sentOf(member, "room-status")).toEqual([
      { type: "room-status", status: "paused" },
    ])
    // What was said is still read and sent, and the room keeps reading.
    expect(roomMessages(member)).toEqual([1, 2])
    expect(ticks(agent)).toHaveLength(1)

    // A page that opens now is sent the room, then told it is paused.
    const ada = await join(agent, "ada")
    expect(ada.sent.map((message) => message.type)).toEqual(
      expect.arrayContaining(["room-snapshot", "room-status"])
    )
    expect(roomMessages(ada)).toEqual([1, 2])
    const types = ada.sent.map((message) => message.type)
    expect(types.lastIndexOf("room-status")).toBeGreaterThan(
      types.indexOf("room-snapshot")
    )
    expect(sentOf(ada, "room-status")).toEqual([
      { type: "room-status", status: "paused" },
    ])

    // Still paused: nobody is told again.
    await run(agent, roomTiming.heartbeatMs)
    expect(sentOf(member, "room-status")).toHaveLength(1)

    goRoom.pause(false)
    await run(agent, roomTiming.heartbeatMs)
    for (const sock of [member, ada]) {
      expect(sentOf(sock, "room-status").at(-1)).toEqual({
        type: "room-status",
        status: "live",
      })
    }
  })

  it("tells every page a room is paused when Go refuses a post 409 teamspace_disabled", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada")

    goRoom.onPost(async () => {
      goRoom.pause(true)
      return goError(409, "teamspace_disabled", "teamspace is disabled")
    })
    await post(agent, member, "post-1", "Hello")
    expect(sentOf(member, "room-posted").at(-1)).toEqual({
      type: "room-posted",
      result: {
        id: "post-1",
        ok: false,
        status: 409,
        code: "teamspace_disabled",
        message: "teamspace is disabled",
      },
    })
    for (const sock of [member, ada]) {
      expect(sentOf(sock, "room-status")).toEqual([
        { type: "room-status", status: "paused" },
      ])
      expect(sock.closed).toBeUndefined()
    }
  })

  it("says a server without rooms is unavailable, and a Go that fails is trying again", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    vi.spyOn(console, "error").mockImplementation(() => {})

    let failing = true
    goRoom.refuse((call) =>
      failing && call.path.includes("/messages")
        ? goError(500, "internal", "internal server error")
        : undefined
    )
    await run(agent, 2 * roomTiming.heartbeatMs)
    expect(sentOf(member, "room-status")).toEqual([
      { type: "room-status", status: "error" },
    ])
    failing = false
    await run(agent, roomTiming.heartbeatMs)
    expect(sentOf(member, "room-status").at(-1)).toEqual({
      type: "room-status",
      status: "live",
    })

    goRoom.refuse((call) =>
      call.path.includes("/messages")
        ? goError(
            503,
            "rooms_unavailable",
            "teamspace conversations are not configured"
          )
        : undefined
    )
    await run(agent, roomTiming.heartbeatMs)
    expect(sentOf(member, "room-status").at(-1)).toEqual({
      type: "room-status",
      status: "unavailable",
      message: "teamspace conversations are not configured",
    })
    expect(ticks(agent)).toEqual([])
  })
})

describe("TeamspaceRoomAgent: membership", () => {
  it("closes a hidden tab of somebody who left the team, with no purge, once Go is asked in their turn, and never reads as them", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada", false)
    expect(roomMessages(ada)).toEqual([1])

    // Ada leaves the team, and no purge comes: Go stops seating her, and
    // answers 404 for the teamspace and its room.
    goRoom.stand("ada", "left")
    await run(agent, roomTiming.memberCheckMs + roomTiming.minTickGapMs)
    expect(sentOf(ada, "room-status")).toEqual([
      { type: "room-status", status: "gone", message: "teamspace not found" },
    ])
    expect(ada.closed?.code).toBe(roomGoneCloseCode)
    expect(goRoom.readsAs("ada")).toBe(0)

    // The room goes on for the team, and her tab hears none of it.
    goRoom.held.push(said(2, "juno"))
    await run(agent, roomTiming.slowMs)
    expect(roomMessages(member)).toEqual([1, 2])
    expect(roomMessages(ada)).toEqual([1])
    expect(member.closed).toBeUndefined()
  })

  it("sends a tab nothing once Go's word on its member is older than memberLeaseMs, and the room afresh once Go says it again", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada", false)
    // Ada has no agent of her own, so no read's seats vouch for her, and Go
    // cannot say for a while whether she is on the team.
    goRoom.unseat("ada")
    let failing = true
    goRoom.refuse((call) =>
      failing && call.path === room && call.cookie === "oryxa_access=ada"
        ? goError(500, "internal", "internal server error")
        : undefined
    )

    // Within Go's word on her, the room reaches her.
    goRoom.held.push(said(2, "juno"))
    await run(agent, roomTiming.slowMs)
    expect(roomMessages(ada)).toEqual([1, 2])
    // Past it, nothing does, and her tab stays open while Go is asked again.
    goRoom.held.push(said(3, "juno"))
    await run(agent, roomTiming.slowMs)
    expect(roomMessages(member)).toEqual([1, 2, 3])
    expect(roomMessages(ada)).toEqual([1, 2])
    expect(ada.closed).toBeUndefined()
    expect(goRoom.askedAbout("ada")).toBeGreaterThan(2)
    // Nor does the synced state: who is here changes, and she is not told.
    const told = sentOf(ada, "cf_agent_state").length
    await see(agent, member, false)
    await see(agent, member, true)
    expect(sentOf(member, "cf_agent_state").length).toBeGreaterThan(1)
    expect(sentOf(ada, "cf_agent_state")).toHaveLength(told)

    // Go says she is on the team: she is sent the room afresh, what she
    // missed with it, and the state as it stands.
    failing = false
    await run(agent, roomTiming.retryMs)
    expect(sentOf(ada, "room-snapshot")).toHaveLength(2)
    expect(roomMessages(ada)).toEqual([1, 2, 1, 2, 3])
    expect(sentOf(ada, "cf_agent_state").at(-1)).toEqual({
      type: "cf_agent_state",
      state: agent.stored,
    })
    expect(ada.closed).toBeUndefined()
  })

  it("asks Go before it reads as somebody it has not lately vouched for: a manager who left the team is closed and never read as, though Go would let them read", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const ada = await join(agent, "ada")
    await see(agent, ada, false)
    // Nobody looks for a while. Meanwhile ada leaves the team, though she
    // still manages it: Go shows her the teamspace with is_member false, and
    // would still answer her reads.
    goRoom.stand("ada", "manager")
    await run(agent, roomTiming.memberLeaseMs)
    const reads = goRoom.readsAs("ada")

    await see(agent, ada, true)
    expect(goRoom.readsAs("ada")).toBe(reads)
    expect(sentOf(ada, "room-status")).toEqual([
      {
        type: "room-status",
        status: "gone",
        message: "Only the teamspace's team can open its room.",
      },
    ])
    expect(ada.closed?.code).toBe(roomGoneCloseCode)
    expect(agent.stored?.here).toEqual([])
  })

  it("refuses a new socket of somebody not on the team: told gone and closed, sent nothing of the room, and never read as", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    for (const [who, standing, message] of [
      ["ada", "left", "teamspace not found"],
      ["owen", "manager", "Only the teamspace's team can open its room."],
    ] as const) {
      goRoom.stand(who, standing)
      const sock = await join(agent, who)
      expect(sock.closed?.code).toBe(roomGoneCloseCode)
      expect(sock.sent).toEqual([
        { type: "room-status", status: "gone", message },
      ])
      expect(goRoom.readsAs(who)).toBe(0)
    }
    expect(member.closed).toBeUndefined()
    expect(roomMessages(member)).toEqual([1])
    expect(agent.stored?.here).toEqual(["user-member"])
  })
})

describe("TeamspaceRoomAgent: purges", () => {
  it("closes the sockets a member who left opened until then, lets go of what the room kept of them, and keeps a page opened since", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada")
    const adaHidden = await join(agent, "ada", false)
    goRoom.onPost(async () => json({ messages: [said(2, "ada", "Hi")] }, 201))
    await post(agent, ada, "post-1", "Hi")
    expect(agent.ctx.storage.kv.list({ prefix: "room:result:" })).toHaveLength(
      1
    )
    expect(agent.stored?.here).toEqual(["user-ada", "user-member"])
    const removedAt = new Date(Date.now()).toISOString()
    // Let back in since, on a page opened after she left.
    later(1_000)
    const back = await join(agent, "ada", false)

    await agent.purgeMember("user-ada", removedAt)

    for (const sock of [ada, adaHidden]) {
      expect(sentOf(sock, "room-status")).toEqual([
        { type: "room-status", status: "gone" },
      ])
      expect(sock.closed?.code).toBe(roomGoneCloseCode)
    }
    expect(back.closed).toBeUndefined()
    expect(member.closed).toBeUndefined()
    // What the room kept of her goes: her reads and Go's word on her, her
    // post's result, her presence and her seats.
    expect(agent.ctx.storage.kv.get("room:readers")).not.toHaveProperty(
      "user-ada"
    )
    expect(agent.ctx.storage.kv.list({ prefix: "room:result:" })).toEqual([])
    expect(agent.stored?.here).toEqual(["user-member"])
    expect(
      agent.stored?.seats.filter(
        (seat) => seat.user_id === "user-ada" || seat.speaks_for === "user-ada"
      )
    ).toEqual([])

    // The page opened since hears the room once Go seats her again, and the
    // same removal again does nothing.
    goRoom.held.push(said(3, "juno"))
    await run(agent, roomTiming.slowMs)
    expect(roomMessages(member)).toEqual([1, 2, 3])
    expect(roomMessages(back).at(-1)).toBe(3)
    const vouched = agent.ctx.storage.kv.get("room:readers")
    expect(vouched).toHaveProperty(["user-ada", "confirmed"])
    await agent.purgeMember("user-ada", removedAt)
    expect(back.closed).toBeUndefined()
    expect(agent.ctx.storage.kv.get("room:readers")).toEqual(vouched)
  })

  it("counts for nothing what Go said of a member as of before their purge reached the room", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    await join(agent, "member")
    const removedAt = new Date(Date.now()).toISOString()
    later(1_000)
    // Let back in since, her page opens, and Go is asked about her just as
    // the purge of her removal arrives.
    const release = goRoom.hold("GET", room, "ada")
    const sock = socket("socket-back")
    agent.sockets.push(sock)
    const connecting = agent.onConnect(
      sock as unknown as Connection,
      upgrade("ada")
    )
    await vi.waitFor(() => expect(goRoom.askedAbout("ada")).toBe(1))
    await agent.purgeMember("user-ada", removedAt)
    release()
    await connecting

    expect(sock.closed).toBeUndefined()
    expect(agent.ctx.storage.kv.get("room:readers")).not.toHaveProperty(
      "user-ada"
    )
    expect(sock.sent).toEqual([])
    // Asked afresh, Go says she is on the team: she is sent the room.
    later(1_000)
    await see(agent, sock, false)
    expect(goRoom.askedAbout("ada")).toBe(2)
    expect(roomMessages(sock)).toEqual([1])
  })

  it("closes a socket from before the removal that learns whose it is only after the purge, having sent it nothing", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    await join(agent, "member")
    const release = goRoom.hold("GET", "/v1/auth/me", "ada")
    const late = socket("socket-late")
    agent.sockets.push(late)
    const connecting = agent.onConnect(
      late as unknown as Connection,
      upgrade("ada")
    )
    await vi.waitFor(() =>
      expect(
        goRoom.calls.some(
          (call) =>
            call.path === "/v1/auth/me" && call.cookie === "oryxa_access=ada"
        )
      ).toBe(true)
    )
    later(1_000)
    await agent.purgeMember("user-ada", new Date(Date.now()).toISOString())
    expect(late.closed).toBeUndefined()

    release()
    await connecting
    expect(late.closed?.code).toBe(roomGoneCloseCode)
    expect(late.sent).toEqual([{ type: "room-status", status: "gone" }])
    expect(goRoom.askedAbout("ada")).toBe(0)
  })

  it("neither answers nor keeps a post of a member who left while it was out, and the team still hears what Go recorded", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada")
    const answer = deferred<Response>()
    goRoom.onPost(() => answer.promise)
    const posting = post(agent, ada, "post-1", "Leaving soon")
    await vi.waitFor(() => expect(goRoom.posts()).toHaveLength(1))
    later(1_000)

    await agent.purgeMember("user-ada", new Date(Date.now()).toISOString())
    answer.resolve(
      json(
        {
          messages: [said(2, "ada", "Leaving soon"), said(3, "juno", "Noted.")],
        },
        201
      )
    )
    await posting

    expect(ada.closed?.code).toBe(roomGoneCloseCode)
    expect(sentOf(ada, "room-posted")).toEqual([])
    expect(agent.ctx.storage.kv.list({ prefix: "room:result:" })).toEqual([])
    expect(roomMessages(member)).toEqual([1, 2, 3])
  })

  it("lets go of everything when the organization or the teamspace is gone: every socket closes, and a read under way keeps nothing", async () => {
    const goRoom = fakeGo([said(1)])
    const agent = makeRoom()
    const member = await join(agent, "member")
    const ada = await join(agent, "ada", false)
    goRoom.held.push(said(2, "juno"))
    later(roomTiming.returnMs)
    const release = goRoom.hold("GET", `${room}/messages`, "member")
    const seeing = see(agent, member, true)
    await vi.waitFor(() => expect(goRoom.readsAs("member")).toBe(3))

    await agent.purgeRoom()
    release()
    await seeing

    for (const sock of [member, ada]) {
      expect(sentOf(sock, "room-status").at(-1)).toEqual({
        type: "room-status",
        status: "gone",
      })
      expect(sock.closed?.code).toBe(roomGoneCloseCode)
    }
    expect(agent.ctx.storage.kv.list({ prefix: "room:" })).toEqual([])
    expect(agent.stored).toEqual({ seats: [], here: [] })
    expect(agent.jobs).toEqual([])
    // Again, it finds nothing.
    await agent.purgeRoom()
    expect(agent.ctx.storage.kv.list({ prefix: "room:" })).toEqual([])
  })

  it("refuses what is not a member's purge of it", async () => {
    fakeGo([said(1)])
    const agent = makeRoom()
    for (const [userId, removedAt] of [
      ["", "2026-09-24T09:00:00.000Z"],
      ["user~ada", "2026-09-24T09:00:00.000Z"],
      ["user-ada", "2026-09-24T09:00:00Z"],
      ["user-ada", "yesterday"],
    ]) {
      await expect(agent.purgeMember(userId, removedAt)).rejects.toThrow(
        TypeError
      )
    }
  })

  it("tries its registration again when the organization's registry did not answer", async () => {
    fakeGo([said(1)])
    const registry = fakeRegistry()
    registry.answers(false)
    const agent = makeRoom(registry)
    vi.spyOn(console, "error").mockImplementation(() => {})
    await join(agent, "member", null)
    expect(registry.registered).toEqual([])
    const retry = agent.jobs.find((job) => job.callback === "retryRegistration")
    expect(retry && retry.time * 1000 - Date.now()).toBe(30_000)

    registry.answers(true)
    later(30_000)
    await fire(agent)
    expect(registry.registered).toEqual([
      { registry: "org_sim", teamspaceId: "tsp_launch" },
    ])
    expect(agent.jobs).toEqual([])
  })
})
