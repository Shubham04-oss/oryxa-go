import { Agent } from "agents"
import type { Connection, ConnectionContext, Schedule, WSMessage } from "agents"
import { ApiError, requestError } from "./lib/api/client"
import { organizationPath } from "./lib/agents/http"
import { isUnavailableHere } from "./lib/agents/policy"
import { roomApi } from "./lib/rooms/client"
import type { RoomMessage, RoomRequest, RoomSeat } from "./lib/rooms/client"
import {
  ROOM_WINDOW,
  emptyRoomState,
  hasLanded,
  parseRoomClientMessage,
  roomGoneCloseCode,
  teamspaceRoomAgentName,
} from "./lib/rooms/room-agent"
import type {
  RoomPostResult,
  RoomServerMessage,
  TeamspaceRoomState,
} from "./lib/rooms/room-agent"
import { sessionExpiredCode } from "./lib/threads/chat-messages"
import type { ThreadChatEnv } from "./thread-chat-agent"

/**
 * The headers the Worker sets on every request it routes to the room,
 * replacing any `X-Oryxa-*` header the browser sent, once Go has said the
 * member is on the teamspace's team.
 */
export const roomRequestHeaders = {
  cookie: "X-Oryxa-Cookie",
  organization: "X-Oryxa-Organization",
  teamspace: "X-Oryxa-Teamspace",
} as const

/**
 * What a socket keeps: its upgrade request's cookie, ids and origin, and what
 * the room has learned about it since. It lives in the socket's attachment,
 * so hibernation keeps it. A socket never learns a newer cookie; the page
 * reopens it after refreshing the session.
 */
export type RoomConnectionState = {
  cookie: string
  organizationId: string
  teamspaceId: string
  /** The page's origin: Go takes a cookie-authenticated post only from an allowed one. */
  origin: string
  /** Who the cookie is, once `GET /v1/auth/me` has said. */
  userId?: string
  /** The page says its tab is visible. False until the page says anything. */
  visible: boolean
  /** Go answered 401 for this cookie, so it is never used again. */
  expired?: boolean
  /** The socket has been sent the room; until then it is sent nothing else of it. */
  ready?: boolean
}

/**
 * How often the room reads Go, and how long it keeps what it keeps. A read
 * runs on the second at or just before it is due (`toleranceMs`), so the
 * gaps come out a little under these.
 */
export const roomTiming = {
  /** While a post is out and Go has not recorded it yet. */
  landingMs: 1_000,
  /** While a post is out, and for `afterPostMs` after the last one ends. */
  fastMs: 2_500,
  afterPostMs: 120_000,
  /** Otherwise, while anybody has the room open in a visible tab. */
  slowMs: 12_000,
  /** Every visible member is read at least this often: Go counts a reader present for 90 s. */
  heartbeatMs: 30_000,
  /** A member whose read failed is tried again after this. */
  retryMs: 5_000,
  /** A member coming back to the room is read at once, unless read this recently. */
  returnMs: 2_000,
  /** A room nobody has had open for this long lets go of its copy of the messages. */
  forgetMs: 10 * 60_000,
  /** The longest a post may take before the room stops waiting for it. */
  postTimeoutMs: 5 * 60_000,
  /** How long a finished post's result is kept for a socket that reopens. */
  resultMs: 2 * 60_000,
  /** A tick this far overdue never ran, and gives way to a new one. */
  staleTickMs: 15_000,
  /**
   * The SDK's schedule keeps whole seconds, rounded down, so a tick runs up
   * to this much early: a read due that soon is read.
   */
  toleranceMs: 1_000,
  /** The closest two ticks are ever set, whatever is due. */
  minTickGapMs: 1_000,
}

/** The most posts one member may have out at once. */
const maxPostsPerMember = 5

// The largest frame the object sends: a long room's messages go in several.
const maxFrameLength = 256 * 1024

// Keys in the object's storage (SQLite-backed KV). Each message has its own
// key, by seq, so the room lists in order.
const keys = {
  message: "room:message:",
  result: "room:result:",
  readThrough: "room:read-through",
  lastRead: "room:last-read",
  readers: "room:readers",
  fastUntil: "room:fast-until",
  condition: "room:condition",
  trouble: "room:trouble",
  tick: "room:tick",
  forget: "room:forget",
} as const

/** When a member was last read: `tried` when a read went out, `read` when Go answered one. */
type Reader = { read?: number; tried?: number }

/** Every member's reads, by member. */
type Readers = Partial<Record<string, Reader>>

/** Why nothing is read: Go refuses everyone until the teamspace changes. */
type Condition = "paused" | "unavailable"

