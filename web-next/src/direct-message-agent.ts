import { Agent, getAgentByName } from "agents"
import type { Connection, ConnectionContext, Schedule, WSMessage } from "agents"
import { conversationRequestHeaders } from "./conversation-env"
import type { ConversationEnv } from "./conversation-env"
import type { NotedMessage, RestatedConversation } from "./inbox-agent"
import { inboxName, parseDirectMessageName, peerIn } from "./lib/dms/names"
import type { DirectMessageIds } from "./lib/dms/names"
import {
  DM_HISTORY_LIMIT,
  DM_MAX_BODY_LENGTH,
  DM_PAGE_SIZE,
  DM_TYPING_TTL_MS,
  MAX_UNREAD,
  codePoints,
  conversationGoneCloseCode,
  parseDmClientMessage,
  preview,
} from "./lib/dms/protocol"
import {
  EventOutbox,
  memberAuthor,
  realtimeEventsConfig,
} from "./realtime-events"
import type { SqlValue } from "./realtime-events"
import { isPurgeInstant } from "./realtime-registry-agent"
import type {
  DmRead,
  DmSendFailure,
  DmServerMessage,
  DmWireMessage,
} from "./lib/dms/protocol"

/**
 * What a socket keeps, in its attachment (so hibernation keeps it): who it
 * is, as the Worker determined it, whether its page says the tab is
 * visible, and when it opened (a purge ends the sockets from before a
 * member left, and keeps those opened since).
 */
export type DmConnectionState = {
  userId: string
  /** False until the page says. */
  visible: boolean
  /** When the Worker let it in, as `toISOString` writes it. */
  connectedAt?: string
}

/** A purge for one person in the DM, who left the organization at `removedAt` (a `PurgeInstant`). */
export type DmPurgeScope = { userId: string; removedAt: string }

/**
 * How many messages one person may send: `burst` at once, then one every
 * `refillMs`. Kept in memory, so a restart forgives; a message over the
 * limit is refused (`rate_limited`) and the page may retry it.
 */
export const dmSendLimit = { burst: 10, refillMs: 1_000 }

/** How often the other person is told again that somebody is still typing, so a page's own expiry never lapses mid-sentence. */
export const dmTypingRetellMs = 2_000

/** How long the object waits before telling an inbox again after it failed to: doubling from 2 s to at most 5 minutes. */
export const dmInboxRetry = { firstSeconds: 2, maxSeconds: 300 }

/** The most messages one inbox update carries: an inbox that missed more is told the latest. */
const maxNotedPerUpdate = 200

// The most one snapshot or history page carries, in characters of JSON: a
// page of long messages comes shorter, and says there is more.
const maxPageCharacters = 256 * 1024

type MessageRow = {
  seq: number
  id: string
  author_id: string
  client_id: string
  body: string
  created_at: string
}

/** Somebody typing: until `timer` fires, and when the other person was last told. In memory only. */
type Typing = { timer: ReturnType<typeof setTimeout>; told: number }

const refusals: Record<DmSendFailure, string> = {
  empty: "Write something to send.",
  too_long: `A message holds at most ${DM_MAX_BODY_LENGTH.toLocaleString("en-US")} characters.`,
  rate_limited: "You're sending messages too quickly. Wait a moment.",
  unavailable: "The message couldn't be saved. Try again.",
}

function stateOf(connection: Connection): DmConnectionState | undefined {
  const state = connection.state as DmConnectionState | null
  return state && typeof state.userId === "string" ? state : undefined
}

