import { Agent, getAgentByName } from "agents"
import type { Connection, ConnectionContext, WSMessage } from "agents"
import { conversationRequestHeaders } from "./conversation-env"
import type { ConversationEnv } from "./conversation-env"
import type { NotedMessage } from "./inbox-agent"
import { inboxName, parseDirectMessageName, peerIn } from "./lib/dms/names"
import type { DirectMessageIds } from "./lib/dms/names"
import {
  DM_HISTORY_LIMIT,
  DM_MAX_BODY_LENGTH,
  DM_PAGE_SIZE,
  DM_TYPING_TTL_MS,
  codePoints,
  conversationGoneCloseCode,
  parseDmClientMessage,
  preview,
} from "./lib/dms/protocol"
import type {
  DmRead,
  DmSendFailure,
  DmServerMessage,
  DmWireMessage,
} from "./lib/dms/protocol"

/**
 * What a socket keeps, in its attachment (so hibernation keeps it): who it
 * is, as the Worker determined it, and whether its page says the tab is
 * visible.
 */
export type DmConnectionState = {
  userId: string
  /** False until the page says. */
  visible: boolean
}

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

  // After a restart: an inbox that was never told is told again.
  override async onStart(): Promise<void> {
    this.schema()
    if (this.syncPending() && !this.meta("retry")) await this.scheduleRetry()
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
    connection.setState({ userId, visible: false } satisfies DmConnectionState)
    this.schema()
    send(connection, this.snapshot(userId))
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
        await this.post(connection, state.userId, said.clientId, said.body)
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
   * Empties the conversation: its messages, read states and what its inboxes
   * were told, and closes every socket (`dm-gone`, then
   * `conversationGoneCloseCode`). The purge (src/realtime-purge.ts) calls it
   * over RPC when one of its people leaves the organization, and removes it
   * from both inboxes itself. Safe to call again.
   */
  async purge(): Promise<void> {
    this.epoch += 1
    this.schema()
    for (const held of this.typing.values()) clearTimeout(held.timer)
    this.typing.clear()
    this.sendBuckets.clear()
    const retry = this.meta("retry")
    this.sql`DELETE FROM dm_messages`
    this.sql`DELETE FROM dm_reads`
    this.sql`DELETE FROM dm_inbox_sync`
    this.sql`DELETE FROM dm_meta`
    if (retry) await this.cancelSchedule(retry).catch(() => false)
    for (const connection of this.getConnections()) {
      send(connection, { type: "dm-gone" })
      try {
        connection.close(conversationGoneCloseCode, "Gone")
      } catch {
        // Already closed.
      }
    }
  }

  /** The retry's schedule: tells the inboxes what they missed. */
  async retryInboxSync(): Promise<void> {
    this.schema()
    this.deleteMeta("retry")
    await this.syncInboxes()
  }

  private get ids(): DirectMessageIds {
    const ids = parseDirectMessageName(this.name)
    if (!ids) throw new Error(`not a direct message's name: ${this.name}`)
    return ids
  }

  private schema(): void {
    if (this.schemaReady) return
    this.sql`CREATE TABLE IF NOT EXISTS dm_messages (
      seq INTEGER PRIMARY KEY,
      id TEXT NOT NULL,
      author_id TEXT NOT NULL,
      client_id TEXT NOT NULL,
      body TEXT NOT NULL,
      created_at TEXT NOT NULL
    )`
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

  /** Says `body` as `author`, or tells the socket why not. */
  private async post(
    connection: Connection,
    author: string,
    clientId: string,
    body: string
  ): Promise<void> {
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
      row = this.store(author, clientId, text)
    } catch (error) {
      console.error("direct message: could not store a message", error)
      this.refuse(connection, clientId, "unavailable")
      return
    }
    for (const each of this.getConnections()) {
      const state = stateOf(each)
      if (state)
        send(each, { type: "dm-message", message: wire(row, state.userId) })
    }
    // Sending a message reads the conversation through it.
    this.tell({ type: "dm-read", userId: author, seq: row.seq })
    this.stopTyping(author)
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
   * Stores a message and what it changes, synchronously, so that it is
   * written as one: the message, the oldest dropped past the limit, the
   * author's read through it, and the conversation's run.
   */
  private store(author: string, clientId: string, body: string): MessageRow {
    const row: MessageRow = {
      seq: this.latestSeq() + 1,
      id: `dmm_${randomId()}`,
      author_id: author,
      client_id: clientId,
      body,
      created_at: new Date().toISOString(),
    }
    this
      .sql`INSERT INTO dm_messages (seq, id, author_id, client_id, body, created_at)
      VALUES (${row.seq}, ${row.id}, ${author}, ${clientId}, ${body}, ${row.created_at})`
    if (row.seq > DM_HISTORY_LIMIT) {
      this
        .sql`DELETE FROM dm_messages WHERE seq <= ${row.seq - DM_HISTORY_LIMIT}`
    }
    this.setRead(author, row.seq)
    if (!this.meta("conversation")) {
      this.setMeta("conversation", `dmc_${randomId()}`)
    }
    return row
  }

  /** `reader` read through `seq` (at most the latest): both sockets and their inbox are told. */
  private async read(reader: string, seq: number): Promise<void> {
    const through = Math.min(seq, this.latestSeq())
    if (through <= this.readSeq(reader)) return
    this.setRead(reader, through)
    this.tell({ type: "dm-read", userId: reader, seq: through })
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
    const results = await Promise.allSettled([
      this.syncInbox(ids, ids.userA, epoch),
      this.syncInbox(ids, ids.userB, epoch),
    ])
    if (this.epoch !== epoch) return
    const failed = results.filter((result) => result.status === "rejected")
    for (const failure of failed) {
      console.error("direct message: could not update an inbox", failure.reason)
    }
    if (failed.length > 0) {
      await this.scheduleRetry()
      return
    }
    this.deleteMeta("retry_attempt")
    const retry = this.meta("retry")
    if (retry) {
      this.deleteMeta("retry")
      await this.cancelSchedule(retry).catch(() => false)
    }
  }

  private async syncInbox(
    ids: DirectMessageIds,
    user: string,
    epoch: number
  ): Promise<void> {
    const told = this.told(user)
    const fresh = this
      .sql<MessageRow>`SELECT seq, id, author_id, client_id, body, created_at
      FROM dm_messages WHERE seq > ${told.noted}
      ORDER BY seq DESC LIMIT ${maxNotedPerUpdate}`.reverse()
    if (fresh.length === 0 && this.readSeq(user) <= told.read) return
    const inbox = await getAgentByName(
      this.env.InboxAgent,
      inboxName(ids.organizationId, user)
    )
    if (this.epoch !== epoch) return
    const last = fresh.at(-1)
    if (last) {
      await inbox.noteMessage(
        { kind: "dm", name: this.name, peerId: peerIn(ids, user) ?? "" },
        ...fresh.map(noted)
      )
      if (this.epoch !== epoch) return
      this
        .sql`INSERT INTO dm_inbox_sync (user_id, noted_seq) VALUES (${user}, ${last.seq})
        ON CONFLICT (user_id) DO UPDATE SET noted_seq = MAX(noted_seq, excluded.noted_seq)`
    }
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

  /** Whether an inbox has yet to be told something. */
  private syncPending(): boolean {
    const latest = this.latestSeq()
    if (latest === 0) return false
    const { userA, userB } = this.ids
    return [userA, userB].some((user) => {
      const told = this.told(user)
      return told.noted < latest || told.read < this.readSeq(user)
    })
  }

  private async scheduleRetry(): Promise<void> {
    if (this.meta("retry")) return
    const attempt = Number(this.meta("retry_attempt") ?? "0") + 1
    const delay = Math.min(
      dmInboxRetry.firstSeconds * 2 ** (attempt - 1),
      dmInboxRetry.maxSeconds
    )
    const schedule = await this.schedule(delay, "retryInboxSync")
    this.setMeta("retry", schedule.id)
    this.setMeta("retry_attempt", String(attempt))
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

  private setRead(user: string, seq: number): void {
    this.sql`INSERT INTO dm_reads (user_id, seq) VALUES (${user}, ${seq})
      ON CONFLICT (user_id) DO UPDATE SET seq = MAX(seq, excluded.seq)`
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