/** A finished post, kept for a socket of its author's that reopens. */
type KeptResult = {
  member: string
  connectionId: string
  at: number
  result: RoomPostResult
}

/** A post on its way to Go. */
type OutgoingPost = {
  /** Whose it is: the user id, or the socket until Go says who that is. */
  member: string
  connectionId: string
  userId?: string
  body: string
  /** The last seq the room held when it went out. */
  after: number
  /** A read has shown the room recorded it. */
  landed: boolean
}

/** The room's durable part: its copy of the messages, and how it reads. */
class RoomStore {
  constructor(private readonly kv: SyncKvStorage) {}

  /** The last seq read through: nothing until the first read, and again once forgotten. */
  get readThrough(): number | undefined {
    return this.kv.get<number>(keys.readThrough)
  }

  set readThrough(seq: number | undefined) {
    this.put(keys.readThrough, seq)
  }

  /** The messages held, oldest first. */
  messages(): RoomMessage[] {
    return [...this.kv.list<RoomMessage>({ prefix: keys.message })].map(
      ([, message]) => message
    )
  }

  /** The highest seq held or read through. */
  lastSeq(): number {
    const readThrough = this.readThrough ?? 0
    for (const [, last] of this.kv.list<RoomMessage>({
      prefix: keys.message,
      reverse: true,
      limit: 1,
    })) {
      return Math.max(last.seq, readThrough)
    }
    return readThrough
  }

  /**
   * Keeps what it did not hold and the newest `ROOM_WINDOW`, and returns
   * what was new, in order. Go never changes a message it recorded, so a
   * seq held is a message held.
   */
  add(messages: readonly RoomMessage[]): RoomMessage[] {
    const fresh: RoomMessage[] = []
    const ordered = [...messages].sort((left, right) => left.seq - right.seq)
    for (const message of ordered) {
      const key = messageKey(message.seq)
      if (this.kv.get(key) !== undefined) continue
      this.kv.put(key, message)
      fresh.push(message)
    }
    if (fresh.length > 0) {
      const held = [...this.kv.list({ prefix: keys.message })]
      for (const [key] of held.slice(
        0,
        Math.max(0, held.length - ROOM_WINDOW)
      )) {
        this.kv.delete(key)
      }
    }
    return fresh
  }

  /** When any member was last read, successfully or not. */
  get lastRead(): number {
    return this.kv.get<number>(keys.lastRead) ?? 0
  }

  get readers(): Readers {
    return this.kv.get<Readers>(keys.readers) ?? {}
  }

  /** A read as `member` goes out. */
  tried(member: string, at: number): void {
    const readers = this.readers
    readers[member] = { ...readers[member], tried: at }
    this.kv.put(keys.readers, readers)
    this.kv.put(keys.lastRead, at)
  }

  /** Go answered a read as `member`, which also marked them present. */
  read(member: string, at: number): void {
    const readers = this.readers
    readers[member] = { ...readers[member], read: at }
    this.kv.put(keys.readers, readers)
  }

  /** Forgets the members no socket is for any more. */
  keepReaders(members: ReadonlySet<string>): void {
    const readers = this.readers
    const kept = Object.fromEntries(
      Object.entries(readers).filter(([member]) => members.has(member))
    )
    if (Object.keys(kept).length !== Object.keys(readers).length) {
      this.kv.put(keys.readers, kept)
    }
  }

  /** Until when the room reads fast after a post. */
  get fastUntil(): number {
    return this.kv.get<number>(keys.fastUntil) ?? 0
  }

  set fastUntil(at: number) {
    this.kv.put(keys.fastUntil, at)
  }

  get condition(): Condition | undefined {
    return this.kv.get<Condition>(keys.condition)
  }

  set condition(condition: Condition | undefined) {
    this.put(keys.condition, condition)
  }

  /** Go could not be read lately. */
  get trouble(): boolean {
    return this.kv.get<boolean>(keys.trouble) ?? false
  }

  set trouble(trouble: boolean) {
    this.put(keys.trouble, trouble || undefined)
  }

  /** The one tick set, and when it is due. */
  get tick(): { id: string; due: number } | undefined {
    return this.kv.get(keys.tick)
  }

  set tick(tick: { id: string; due: number } | undefined) {
    this.put(keys.tick, tick)
  }

  /** The schedule that lets go of an unused room's copy. */
  get forget(): string | undefined {
    return this.kv.get<string>(keys.forget)
  }

  set forget(id: string | undefined) {
    this.put(keys.forget, id)
  }