/** Changes a socket's state; nothing when it has none or closed meanwhile. */
function update(
  connection: Connection,
  change: Partial<DmConnectionState>
): DmConnectionState | undefined {
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

function send(connection: Connection, message: DmServerMessage | string) {
  try {
    connection.send(
      typeof message === "string" ? message : JSON.stringify(message)
    )
  } catch {
    // It closed meanwhile.
  }
}

/** A stored message as `viewer` is sent it: the page's id only on their own. */
function wire(row: MessageRow, viewer: string): DmWireMessage {
  return {
    id: row.id,
    seq: row.seq,
    authorId: row.author_id,
    body: row.body,
    createdAt: row.created_at,
    ...(row.author_id === viewer ? { clientId: row.client_id } : {}),
  }
}

function noted(row: MessageRow): NotedMessage {
  return {
    seq: row.seq,
    authorId: row.author_id,
    body: preview(row.body),
    createdAt: row.created_at,
  }
}

function randomId(): string {
  return crypto.randomUUID().replace(/-/g, "")
}

/**
 * A direct message between two people in an organization: one object per
 * pair, named `directMessageName(org, userA, userB)`, both people's sockets
 * on the same object (every view of the DM, in the main canvas or the right
 * panel, opens its own). It is the whole of the DM: nothing is kept in Go.
 *
 * - **Storage** (its SQLite): the messages (at most `DM_HISTORY_LIMIT`, the
 *   oldest dropped first), how far each person has read, and what each
 *   person's inbox has been told.
 * - **Sending.** A message's author is always the socket's own person, as
 *   the Worker determined it. Its body is trimmed and holds 1 to
 *   `DM_MAX_BODY_LENGTH` code points; each person may send `dmSendLimit`.
 *   It is stored once per author and page id (`clientId`), so a retry
 *   answers with the stored message. It reaches every socket at once, then
 *   both inboxes (`InboxAgent.noteMessage`); an inbox that could not be told
 *   is told again, with what it missed, until it has been.
 * - **Reading.** A person reads through a seq (`dm-read`), or through their
 *   own message by sending it; both sockets and their inbox are told.
 * - **Typing** lasts `DM_TYPING_TTL_MS` after the page last said so, and is
 *   never stored.
 * - **Presence.** A person is online while one of their sockets says its tab
 *   is visible.
 * - **Events.** Each message stored, and each read that goes further, is
 *   recorded for Go in the same transaction (src/realtime-events.ts), and
 *   sent on the `drainEvents` schedule; never what anybody wrote. A
 *   message's author reads through it with no event of its own: the
 *   message's says so.
 *
 * Only its two people reach it: the Worker routes a socket only once Go has
 * said who the member is and that the other person is in the organization.
 */
export class DirectMessageAgent extends Agent<ConversationEnv> {
  /** Sends each person has left: the send limit's buckets. */
  private readonly sendBuckets = new Map<
    string,
    { tokens: number; at: number }
  >()
  private readonly typing = new Map<string, Typing>()
  /** Counts purges in this instance: work that began before one stops. */
  private epoch = 0
  private schemaReady = false
  private syncing: Promise<void> | undefined
  private syncAgain = false
  /** What Go is told of the conversation: messages and reads, no content. */
  private readonly events = new EventOutbox({
    label: "direct message",
    name: () => this.name,
    scope: () => {
      const { organizationId, userA, userB } = this.ids
      return {
        organizationId,
        conversation: { kind: "direct", id: this.name },
        participants: [userA, userB],
      }
    },
    config: () => realtimeEventsConfig(this.env),
    sql: <T>(strings: TemplateStringsArray, ...values: SqlValue[]) =>
      this.sql<T>(strings, ...values),
    transaction: (work) => this.transaction(work),
    schedule: (at) => this.schedule(at, "drainEvents"),
    cancelSchedule: (id) => this.cancelSchedule(id),
  })

  // The socket carries this object's protocol only: no SDK identity, state
  // or MCP frames, and nothing at all before `onConnect` has checked it.
  override shouldSendProtocolMessages(): boolean {
    return false
  }

  // The object has no synced state, and a page may never set one.
  override validateStateChange(
    _nextState: unknown,
    source: Connection | "server"
  ): void {
    if (source !== "server") {
      throw new Error("A direct message has no state a page may set.")
    }
  }

  // The Worker's check for a page whose socket was refused: it only reaches
  // here once Go has let the member in.
  override onRequest(request: Request): Response {
    if (request.method !== "GET") {
      return new Response(null, { status: 405, headers: { Allow: "GET" } })
    }
    return new Response(null, { status: 204 })
  }

  // After a restart: an inbox that was never told is told again, and the
  // events still waiting go.
  override async onStart(): Promise<void> {
    this.schema()
    if (this.syncPending()) await this.armRetry()
    await this.events.plan()
  }

  override async onConnect(connection: Connection, context: ConnectionContext) {
    const { headers } = context.request
    const organizationId = headers.get(conversationRequestHeaders.organization)
    const userId = headers.get(conversationRequestHeaders.user)
    const ids = parseDirectMessageName(this.name)
    if (
      !ids ||
      !userId ||
      organizationId !== ids.organizationId ||
      !peerIn(ids, userId)
    ) {
      connection.close(1008, "Unauthorized")
      return
    }
    connection.setState({
      userId,
      visible: false,
      connectedAt: new Date().toISOString(),
    } satisfies DmConnectionState)
    this.schema()
    send(connection, this.snapshot(userId))
    // The conversation is its people's from the moment it opens, message or
    // not: both inboxes learn it exists, so purging either person finds it.
    if (this.meta("opened") !== "1") this.setMeta("opened", "1")
    if (this.syncPending()) await this.syncInboxes()
  }

  override async onMessage(connection: Connection, message: WSMessage) {
    const said = parseDmClientMessage(message)
    const state = stateOf(connection)
    if (!said || !state) return
    this.schema()
    switch (said.type) {
      case "dm-visibility":
        this.see(connection, state, said.visible)
        return
      case "dm-send":
        await this.post(connection, state, said.clientId, said.body)
        return
      case "dm-history": {
        const page = this.page(said.beforeSeq, said.limit)
        send(connection, {
          type: "dm-history",
          requestId: said.requestId,
          messages: page.messages.map((row) => wire(row, state.userId)),
          hasOlder: page.hasOlder,
        })
        return
      }
      case "dm-read":
        await this.read(state.userId, said.seq)
        return
      case "dm-typing":
        if (said.typing) this.startTyping(state.userId)
        else this.stopTyping(state.userId)
        return
    }
  }

  override async onClose(connection: Connection) {
    const state = stateOf(connection)
    if (!state) return
    const staying = this.socketsOf(state.userId, connection.id)
    if (state.visible && !staying.some((each) => stateOf(each)?.visible)) {
      this.tell(
        { type: "dm-presence", userId: state.userId, online: false },
        undefined,
        connection.id
      )
    }
    if (staying.length === 0) this.stopTyping(state.userId, connection.id)
  }

  /**
   * The purge (src/realtime-purge.ts), over RPC from an inbox's.
   *
   * Without a `scope` (an organization deleted), it empties the
   * conversation: every message, read state and what its inboxes were told,
   * and tells every socket `dm-gone` and closes it.
   *
   * For one of its people who left at `scope.removedAt`, the messages written
   * at or before then go, and so do any that person wrote after it on a
   * socket opened before it (a page still open from before they left). What
   * is newer stays, as a new run of the conversation: both inboxes are
   * restated from it (`InboxAgent.restateConversation`: its latest message
   * and what is unread), and sockets opened since are sent it, while those
   * opened before are told `dm-gone` and closed, so each page asks the Worker
   * whether it may open the conversation again. Nothing newer and no socket
   * opened since: it is emptied as above.
   *
   * Resolves whether it was emptied, and the caller then takes it off both
   * inboxes; otherwise it stays in them, restated. Safe to call again.
   *
   * Either way, the events waiting for Go and those Go refused go with it
   * (`EventOutbox.purge`): nothing recorded before the purge is sent, and a
   * request already on its way is answered before the purge resolves.
   */
  async purge(scope?: DmPurgeScope): Promise<{ emptied: boolean }> {
    // A scope that is not this conversation's is refused before anything
    // goes.
    const leaving = scope ? this.checkedScope(scope) : undefined
    this.epoch += 1
    this.schema()
    const eventsGone = this.events.purge()
    for (const held of this.typing.values()) clearTimeout(held.timer)
    this.typing.clear()
    this.sendBuckets.clear()
    if (!leaving) {
      await this.reset()
      this.endSockets(() => true)
      await eventsGone
      return { emptied: true }
    }
    const { userId, removedAt } = leaving
    this.sql`DELETE FROM dm_messages WHERE created_at <= ${removedAt}
      OR (author_id = ${userId} AND session_at IS NOT NULL AND session_at <= ${removedAt})`
    const openedBefore = (state: DmConnectionState) =>
      (state.connectedAt ?? "") <= removedAt
    const staying = [...this.getConnections()].flatMap((connection) => {
      const state = stateOf(connection)
      return state && !openedBefore(state) ? [{ connection, state }] : []
    })
    if (this.latestSeq() === 0 && staying.length === 0) {
      await this.reset()
      this.endSockets(() => true)
      await eventsGone
      return { emptied: true }
    }
    // What stays is a new run of the conversation.
    if (this.latestSeq() === 0) {
      this.sql`DELETE FROM dm_reads`
      this.deleteMeta("conversation")
      this.setMeta("opened", "1")
    } else {
      this.setMeta("conversation", `dmc_${randomId()}`)
    }
    const { userA, userB } = this.ids
    for (const user of [userA, userB]) this.setMeta(`restate:${user}`, "1")
    this.endSockets(openedBefore)
    for (const { connection, state } of staying) {
      send(connection, this.snapshot(state.userId))
    }
    await this.syncInboxes()
    await eventsGone
    return { emptied: false }
  }

  /** The retry's schedule: tells the inboxes what they missed; each try that fails waits twice as long for the next. */
  async retryInboxSync(): Promise<void> {
    this.schema()
    this.deleteMeta("retry")
    const attempt = Number(this.meta("retry_attempt") ?? "0") + 1
    this.setMeta("retry_attempt", String(attempt))
    await this.syncInboxes()
  }

  /** The events' schedule: sends Go what waits (`EventOutbox.drain`). */
  async drainEvents(
    _payload?: unknown,
    schedule?: Schedule<unknown>
  ): Promise<void> {
    await this.events.drain(schedule)
  }

  private get ids(): DirectMessageIds {
    const ids = parseDirectMessageName(this.name)
    if (!ids) throw new Error(`not a direct message's name: ${this.name}`)
    return ids
  }

  /** A purge's scope checked against this conversation: one of its people, and an instant. */
  private checkedScope(scope: DmPurgeScope): DmPurgeScope {
    const { userId, removedAt } = scope as {
      [Key in keyof DmPurgeScope]?: unknown
    }
    if (
      typeof userId !== "string" ||
      !peerIn(this.ids, userId) ||
      !isPurgeInstant(removedAt)
    ) {
      throw new TypeError("not a purge of this conversation")
    }
    return { userId, removedAt }
  }

  /** Empties all the conversation keeps, and lets its retry go. */
  private async reset(): Promise<void> {
    const retry = this.meta("retry")
    this.sql`DELETE FROM dm_messages`
    this.sql`DELETE FROM dm_reads`
    this.sql`DELETE FROM dm_inbox_sync`
    this.sql`DELETE FROM dm_meta`
    if (retry) await this.cancelSchedule(retry).catch(() => false)
  }

  /**
   * Tells the sockets `ending` picks (and any never let in) that this run of
   * the conversation is over, and closes them: each page asks the Worker
   * whether it may open the conversation again.
   */
  private endSockets(ending: (state: DmConnectionState) => boolean): void {
    for (const connection of this.getConnections()) {
      const state = stateOf(connection)
      if (state && !ending(state)) continue
      send(connection, { type: "dm-gone" })
      try {
        connection.close(conversationGoneCloseCode, "Gone")
      } catch {
        // Already closed.
      }
    }
  }

  private schema(): void {
    if (this.schemaReady) return
    // `session_at`: when the socket the message came on opened.
    this.sql`CREATE TABLE IF NOT EXISTS dm_messages (
      seq INTEGER PRIMARY KEY,
      id TEXT NOT NULL,
      author_id TEXT NOT NULL,
      client_id TEXT NOT NULL,
      body TEXT NOT NULL,
      created_at TEXT NOT NULL,
      session_at TEXT
    )`
    const columns = this.sql<{ name: string }>`PRAGMA table_info(dm_messages)`
    if (!columns.some((column) => column.name === "session_at")) {
      this.sql`ALTER TABLE dm_messages ADD COLUMN session_at TEXT`
    }
    this
      .sql`CREATE UNIQUE INDEX IF NOT EXISTS dm_messages_by_client ON dm_messages (author_id, client_id)`
    this.sql`CREATE TABLE IF NOT EXISTS dm_reads (
      user_id TEXT PRIMARY KEY,
      seq INTEGER NOT NULL
    )`
    // What each person's inbox has been told: messages through `noted_seq`,
    // and that they read through `read_seq`.
    this.sql`CREATE TABLE IF NOT EXISTS dm_inbox_sync (
      user_id TEXT PRIMARY KEY,
      noted_seq INTEGER NOT NULL DEFAULT 0,
      read_seq INTEGER NOT NULL DEFAULT 0
    )`
    this.sql`CREATE TABLE IF NOT EXISTS dm_meta (
      key TEXT PRIMARY KEY,
      value TEXT NOT NULL
    )`
    this.schemaReady = true
  }

  /** The conversation as a socket opens. */
  private snapshot(viewer: string): DmServerMessage {
    const page = this.page(0, DM_PAGE_SIZE)
    return {
      type: "dm-snapshot",
      conversation: this.meta("conversation") ?? "",
      messages: page.messages.map((row) => wire(row, viewer)),
      hasOlder: page.hasOlder,
      reads: this.reads(),
      online: this.online(),
      typing: [...this.typing.keys()].filter((user) => user !== viewer),
    }
  }

  /**
   * Up to `limit` messages before `beforeSeq` (0: the latest), oldest first,
   * as many of the newest as fit one frame, and whether older ones exist.
   */
  private page(
    beforeSeq: number,
    limit: number
  ): { messages: MessageRow[]; hasOlder: boolean } {
    const rows =
      beforeSeq > 0
        ? this
            .sql<MessageRow>`SELECT seq, id, author_id, client_id, body, created_at
            FROM dm_messages WHERE seq < ${beforeSeq} ORDER BY seq DESC LIMIT ${limit}`
        : this
            .sql<MessageRow>`SELECT seq, id, author_id, client_id, body, created_at
            FROM dm_messages ORDER BY seq DESC LIMIT ${limit}`
    const kept: MessageRow[] = []
    let size = 0
    for (const row of rows) {
      const rowSize = JSON.stringify(row).length
      if (kept.length > 0 && size + rowSize > maxPageCharacters) break
      kept.push(row)
      size += rowSize
    }
    kept.reverse()
    const oldest = kept.at(0)?.seq
    const hasOlder =
      oldest !== undefined &&
      this.sql<{ older: number }>`SELECT EXISTS (
        SELECT 1 FROM dm_messages WHERE seq < ${oldest}
      ) AS older`.at(0)?.older === 1
    return { messages: kept, hasOlder }
  }

  /** Says `body` as the socket's person, or tells the socket why not. */
  private async post(
    connection: Connection,
    state: DmConnectionState,
    clientId: string,
    body: string
  ): Promise<void> {
    const author = state.userId
    const stored = this
      .sql<MessageRow>`SELECT seq, id, author_id, client_id, body, created_at
      FROM dm_messages WHERE author_id = ${author} AND client_id = ${clientId}`.at(
      0
    )
    if (stored) {
      // Sent again: the page never heard it arrived.
      send(connection, { type: "dm-message", message: wire(stored, author) })
      return
    }
    const text = body.trim()
    const refused: DmSendFailure | undefined =
      text === ""
        ? "empty"
        : codePoints(text) > DM_MAX_BODY_LENGTH
          ? "too_long"
          : !this.allowSend(author)
            ? "rate_limited"
            : undefined
    if (refused) {
      this.refuse(connection, clientId, refused)
      return
    }
    let row: MessageRow
    try {
      row = this.store(author, clientId, text, state.connectedAt ?? null)
    } catch (error) {
      console.error("direct message: could not store a message", error)
      this.refuse(connection, clientId, "unavailable")
      return
    }
    for (const each of this.getConnections()) {
      const viewer = stateOf(each)?.userId
      if (viewer) send(each, { type: "dm-message", message: wire(row, viewer) })
    }
    // Sending a message reads the conversation through it.
    this.tell({ type: "dm-read", userId: author, seq: row.seq })
    this.stopTyping(author)
    await this.events.plan()
    await this.syncInboxes()
  }

  private refuse(
    connection: Connection,
    clientId: string,
    code: DmSendFailure
  ): void {
    send(connection, {
      type: "dm-send-failed",
      clientId,
      code,
      message: refusals[code],
    })
  }

  /**
   * Stores a message and what it changes as one transaction: the message
   * (with when its socket opened) and its event for Go, the oldest dropped
   * past the limit, the author's read through it (no event of its own: the
   * message's says so), and the conversation's run. When any of it fails,
   * none of it is kept.
   */
  private store(
    author: string,
    clientId: string,
    body: string,
    sessionAt: string | null
  ): MessageRow {
    return this.transaction(() => {
      const row: MessageRow = {
        seq: this.latestSeq() + 1,
        id: `dmm_${randomId()}`,
        author_id: author,
        client_id: clientId,
        body,
        created_at: new Date().toISOString(),
      }
      this
        .sql`INSERT INTO dm_messages (seq, id, author_id, client_id, body, created_at, session_at)
        VALUES (${row.seq}, ${row.id}, ${author}, ${clientId}, ${body}, ${row.created_at}, ${sessionAt})`
      this.events.recordPosted(row, memberAuthor(author), row.created_at)
      if (row.seq > DM_HISTORY_LIMIT) {
        this
          .sql`DELETE FROM dm_messages WHERE seq <= ${row.seq - DM_HISTORY_LIMIT}`
      }
      this.setRead(author, row.seq, false)
      if (!this.meta("conversation")) {
        this.setMeta("conversation", `dmc_${randomId()}`)
      }
      return row
    })
  }

  /** `reader` read through `seq` (at most the latest): both sockets and their inbox are told. */
  private async read(reader: string, seq: number): Promise<void> {
    const through = Math.min(seq, this.latestSeq())
    if (through <= this.readSeq(reader)) return
    this.setRead(reader, through)
    this.tell({ type: "dm-read", userId: reader, seq: through })
    await this.events.plan()
    await this.syncInboxes()
  }

  private see(
    connection: Connection,
    state: DmConnectionState,
    visible: boolean
  ): void {
    if (state.visible === visible) return
    const before = this.isOnline(state.userId)
    if (!update(connection, { visible })) return
    const after = this.isOnline(state.userId)
    if (before !== after) {
      this.tell({ type: "dm-presence", userId: state.userId, online: after })
    }
  }

  private startTyping(user: string): void {
    const held = this.typing.get(user)
    if (held) clearTimeout(held.timer)
    const timer = setTimeout(() => this.stopTyping(user), DM_TYPING_TTL_MS)
    const now = Date.now()
    if (held && now - held.told < dmTypingRetellMs) {
      this.typing.set(user, { timer, told: held.told })
      return
    }
    this.typing.set(user, { timer, told: now })
    this.tell(
      { type: "dm-typing", userId: user, typing: true },
      (state) => state.userId !== user
    )
  }

  private stopTyping(user: string, without?: string): void {
    const held = this.typing.get(user)
    if (!held) return
    clearTimeout(held.timer)
    this.typing.delete(user)
    this.tell(
      { type: "dm-typing", userId: user, typing: false },
      (state) => state.userId !== user,
      without
    )
  }

  /** Whether a message from `user` may go now; takes one of their sends if so. */
  private allowSend(user: string): boolean {
    const now = Date.now()
    const held = this.sendBuckets.get(user) ?? {
      tokens: dmSendLimit.burst,
      at: now,
    }
    const tokens = Math.min(
      dmSendLimit.burst,
      held.tokens + (now - held.at) / dmSendLimit.refillMs
    )
    const allowed = tokens >= 1
    this.sendBuckets.set(user, {
      tokens: allowed ? tokens - 1 : tokens,
      at: now,
    })
    return allowed
  }

  /**
   * Tells both inboxes what they have not been told: each message after
   * what they were told last (the latest `maxNotedPerUpdate` of them), then
   * how far their person has read. One run at a time; a change during it
   * runs it again. What fails is tried again on a schedule.
   */
  private syncInboxes(): Promise<void> {
    if (this.syncing) {
      this.syncAgain = true
      return this.syncing
    }
    const run = async () => {
      this.syncAgain = false
      await this.syncOnce()
      while (this.takeSyncAgain()) await this.syncOnce()
    }
    this.syncing = run().finally(() => {
      this.syncing = undefined
    })
    return this.syncing
  }

  /** Whether a change came in during the last run, which then runs again. */
  private takeSyncAgain(): boolean {
    const again = this.syncAgain
    this.syncAgain = false
    return again
  }

  private async syncOnce(): Promise<void> {
    const epoch = this.epoch
    const ids = this.ids
    if (!this.syncPending()) {
      this.deleteMeta("retry_attempt")
      return
    }
    // The alarm first: an instance lost before the inboxes answer (a
    // deploy, an eviction) still tells them once the alarm wakes it.
    await this.armRetry()
    if (this.epoch !== epoch) return
    const results = await Promise.allSettled([
      this.syncInbox(ids, ids.userA, epoch),
      this.syncInbox(ids, ids.userB, epoch),
    ])
    if (this.epoch !== epoch) return
    const failed = results.filter((result) => result.status === "rejected")
    for (const failure of failed) {
      console.error("direct message: could not update an inbox", failure.reason)
    }
    // What failed is tried again when the alarm goes off.
    if (failed.length > 0) return
    this.deleteMeta("retry_attempt")
    await this.disarmRetry()
  }

  private async syncInbox(
    ids: DirectMessageIds,
    user: string,
    epoch: number
  ): Promise<void> {
    const restate = this.meta(`restate:${user}`) === "1"
    const known = this.meta(`known:${user}`) === "1"
    const told = this.told(user)
    const fresh = restate
      ? []
      : this
          .sql<MessageRow>`SELECT seq, id, author_id, client_id, body, created_at
      FROM dm_messages WHERE seq > ${told.noted}
      ORDER BY seq DESC LIMIT ${maxNotedPerUpdate}`.reverse()
    if (
      !restate &&
      known &&
      fresh.length === 0 &&
      this.readSeq(user) <= told.read
    ) {
      return
    }
    const inbox = await getAgentByName(
      this.env.InboxAgent,
      inboxName(ids.organizationId, user)
    )
    if (this.epoch !== epoch) return
    const entry = {
      kind: "dm" as const,
      name: this.name,
      peerId: peerIn(ids, user) ?? "",
    }
    if (restate) {
      // After a purge that kept something: the inbox is told what stays,
      // in place of what it had.
      await inbox.restateConversation(entry, this.restated(user))
      if (this.epoch !== epoch) return
      this.sql`INSERT INTO dm_inbox_sync (user_id, noted_seq, read_seq)
        VALUES (${user}, ${this.latestSeq()}, ${this.readSeq(user)})
        ON CONFLICT (user_id) DO UPDATE SET
          noted_seq = excluded.noted_seq, read_seq = excluded.read_seq`
      this.setMeta(`known:${user}`, "1")
      this.deleteMeta(`restate:${user}`)
      return
    }
    const last = fresh.at(-1)
    if (last) {
      await inbox.noteMessage(entry, ...fresh.map(noted))
      if (this.epoch !== epoch) return
      this
        .sql`INSERT INTO dm_inbox_sync (user_id, noted_seq) VALUES (${user}, ${last.seq})
        ON CONFLICT (user_id) DO UPDATE SET noted_seq = MAX(noted_seq, excluded.noted_seq)`
    } else if (!known) {
      // Opened without a message yet: known to the inbox, listed by none.
      await inbox.knowConversation(entry)
      if (this.epoch !== epoch) return
    }
    if (!known) this.setMeta(`known:${user}`, "1")
    const read = this.readSeq(user)
    if (read > told.read) {
      // An inbox reads through its member's own message as it is noted: a
      // read that goes no further than that needs no word of its own.
      const readByNote =
        fresh.filter((row) => row.author_id === user).at(-1)?.seq ?? 0
      if (read > readByNote) {
        await inbox.markRead(this.name, read)
        if (this.epoch !== epoch) return
      }
      this
        .sql`INSERT INTO dm_inbox_sync (user_id, read_seq) VALUES (${user}, ${read})
        ON CONFLICT (user_id) DO UPDATE SET read_seq = MAX(read_seq, excluded.read_seq)`
    }
  }

  /**
   * Whether an inbox has yet to be told something: that the conversation
   * exists, once it has opened, or a message or a read since.
   */
  private syncPending(): boolean {
    const latest = this.latestSeq()
    if (latest === 0 && this.meta("opened") !== "1") return false
    const { userA, userB } = this.ids
    return [userA, userB].some((user) => {
      const told = this.told(user)
      return (
        this.meta(`restate:${user}`) === "1" ||
        this.meta(`known:${user}`) !== "1" ||
        told.noted < latest ||
        told.read < this.readSeq(user)
      )
    })
  }

  /** What a person's inbox keeps of the conversation, restated from what it holds now. */
  private restated(user: string): RestatedConversation {
    const last = this
      .sql<MessageRow>`SELECT seq, id, author_id, client_id, body, created_at
      FROM dm_messages ORDER BY seq DESC LIMIT 1`.at(0)
    const readSeq = this.readSeq(user)
    const unread = this.sql<{ seq: number }>`SELECT seq FROM dm_messages
      WHERE author_id != ${user} AND seq > ${readSeq}
      ORDER BY seq DESC LIMIT ${MAX_UNREAD}`
      .map((row) => row.seq)
      .reverse()
    return { ...(last ? { last: noted(last) } : {}), readSeq, unread }
  }

  /**
   * Makes sure an alarm will run the sync again: `dmInboxRetry.firstSeconds`
   * from now, doubled for each retry that failed in a row, at most
   * `dmInboxRetry.maxSeconds`.
   */
  private async armRetry(): Promise<void> {
    if (this.meta("retry")) return
    const attempt = Number(this.meta("retry_attempt") ?? "0")
    const delay = Math.min(
      dmInboxRetry.firstSeconds * 2 ** attempt,
      dmInboxRetry.maxSeconds
    )
    const schedule = await this.schedule(delay, "retryInboxSync")
    this.setMeta("retry", schedule.id)
  }

  private async disarmRetry(): Promise<void> {
    const retry = this.meta("retry")
    if (!retry) return
    this.deleteMeta("retry")
    await this.cancelSchedule(retry).catch(() => false)
  }

  private told(user: string): { noted: number; read: number } {
    const row = this.sql<{ noted_seq: number; read_seq: number }>`
      SELECT noted_seq, read_seq FROM dm_inbox_sync WHERE user_id = ${user}`.at(
      0
    )
    return { noted: row?.noted_seq ?? 0, read: row?.read_seq ?? 0 }
  }

  private latestSeq(): number {
    const row = this.sql<{
      seq: number | null
    }>`SELECT MAX(seq) AS seq FROM dm_messages`.at(0)
    return row?.seq ?? 0
  }

  private readSeq(user: string): number {
    const row = this.sql<{
      seq: number
    }>`SELECT seq FROM dm_reads WHERE user_id = ${user}`.at(0)
    return row?.seq ?? 0
  }

  /**
   * Reads `user` through `seq`, only ever further, and records it for Go in
   * the same transaction when it goes further, unless `record` says the
   * read is an event's already (an author's through their own message).
   */
  private setRead(user: string, seq: number, record = true): void {
    this.transaction(() => {
      const before = this.readSeq(user)
      this.sql`INSERT INTO dm_reads (user_id, seq) VALUES (${user}, ${seq})
        ON CONFLICT (user_id) DO UPDATE SET seq = MAX(seq, excluded.seq)`
      if (record && seq > before) {
        this.events.recordRead(user, seq, new Date().toISOString())
      }
    })
  }

  /** Runs `work` as one SQLite transaction: all it writes, or none of it when it throws. */
  private transaction<T>(work: () => T): T {
    return this.ctx.storage.transactionSync(work)
  }

  private reads(): DmRead[] {
    const { userA, userB } = this.ids
    return [userA, userB].map((userId) => ({
      userId,
      seq: this.readSeq(userId),
    }))
  }

  /** The people with a visible socket here, leaving out one that is closing. */
  private online(without?: string): string[] {
    const here = new Set<string>()
    for (const connection of this.getConnections()) {
      if (connection.id === without) continue
      const state = stateOf(connection)
      if (state?.visible) here.add(state.userId)
    }
    return [...here].sort()
  }

  private isOnline(user: string): boolean {
    return this.online().includes(user)
  }

  private socketsOf(user: string, without?: string): Connection[] {
    return [...this.getConnections()].filter(
      (connection) =>
        connection.id !== without && stateOf(connection)?.userId === user
    )
  }

  /** Sends every socket here the message, or those `to` picks, leaving out one that is closing. */
  private tell(
    message: DmServerMessage,
    to?: (state: DmConnectionState) => boolean,
    without?: string
  ): void {
    const data = JSON.stringify(message)
    for (const connection of this.getConnections()) {
      if (connection.id === without) continue
      const state = stateOf(connection)
      if (!state || (to && !to(state))) continue
      send(connection, data)
    }
  }

  private meta(key: string): string | undefined {
    const row = this.sql<{
      value: string
    }>`SELECT value FROM dm_meta WHERE key = ${key}`.at(0)
    return row?.value
  }

  private setMeta(key: string, value: string): void {
    this.sql`INSERT INTO dm_meta (key, value) VALUES (${key}, ${value})
      ON CONFLICT (key) DO UPDATE SET value = excluded.value`
  }

  private deleteMeta(key: string): void {
    this.sql`DELETE FROM dm_meta WHERE key = ${key}`
  }
}