  result(id: string): KeptResult | undefined {
    const kept = this.kv.get<KeptResult>(keys.result + id)
    return kept && kept.at + roomTiming.resultMs > Date.now() ? kept : undefined
  }

  /** Keeps a finished post's result a while, and drops those kept too long. */
  keepResult(id: string, kept: KeptResult): void {
    for (const [key, old] of this.kv.list<KeptResult>({
      prefix: keys.result,
    })) {
      if (old.at + roomTiming.resultMs <= kept.at) this.kv.delete(key)
    }
    this.kv.put(keys.result + id, kept)
  }

  /** The results kept for a member's posts, or for one socket's. */
  resultsFor(member: string, connectionId: string): RoomPostResult[] {
    const now = Date.now()
    return [...this.kv.list<KeptResult>({ prefix: keys.result })]
      .map(([, kept]) => kept)
      .filter(
        (kept) =>
          kept.at + roomTiming.resultMs > now &&
          (kept.member === member || kept.connectionId === connectionId)
      )
      .map((kept) => kept.result)
  }

  /** Lets go of everything the room holds. */
  clear(): void {
    for (const [key] of this.kv.list({ prefix: "room:" })) this.kv.delete(key)
  }

  private put(key: string, value: unknown): void {
    if (value === undefined) this.kv.delete(key)
    else this.kv.put(key, value)
  }
}

function messageKey(seq: number): string {
  return `${keys.message}${String(seq).padStart(16, "0")}`
}

function stateOf(connection: Connection): RoomConnectionState | undefined {
  const state = connection.state as RoomConnectionState | null
  return state && typeof state.cookie === "string" ? state : undefined
}

/** Changes a socket's state; nothing when it has none or has closed meanwhile. */
function update(
  connection: Connection,
  change: Partial<RoomConnectionState>
): RoomConnectionState | undefined {
  const state = stateOf(connection)
  if (!state) return undefined
  const next = { ...state, ...change }
  try {
    connection.setState(next)
  } catch {
    return undefined
  }
  return next
}

/** Whom a socket reads and posts as: its user id, or itself until Go says who that is. */
function memberOf(state: RoomConnectionState, connectionId: string): string {
  return state.userId ?? `socket:${connectionId}`
}

function send(connection: Connection, message: RoomServerMessage): void {
  try {
    connection.send(JSON.stringify(message))
  } catch {
    // It closed meanwhile.
  }
}

/** Messages in frames of at most about `maxFrameLength` characters. */
function framed(messages: readonly RoomMessage[]): RoomMessage[][] {
  const frames: RoomMessage[][] = []
  let frame: RoomMessage[] = []
  let length = 0
  for (const message of messages) {
    const size = JSON.stringify(message).length
    if (frame.length > 0 && length + size > maxFrameLength) {
      frames.push(frame)
      frame = []
      length = 0
    }
    frame.push(message)
    length += size
  }
  if (frame.length > 0) frames.push(frame)
  return frames
}

/** When `reader` is next due a read: every `heartbeatMs`, and `retryMs` after one that went out. */
function heartbeatAt(reader: Reader | undefined): number {
  if (!reader) return 0
  return Math.max(
    (reader.read ?? 0) + roomTiming.heartbeatMs,
    (reader.tried ?? 0) + roomTiming.retryMs
  )
}

function sameJson(left: unknown, right: unknown): boolean {
  return JSON.stringify(left) === JSON.stringify(right)
}

/** A post Go refused or never answered, as its author's page is told. */
function failedPost(id: string, error: unknown): RoomPostResult {
  if (error instanceof ApiError) {
    if (error.status === 401) {
      return {
        id,
        ok: false,
        status: 401,
        code: sessionExpiredCode,
        message: "Your session has ended.",
      }
    }
    return {
      id,
      ok: false,
      status: error.status,
      ...(error.code ? { code: error.code } : {}),
      message: error.message,
    }
  }
  return { id, ok: false, status: 0, message: "The room could not be reached." }
}

/**
 * A teamspace's room, shared live by everyone on its team who has it open:
 * one object per teamspace, named `teamspaceRoomAgentName(org, teamspace)`.
 * Go keeps the room (who may read it, what was said, who is present); this
 * object is the live layer over it, and never decides any of that itself.
 *
 * - **Reading.** It reads Go (`…/messages?after=`) as a member, with that
 *   member's own cookie, keeps the recent window (`ROOM_WINDOW`) and the
 *   highest seq it read through, and sends every socket each new message
 *   once, by seq. A socket that opens is sent the window once the room has
 *   been read.
 * - **Presence.** Go counts a reader present for 90 seconds, and a present
 *   member's agent does not answer over them. So the object reads as each
 *   member with a visible tab, at least every `heartbeatMs`, and never as a
 *   member whose tabs are all hidden or closed: the page says which
 *   (`room-visibility`). Who has the room open and visible right now is in
 *   the synced state (`here`), beside Go's seats.
 * - **Posting.** A post goes to Go as the socket's member, whatever the page
 *   says. Go answers only once the room's agents have, so while it is out
 *   the object reads every `landingMs` until the member's message is
 *   recorded and every `fastMs` after, and each reply reaches every socket as
 *   Go records it; the answer is merged by seq and sent to the author's
 *   sockets.
 * - **Scheduling.** Reads are woken by the SDK's schedule (an alarm), never
 *   a timer, so the object hibernates between them: fast during and for
 *   `afterPostMs` after a post, `slowMs` otherwise, and not at all once no
 *   tab is visible.
 * - **Refusals**, per member: a 401 tells that cookie's sockets
 *   `session_expired` and the cookie is not used again; a 403 or 404 closes
 *   the member's sockets as gone, and once nobody here may open the room its
 *   copy goes; a paused room (400) or a server without rooms (503) tells
 *   every socket and stops reading until a member's read succeeds again.
 *
 * Only the teamspace's team reaches it: the Worker routes a socket only once
 * Go answers `GET …/teamspaces/{id}` with `is_member` for the member's cookie.
 */
export class TeamspaceRoomAgent extends Agent<
  ThreadChatEnv,
  TeamspaceRoomState
> {
  override initialState: TeamspaceRoomState = emptyRoomState

  /** Posts on their way to Go, by the page's id. In memory: a restart loses the POST with them. */
  private readonly posts = new Map<string, OutgoingPost>()
  /** The members being read right now: one member is never read twice at once. */
  private readonly reading = new Set<string>()
  /** `GET /v1/auth/me` for a socket, while it is out. */
  private readonly identifying = new Map<string, Promise<string | undefined>>()
  /** When the last tick ran, in this instance. */
  private lastTick = 0

  private get store(): RoomStore {
    return new RoomStore(this.ctx.storage.kv)
  }

  // After hibernation or a restart: carry on reading for whoever is here.
  override async onStart(): Promise<void> {
    await this.planTick()
  }

  // The Worker's check for a page whose socket was refused
  // (checkTeamspaceRoom). It only reaches here once Go has let the member in.
  override onRequest(request: Request): Response {
    if (request.method !== "GET") {
      return new Response(null, { status: 405, headers: { Allow: "GET" } })
    }
    return new Response(null, { status: 204 })
  }

  // Seats and presence are Go's and the object's to say, never a page's.
  override validateStateChange(
    _nextState: TeamspaceRoomState,
    source: Connection | "server"
  ): void {
    if (source !== "server") {
      throw new Error("Only the room changes its state.")
    }
  }

  override async onConnect(connection: Connection, context: ConnectionContext) {
    const { request } = context
    const cookie = request.headers.get(roomRequestHeaders.cookie)
    const organizationId = request.headers.get(roomRequestHeaders.organization)
    const teamspaceId = request.headers.get(roomRequestHeaders.teamspace)
    if (
      !cookie ||
      !organizationId ||
      !teamspaceId ||
      teamspaceRoomAgentName(organizationId, teamspaceId) !== this.name
    ) {
      connection.close(1008, "Unauthorized")
      return
    }
    connection.setState({
      cookie,
      organizationId,
      teamspaceId,
      origin: request.headers.get("Origin") ?? "",
      visible: false,
    } satisfies RoomConnectionState)
    await this.cancelForget()
    // Who the cookie is, for presence and a post's answer. The room is not
    // read yet: reading marks the member present, so the page says first
    // whether anybody is looking.
    await this.identify(connection)
  }

  override async onMessage(connection: Connection, message: WSMessage) {
    const said = parseRoomClientMessage(message)
    if (!said || !stateOf(connection)) return
    if (said.type === "room-visibility") {
      await this.see(connection, said.visible)
    } else {
      await this.post(connection, said.id, said.body)
    }
  }

  override async onClose(connection: Connection) {
    this.identifying.delete(connection.id)
    this.publishPresence(connection.id)
    if (this.connections(connection.id).length === 0) {
      await this.scheduleForget()
    }
  }

  /**
   * One round of reads, woken by its schedule: as each visible member whose
   * presence is due, else, when the room is due a read, as the one read
   * longest ago. Then the next round is set.
   */
  async tick(_payload?: unknown, schedule?: Schedule<unknown>): Promise<void> {
    const store = this.store
    // The SDK drops a tick once it has run; any other set stands.
    if (schedule && store.tick?.id === schedule.id) store.tick = undefined
    this.lastTick = Date.now()
    if (!store.condition) {
      const members = this.visibleMembers()
      // A tick may run a little early: whatever is due by then is read.
      const by = Date.now() + roomTiming.toleranceMs
      const readers = store.readers
      const due = [...members].filter(
        ([member]) => heartbeatAt(readers[member]) <= by
      )
      if (due.length === 0 && members.size > 0) {
        if (store.lastRead + this.cadence(Date.now()) <= by) {
          const [oldest] = [...members].sort(
            ([left], [right]) =>
              (readers[left]?.tried ?? 0) - (readers[right]?.tried ?? 0)
          )
          due.push(oldest)
        }
      }
      for (const [, connection] of due) await this.readAs(connection)
    }
    await this.planTick()
  }

  /** Lets go of the room's copy once nobody has had it open for `forgetMs`: Go keeps the room, and the next socket reads it afresh. */
  async forgetIfIdle(): Promise<void> {
    this.store.forget = undefined
    if (this.connections().length > 0) return
    await this.forget()
  }

  /** A page's tab became visible or hidden (and, first, which it is). */
  private async see(connection: Connection, visible: boolean): Promise<void> {
    const state = update(connection, { visible })
    if (!state) return
    this.publishPresence()
    if (visible) {
      const reader = this.store.readers[memberOf(state, connection.id)]
      // Whoever comes back to the room is read at once: that is what tells
      // Go they are here, and their agent to keep quiet.
      if (
        !state.ready ||
        (reader?.read ?? 0) + roomTiming.returnMs <= Date.now()
      ) {
        await this.readAs(connection)
      }
    } else if (!state.ready) {
      // A tab nobody is looking at is not read for: it is sent what the room
      // already holds, or waits for somebody who is looking.
      await this.sendSnapshot(connection)
    }
    await this.planTick()
  }

  /**
   * Reads what the room added as the socket's member, which also marks them
   * present, and sends it on: new messages to every socket, the seats in the
   * synced state, and the room to any socket still waiting for it.
   */
  private async readAs(connection: Connection): Promise<void> {
    await this.identify(connection)
    const state = stateOf(connection)
    if (!state || state.expired) return
    const member = memberOf(state, connection.id)
    if (this.reading.has(member)) return
    this.reading.add(member)
    const store = this.store
    store.tried(member, Date.now())
    try {
      const read = await this.api(state).readAfter(
        state.organizationId,
        state.teamspaceId,
        store.readThrough
      )
      store.readThrough = Math.max(read.readThrough, store.readThrough ?? 0)
      store.read(member, Date.now())
      this.arrived(read.messages)
      this.setSeats(read.seats)
      this.recovered()
      this.noteLanded()
      await this.sendSnapshots()
    } catch (error) {
      await this.refused(connection, state, error)
    } finally {
      this.reading.delete(member)
    }
  }

  /** Stores what it did not hold and sends it, once, to every socket that has the room. */
  private arrived(messages: readonly RoomMessage[]): void {
    const store = this.store
    // Until the room has been read it holds nothing: the first read brings
    // its latest page, these included.
    if (store.readThrough === undefined) return
    const fresh = store.add(messages)
    if (fresh.length === 0) return
    for (const connection of this.connections()) {
      if (!stateOf(connection)?.ready) continue
      for (const frame of framed(fresh)) {
        send(connection, { type: "room-messages", messages: frame })
      }
    }
  }

  private async sendSnapshots(): Promise<void> {
    for (const connection of this.connections()) {
      if (!stateOf(connection)?.ready) await this.sendSnapshot(connection)
    }
  }

  /**
   * Sends a socket the room, once there is one to send: after a read, and
   * never while Go refuses the room. With it go this member's posts still
   * out and those finished lately, so a page that reopened its socket learns
   * what became of what it said.
   */
  private async sendSnapshot(connection: Connection): Promise<void> {
    const store = this.store
    if (store.condition) {
      send(connection, { type: "room-status", status: store.condition })
      return
    }
    if (store.readThrough === undefined) return
    await this.identify(connection)
    const state = stateOf(connection)
    if (!state || state.ready) return
    const member = memberOf(state, connection.id)
    const pending = [...this.posts]
      .filter(
        ([, post]) =>
          post.member === member || post.connectionId === connection.id
      )
      .map(([id]) => id)
    const [first = [], ...rest] = framed(store.messages())
    send(connection, {
      type: "room-snapshot",
      messages: first,
      pending,
      results: store.resultsFor(member, connection.id),
    })
    for (const frame of rest) {
      send(connection, { type: "room-messages", messages: frame })
    }
    if (store.trouble)
      send(connection, { type: "room-status", status: "error" })
    update(connection, { ready: true })
  }

  /** Says `body` in the room as the socket's member, and tells their sockets what became of it. */
  private async post(
    connection: Connection,
    id: string,
    body: string
  ): Promise<void> {
    const state = stateOf(connection)
    if (!state) return
    const member = memberOf(state, connection.id)
    // A page that reopened its socket sends a post again when it never
    // heard what became of it: one still out is not said twice, and one
    // finished lately is answered with what became of it.
    if (this.posts.has(id)) return
    const kept = this.store.result(id)
    if (kept) {
      if (kept.member === member || kept.connectionId === connection.id) {
        send(connection, { type: "room-posted", result: kept.result })
      }
      return
    }
    // Taken before anything is awaited, so a snapshot sent to this socket
    // always knows a post that reached it.
    const outgoing: OutgoingPost = {
      member,
      connectionId: connection.id,
      userId: state.userId,
      body: body.trim(),
      after: this.store.lastSeq(),
      landed: false,
    }
    const out = [...this.posts.values()].filter(
      (post) => post.member === outgoing.member
    ).length
    this.posts.set(id, outgoing)

    let result: RoomPostResult
    try {
      if (out >= maxPostsPerMember) {
        throw new ApiError("Too many messages at once.", 429, "rate_limited")
      }
      const userId = await this.identify(connection)
      if (userId) {
        outgoing.userId = userId
        outgoing.member = userId
      }
      if (stateOf(connection)?.expired) {
        throw new ApiError("Your session has ended.", 401)
      }
      // Reads at the landing pace start now.
      await this.planTick()
      const said = await this.keepAliveWhile(() =>
        this.api(state).say(state.organizationId, state.teamspaceId, body, {
          signal: AbortSignal.timeout(roomTiming.postTimeoutMs),
        })
      )
      this.arrived(said)
      result = { id, ok: true, messages: said }
    } catch (error) {
      result = failedPost(id, error)
      if (error instanceof ApiError && error.status === 401) {
        this.expire(state.cookie)
      }
    } finally {
      this.posts.delete(id)
      this.store.fastUntil = Date.now() + roomTiming.afterPostMs
    }
    if (!result.ok && result.status !== 401 && result.status !== 429) {
      // What the room recorded reaches every page before the author hears
      // the post failed, so their page can tell a post the room kept from
      // one it did not. The read also finds a room that was paused.
      await this.readForLanding(connection)
    }
    this.store.keepResult(id, {
      member: outgoing.member,
      connectionId: outgoing.connectionId,
      at: Date.now(),
      result,
    })
    this.answer(outgoing, result)
    if (!result.ok && result.status === 404) {
      await this.turnAway(outgoing.member, result.message)
    }
    await this.planTick()
  }

  /** Sends a post's result to every socket of its author. */
  private answer(post: OutgoingPost, result: RoomPostResult): void {
    for (const connection of this.connections()) {
      const state = stateOf(connection)
      if (!state) continue
      if (
        connection.id === post.connectionId ||
        (post.userId !== undefined && state.userId === post.userId)
      ) {
        send(connection, { type: "room-posted", result })
      }
    }
  }

  /** Reads as a visible member, the author first, so what a failed post left in the room is seen. */
  private async readForLanding(author: Connection): Promise<void> {
    const state = stateOf(author)
    const reader =
      state?.visible && !state.expired
        ? author
        : this.visibleMembers().values().next().value
    if (reader) await this.readAs(reader)
  }

  /** Marks the posts out whose message a read has shown. */
  private noteLanded(): void {
    const waiting = [...this.posts.values()].filter((post) => !post.landed)
    if (waiting.length === 0) return
    const messages = this.store.messages()
    for (const post of waiting) {
      if (hasLanded(messages, post, post.userId)) post.landed = true
    }
  }

  /** What Go's refusal of a member's read means for the room. */
  private async refused(
    connection: Connection,
    state: RoomConnectionState,
    error: unknown
  ): Promise<void> {
    if (!(error instanceof ApiError)) {
      console.error("teamspace room: Go could not be read", error)
      this.troubled()
      return
    }
    if (error.status === 401) {
      this.expire(state.cookie)
    } else if (error.status === 403 || error.status === 404) {
      await this.turnAway(memberOf(state, connection.id), error.message)
    } else if (error.status === 400) {
      // Go refuses a paused room's reads to everyone.
      this.stop("paused", error.message)
    } else if (isUnavailableHere(error)) {
      this.stop("unavailable", error.message)
    } else {
      this.troubled()
    }
  }

  /** Go no longer takes `cookie`: its sockets are told, and it is never used again. */
  private expire(cookie: string): void {
    for (const connection of this.connections()) {
      const state = stateOf(connection)
      if (!state || state.cookie !== cookie || state.expired) continue
      update(connection, { expired: true })
      send(connection, { type: "room-status", status: "session_expired" })
    }
    this.publishPresence()
  }

  /**
   * Go no longer lets `member` in (403 or 404): their sockets are told and
   * closed. Go answers 404 to everyone once the teamspace is deleted, so the
   * other members here are asked too, for the teamspace itself (which marks
   * nobody present); once none of them may open it, the room's copy goes.
   */
  private async turnAway(member: string, message?: string): Promise<void> {
    this.close(member, message)
    const asked = new Set<string>()
    for (const connection of this.connections()) {
      const state = stateOf(connection)
      if (!state || state.expired) continue
      const other = memberOf(state, connection.id)
      if (asked.has(other)) continue
      asked.add(other)
      try {
        const space = await this.requestAs(state)<{ is_member?: unknown }>(
          organizationPath(
            state.organizationId,
            "teamspaces",
            state.teamspaceId
          )
        )
        if (space.is_member === true) return
        this.close(other)
      } catch (error) {
        if (
          !(error instanceof ApiError) ||
          (error.status !== 403 && error.status !== 404)
        ) {
          // Nothing learned: the copy stays with the room.
          return
        }
        this.close(other, error.message)
      }
    }
    await this.forget()
  }

  /** Tells a member's sockets they may no longer open the room, and closes them. */
  private close(member: string, message?: string): void {
    for (const connection of this.connections()) {
      const state = stateOf(connection)
      if (!state || memberOf(state, connection.id) !== member) continue
      send(connection, {
        type: "room-status",
        status: "gone",
        ...(message ? { message } : {}),
      })
      try {
        connection.close(roomGoneCloseCode, "Gone")
      } catch {
        // Already closed.
      }
    }
    this.publishPresence()
  }

  /** The room is paused, or not run on this server: every socket is told, and nothing is read until a member's read succeeds again. */
  private stop(condition: Condition, message: string): void {
    const store = this.store
    store.condition = condition
    store.trouble = false
    for (const connection of this.connections()) {
      send(connection, { type: "room-status", status: condition, message })
    }
  }

  /** Go failed to answer; the room keeps trying on its schedule. */
  private troubled(): void {
    const store = this.store
    const told = store.trouble
    store.trouble = true
    for (const connection of this.connections()) {
      if (!told || !stateOf(connection)?.ready) {
        send(connection, { type: "room-status", status: "error" })
      }
    }
  }

  /** A read succeeded: whatever had stopped the room is over. */
  private recovered(): void {
    const store = this.store
    if (!store.condition && !store.trouble) return
    store.condition = undefined
    store.trouble = false
    for (const connection of this.connections()) {
      if (stateOf(connection)?.ready) {
        send(connection, { type: "room-status", status: "live" })
      }
    }
  }

  private setSeats(seats: RoomSeat[]): void {
    if (sameJson(this.state.seats, seats)) return
    this.setState({ ...this.state, seats })
  }

  /** Publishes who has the room open in a visible tab, leaving out a socket that is closing. */
  private publishPresence(without?: string): void {
    const here = new Set<string>()
    for (const connection of this.connections(without)) {
      const state = stateOf(connection)
      if (state?.visible && !state.expired && state.userId) {
        here.add(state.userId)
      }
    }
    const next = [...here].sort()
    if (sameJson(this.state.here, next)) return
    this.setState({ ...this.state, here: next })
  }

  /**
   * One socket per member with the room open in a visible tab and a cookie
   * Go still takes: whom the room reads as.
   */
  private visibleMembers(): Map<string, Connection> {
    const members = new Map<string, Connection>()
    for (const connection of this.connections()) {
      const state = stateOf(connection)
      if (!state?.visible || state.expired) continue
      const member = memberOf(state, connection.id)
      if (!members.has(member)) members.set(member, connection)
    }
    return members
  }

  /** How soon the room is due its next read, besides each member's presence. */
  private cadence(now: number): number {
    const posts = [...this.posts.values()]
    if (posts.some((post) => !post.landed)) return roomTiming.landingMs
    if (posts.length > 0 || now < this.store.fastUntil) {
      return roomTiming.fastMs
    }
    return roomTiming.slowMs
  }

  /**
   * Sets the next round of reads for whoever is looking. Nothing is set
   * once no tab is visible, or while Go refuses the room: a tick already set
   * fires once, finds nobody to read as, and stops.
   */
  private async planTick(): Promise<void> {
    const store = this.store
    const members = this.visibleMembers()
    store.keepReaders(
      new Set(
        this.connections().flatMap((connection) => {
          const state = stateOf(connection)
          return state ? [memberOf(state, connection.id)] : []
        })
      )
    )
    if (store.condition || members.size === 0) return
    const now = Date.now()
    const readers = store.readers
    let next = store.lastRead + this.cadence(now)
    for (const member of members.keys()) {
      next = Math.min(next, heartbeatAt(readers[member]))
    }
    // Never sooner than a moment after the last tick: a room can be busy,
    // but its alarm never spins.
    await this.tickBy(
      Math.max(next, now, this.lastTick + roomTiming.minTickGapMs)
    )
  }

  /** Makes sure a tick comes by `due`: one set earlier stands, one set later gives way. */
  private async tickBy(due: number): Promise<void> {
    const store = this.store
    const set = store.tick
    if (
      set &&
      set.due <= due &&
      set.due > Date.now() - roomTiming.staleTickMs
    ) {
      return
    }
    if (set) await this.cancelSchedule(set.id)
    const schedule = await this.schedule(new Date(due), "tick")
    store.tick = { id: schedule.id, due: schedule.time * 1000 }
  }

  private async scheduleForget(): Promise<void> {
    await this.cancelForget()
    const schedule = await this.schedule(
      roomTiming.forgetMs / 1000,
      "forgetIfIdle"
    )
    this.store.forget = schedule.id
  }

  private async cancelForget(): Promise<void> {
    const store = this.store
    const id = store.forget
    if (!id) return
    store.forget = undefined
    await this.cancelSchedule(id)
  }

  /** Lets go of everything the room holds: its messages, how it reads, its seats and presence. Go keeps the room. */
  private async forget(): Promise<void> {
    const store = this.store
    const tick = store.tick
    store.clear()
    if (tick) await this.cancelSchedule(tick.id)
    this.setState(emptyRoomState)
  }

  /** Who a socket's cookie is (`GET /v1/auth/me`), asked once per socket. */
  private identify(connection: Connection): Promise<string | undefined> {
    const state = stateOf(connection)
    if (!state || state.expired) return Promise.resolve(undefined)
    if (state.userId) return Promise.resolve(state.userId)
    let asking = this.identifying.get(connection.id)
    if (!asking) {
      asking = this.requestAs(state)<{ user?: { id?: unknown } }>(
        "/v1/auth/me"
      ).then(
        (session) => {
          this.identifying.delete(connection.id)
          const userId = session.user?.id
          if (typeof userId !== "string" || !userId) return undefined
          update(connection, { userId })
          this.publishPresence()
          return userId
        },
        (error: unknown) => {
          this.identifying.delete(connection.id)
          if (error instanceof ApiError && error.status === 401) {
            this.expire(state.cookie)
          }
          return undefined
        }
      )
      this.identifying.set(connection.id, asking)
    }
    return asking
  }

  private connections(without?: string): Connection[] {
    return [...this.getConnections()].filter(
      (connection) => connection.id !== without
    )
  }

  private api(state: RoomConnectionState) {
    return roomApi(this.requestAs(state))
  }

  /**
   * Go, as the member whose socket this is: their cookie, and for a post
   * their page's origin, since Go takes a cookie-authenticated change only
   * from an allowed one.
   */
  private requestAs(state: RoomConnectionState): RoomRequest {
    const apiOrigin = this.env.ORYXA_API_ORIGIN
    return async <T>(
      path: string,
      options: {
        method?: "GET" | "POST"
        body?: unknown
        signal?: AbortSignal
      } = {}
    ): Promise<T> => {
      const method = options.method ?? "GET"
      const headers: Record<string, string> = { Cookie: state.cookie }
      if (options.body !== undefined) {
        headers["Content-Type"] = "application/json"
      }
      if (method !== "GET" && state.origin) headers.Origin = state.origin
      const response = await fetch(new URL(path, apiOrigin), {
        method,
        headers,
        body:
          options.body === undefined ? undefined : JSON.stringify(options.body),
        signal: options.signal,
      })
      if (!response.ok) throw await requestError(response)
      const answer: T = await response.json()
      return answer
    }
  }
}
