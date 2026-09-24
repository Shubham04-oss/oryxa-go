import { Agent, getAgentByName } from "agents"
import type { Connection, ConnectionContext } from "agents"
import { conversationRequestHeaders } from "./conversation-env"
import type { ConversationEnv } from "./conversation-env"
import {
  inboxName,
  isNameId,
  parseDirectMessageName,
  parseInboxName,
  parseSideConversationName,
  peerIn,
  registryName,
} from "./lib/dms/names"
import {
  MAX_UNREAD,
  conversationGoneCloseCode,
  preview,
} from "./lib/dms/protocol"
import type { InboxServerMessage } from "./lib/dms/protocol"
import type { Conversation, ConversationKind } from "./lib/dms/types"
import { isPurgeInstant } from "./realtime-registry-agent"

/**
 * What a conversation object tells an inbox about itself: its kind and name,
 * the other person in it, and for a side conversation its teamspace.
 */
export type ConversationEntry = {
  kind: ConversationKind
  name: string
  peerId: string
  teamspaceId?: string
  teamspaceName?: string
}

/** A message as an inbox is told of it: enough for its preview and the unread count. */
export type NotedMessage = {
  /** The conversation's own order, from 1. */
  seq: number
  authorId: string
  /** At most a preview's worth is kept. */
  body: string
  createdAt: string
}

/**
 * What a conversation's object says an inbox keeps of it, after a purge that
 * kept part of it: its latest message (none: known, not listed), how far the
 * member has read, and the seqs of the later messages from others.
 */
export type RestatedConversation = {
  last?: NotedMessage
  readSeq: number
  unread: number[]
}

/** A socket's state: whose inbox it opened, as the Worker determined it, and when. */
export type InboxConnectionState = { userId: string; connectedAt?: string }

/** The most conversations a socket is sent as it opens, the latest first. The unread total counts them all. */
export const INBOX_SNAPSHOT_LIMIT = 500

/** How long the inbox waits before registering with its organization again after it failed to. */
const registrationRetrySeconds = 30

/** How many conversations a purge empties at once. */
const purgeConcurrency = 4

/**
 * How many times a purge goes through its conversations: one noted while it
 * was emptying the others is found by the next pass.
 */
const purgePasses = 5

const maxTeamspaceName = 200

type ConversationRow = {
  name: string
  kind: ConversationKind
  peer_id: string
  teamspace_id: string | null
  teamspace_name: string | null
  last_seq: number
  last_author_id: string | null
  last_body: string | null
  last_created_at: string | null
  read_seq: number
  updated_at: string
  unread: number
}

function send(connection: Connection, message: InboxServerMessage | string) {
  try {
    connection.send(
      typeof message === "string" ? message : JSON.stringify(message)
    )
  } catch {
    // It closed meanwhile.
  }
}

function conversationOf(row: ConversationRow): Conversation {
  return {
    kind: row.kind,
    name: row.name,
    peerId: row.peer_id,
    ...(row.teamspace_id === null ? {} : { teamspaceId: row.teamspace_id }),
    ...(row.teamspace_name === null
      ? {}
      : { teamspaceName: row.teamspace_name }),
    ...(row.last_author_id === null ||
    row.last_body === null ||
    row.last_created_at === null
      ? {}
      : {
          lastMessage: {
            authorId: row.last_author_id,
            body: row.last_body,
            createdAt: row.last_created_at,
          },
        }),
    unread: row.unread,
    updatedAt: row.updated_at,
  }
}

function isSeq(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0
}

/** Runs `work` for each item, at most `concurrency` at a time; resolves how many failed. */
async function eachLimited<T>(
  items: readonly T[],
  concurrency: number,
  work: (item: T) => Promise<void>
): Promise<number> {
  let next = 0
  let failed = 0
  const worker = async () => {
    while (next < items.length) {
      const item = items[next]
      next += 1
      try {
        await work(item)
      } catch (error) {
        failed += 1
        console.error("inbox: could not purge a conversation", error)
      }
    }
  }
  await Promise.all(
    Array.from({ length: Math.min(concurrency, items.length) }, worker)
  )
  return failed
}

/**
 * A member's conversation list in one organization: one object per member,
 * named `inboxName(org, user)`. The DMs tab and its badges read it live;
 * only its member's sockets reach it (the Worker checks with Go), and they
 * only listen.
 *
 * The conversation objects keep it current over RPC: `noteMessage` for each
 * message, `markRead` as the member reads, `upsertConversation` to list one
 * before it has a message, and `removeConversation`. For each conversation
 * it keeps the kind, name, other person and teamspace, the latest message's
 * preview, how far the member has read, and which messages from others
 * after that they have not (so a message told twice counts once, and reads
 * and messages may arrive in either order). Every change reaches the
 * member's sockets with the new unread total.
 *
 * Its first conversation registers it with its organization's registry
 * (`RealtimeRegistryAgent`), so an organization's purge finds it; `purge`
 * empties every conversation it lists, and then itself.
 */
export class InboxAgent extends Agent<ConversationEnv> {
  private schemaReady = false

  // The socket carries this object's protocol only.
  override shouldSendProtocolMessages(): boolean {
    return false
  }

  override validateStateChange(
    _nextState: unknown,
    source: Connection | "server"
  ): void {
    if (source !== "server") {
      throw new Error("An inbox has no state a page may set.")
    }
  }

  // The Worker's check for a page whose socket was refused.
  override onRequest(request: Request): Response {
    if (request.method !== "GET") {
      return new Response(null, { status: 405, headers: { Allow: "GET" } })
    }
    return new Response(null, { status: 204 })
  }

  override async onConnect(connection: Connection, context: ConnectionContext) {
    const { headers } = context.request
    const owner = parseInboxName(this.name)
    if (
      !owner ||
      headers.get(conversationRequestHeaders.organization) !==
        owner.organizationId ||
      headers.get(conversationRequestHeaders.user) !== owner.userId
    ) {
      connection.close(1008, "Unauthorized")
      return
    }
    connection.setState({
      userId: owner.userId,
      connectedAt: new Date().toISOString(),
    } satisfies InboxConnectionState)
    this.schema()
    send(connection, {
      type: "inbox-snapshot",
      conversations: this.listed(INBOX_SNAPSHOT_LIMIT),
      unreadTotal: this.unreadTotal(),
    })
  }

  // A page says nothing on this socket: the conversations change it.
  override onMessage(): void {}

  /**
   * One or more messages in a conversation, told by its object. Lists the
   * conversation if it was not, keeps the latest message as its preview,
   * counts a message from someone else after what the member has read as
   * unread, and reads through the member's own message. Safe to tell twice.
   */
  async noteMessage(
    entry: ConversationEntry,
    ...messages: NotedMessage[]
  ): Promise<void> {
    this.schema()
    const conversation = this.checkedEntry(entry)
    const notes = messages
      .map((message) => this.checkedMessage(conversation, message))
      .sort((left, right) => left.seq - right.seq)
    const latest = notes.at(-1)
    if (!latest) return
    this.list(conversation, latest.createdAt)
    const { name } = conversation
    for (const note of notes) {
      this.sql`UPDATE inbox_conversations SET
          last_seq = ${note.seq},
          last_author_id = ${note.authorId},
          last_body = ${note.body},
          last_created_at = ${note.createdAt},
          updated_at = MAX(updated_at, ${note.createdAt})
        WHERE name = ${name} AND last_seq < ${note.seq}`
      if (note.authorId === this.owner.userId) {
        this.readThrough(name, note.seq)
      } else {
        this.sql`INSERT INTO inbox_unread (name, seq)
          SELECT ${name}, ${note.seq}
          WHERE ${note.seq} > (SELECT read_seq FROM inbox_conversations WHERE name = ${name})
          ON CONFLICT DO NOTHING`
      }
    }
    this.capUnread(name)
    this.announce(name)
    await this.register()
  }

  /**
   * The member read `name` through `seq`: what they have not read is only
   * what comes after it. A conversation not listed yet is left alone, so its
   * object notes a message before it marks it read.
   */
  async markRead(name: string, seq: number): Promise<void> {
    this.schema()
    if (typeof name !== "string" || !isSeq(seq)) {
      throw new TypeError("markRead needs a conversation and a seq")
    }
    if (!this.row(name)) return
    this.readThrough(name, seq)
    this.announce(name)
  }

  /** Lists a conversation without a message (a side conversation as it starts), or updates what it says about itself. */
  async upsertConversation(entry: ConversationEntry): Promise<void> {
    this.schema()
    const conversation = this.checkedEntry(entry)
    this.list(conversation, new Date().toISOString())
    this.announce(conversation.name)
    await this.register()
  }

  /** Takes a conversation off the list, with what it counted unread. */
  async removeConversation(name: string): Promise<void> {
    this.schema()
    if (typeof name !== "string") {
      throw new TypeError("removeConversation needs a conversation")
    }
    this.sql`DELETE FROM inbox_known WHERE name = ${name}`
    if (!this.row(name)) return
    this.forgetConversation(name)
  }

  /**
   * A conversation the member is in exists, though it lists nothing yet (a
   * DM opened before its first message). The inbox keeps it unseen, so a
   * purge of the member finds it too.
   */
  async knowConversation(entry: ConversationEntry): Promise<void> {
    this.schema()
    const conversation = this.checkedEntry(entry)
    this.sql`INSERT INTO inbox_known (name, kind, peer_id)
      VALUES (${conversation.name}, ${conversation.kind}, ${conversation.peerId})
      ON CONFLICT (name) DO NOTHING`
    await this.register()
  }

  /** Every conversation listed, the latest first. */
  async listConversations(): Promise<Conversation[]> {
    this.schema()
    return this.listed()
  }

  /**
   * A conversation's object restates what the inbox keeps of it, after a
   * purge that kept part of it: its latest message, how far the member has
   * read, and what is unread, in place of what the inbox had. Without a
   * latest message the conversation is known, not listed.
   */
  async restateConversation(
    entry: ConversationEntry,
    state: RestatedConversation
  ): Promise<void> {
    this.schema()
    const conversation = this.checkedEntry(entry)
    const { name } = conversation
    const { last, readSeq, unread } = state as {
      [Key in keyof RestatedConversation]?: unknown
    }
    if (
      typeof readSeq !== "number" ||
      !Number.isSafeInteger(readSeq) ||
      readSeq < 0 ||
      !Array.isArray(unread) ||
      !unread.every((seq) => isSeq(seq) && seq > readSeq)
    ) {
      throw new TypeError("not a restated conversation")
    }
    if (last === undefined) {
      const listed = this.row(name) !== undefined
      this.sql`DELETE FROM inbox_conversations WHERE name = ${name}`
      this.sql`DELETE FROM inbox_unread WHERE name = ${name}`
      this.sql`INSERT INTO inbox_known (name, kind, peer_id)
        VALUES (${name}, ${conversation.kind}, ${conversation.peerId})
        ON CONFLICT (name) DO NOTHING`
      if (listed) {
        this.tell({
          type: "inbox-removed",
          name,
          unreadTotal: this.unreadTotal(),
        })
      }
      await this.register()
      return
    }
    const latest = this.checkedMessage(conversation, last as NotedMessage)
    this.list(conversation, latest.createdAt)
    this.sql`UPDATE inbox_conversations SET
        last_seq = ${latest.seq},
        last_author_id = ${latest.authorId},
        last_body = ${latest.body},
        last_created_at = ${latest.createdAt},
        read_seq = ${readSeq},
        updated_at = ${latest.createdAt}
      WHERE name = ${name}`
    this.sql`DELETE FROM inbox_unread WHERE name = ${name}`
    for (const seq of (unread as number[]).slice(-MAX_UNREAD)) {
      this.sql`INSERT INTO inbox_unread (name, seq) VALUES (${name}, ${seq})
        ON CONFLICT DO NOTHING`
    }
    this.sql`DELETE FROM inbox_known WHERE name = ${name}`
    this.announce(name)
    await this.register()
  }

  /**
   * The member's purge (src/realtime-purge.ts, through the registry). Each
   * conversation they are in, listed or only known, is purged by its object:
   * entirely, for an organization's purge (no `scope`), or, for a member who
   * left at `scope.removedAt`, all but what is newer. A conversation emptied
   * is taken off the other person's inbox and this one; one that kept
   * something newer stays in both, restated by its object. Conversations
   * that arrive meanwhile are purged too.
   *
   * Then, if nothing stays, the inbox empties itself. Its sockets (all of
   * them for an organization's purge, else those opened at or before the
   * member left) are told `inbox-gone` and closed, so each page asks the
   * Worker whether it may open the inbox again; the rest are sent it as it
   * now stands. A conversation that could not be purged stays, and the purge
   * throws, so trying again finds it. Safe to call again. Resolves whether
   * the inbox was emptied.
   */
  async purge(scope?: { removedAt: string }): Promise<{ emptied: boolean }> {
    this.schema()
    const { organizationId, userId } = this.owner
    const removedAt = scope?.removedAt
    if (scope !== undefined && !isPurgeInstant(removedAt)) {
      throw new TypeError("not a purge of this inbox")
    }
    const purged = new Set<string>()
    for (let pass = 0; pass < purgePasses; pass += 1) {
      const conversations = this.everyConversation().filter(
        (conversation) => !purged.has(conversation.name)
      )
      if (conversations.length === 0) break
      const failed = await eachLimited(
        conversations,
        purgeConcurrency,
        async (conversation) => {
          let emptied = true
          if (conversation.kind === "dm") {
            const dm = await getAgentByName(
              this.env.DirectMessageAgent,
              conversation.name
            )
            const result = await dm.purge(
              removedAt === undefined ? undefined : { userId, removedAt }
            )
            emptied = result.emptied
          }
          // A side conversation's object comes later: its purge belongs here.
          if (emptied) {
            const peer = await getAgentByName(
              this.env.InboxAgent,
              inboxName(organizationId, conversation.peerId)
            )
            await peer.removeConversation(conversation.name)
            this.sql`DELETE FROM inbox_known WHERE name = ${conversation.name}`
            if (this.row(conversation.name)) {
              this.forgetConversation(conversation.name)
            }
          }
          purged.add(conversation.name)
        }
      )
      if (failed > 0) {
        throw new Error(`${failed} conversations could not be purged`)
      }
    }
    const staying = this.everyConversation()
    if (removedAt === undefined && staying.length > 0) {
      throw new Error("conversations kept arriving while the inbox was purged")
    }
    const emptied = staying.length === 0
    if (emptied) {
      const retry = this.meta("registration_retry")
      this.sql`DELETE FROM inbox_conversations`
      this.sql`DELETE FROM inbox_known`
      this.sql`DELETE FROM inbox_unread`
      this.sql`DELETE FROM inbox_meta`
      if (retry) await this.cancelSchedule(retry).catch(() => false)
    }
    for (const connection of this.getConnections()) {
      const state = connection.state as InboxConnectionState | null
      if (
        removedAt === undefined ||
        typeof state?.userId !== "string" ||
        (state.connectedAt ?? "") <= removedAt
      ) {
        send(connection, { type: "inbox-gone" })
        try {
          connection.close(conversationGoneCloseCode, "Gone")
        } catch {
          // Already closed.
        }
      } else {
        send(connection, {
          type: "inbox-snapshot",
          conversations: this.listed(INBOX_SNAPSHOT_LIMIT),
          unreadTotal: this.unreadTotal(),
        })
      }
    }
    return { emptied }
  }

  /** The registration retry's schedule. */
  async retryRegistration(): Promise<void> {
    this.schema()
    this.deleteMeta("registration_retry")
    if (this.listed(1).length > 0) await this.register()
  }

  private get owner(): { organizationId: string; userId: string } {
    const owner = parseInboxName(this.name)
    if (!owner) throw new Error(`not an inbox's name: ${this.name}`)
    return owner
  }

  private schema(): void {
    if (this.schemaReady) return
    this.sql`CREATE TABLE IF NOT EXISTS inbox_conversations (
      name TEXT PRIMARY KEY,
      kind TEXT NOT NULL,
      peer_id TEXT NOT NULL,
      teamspace_id TEXT,
      teamspace_name TEXT,
      last_seq INTEGER NOT NULL DEFAULT 0,
      last_author_id TEXT,
      last_body TEXT,
      last_created_at TEXT,
      read_seq INTEGER NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL
    )`
    // The messages from others the member has not read, by conversation.
    this.sql`CREATE TABLE IF NOT EXISTS inbox_unread (
      name TEXT NOT NULL,
      seq INTEGER NOT NULL,
      PRIMARY KEY (name, seq)
    )`
    // Conversations the member is in that list nothing yet: only a purge
    // reads them.
    this.sql`CREATE TABLE IF NOT EXISTS inbox_known (
      name TEXT PRIMARY KEY,
      kind TEXT NOT NULL,
      peer_id TEXT NOT NULL
    )`
    this.sql`CREATE TABLE IF NOT EXISTS inbox_meta (
      key TEXT PRIMARY KEY,
      value TEXT NOT NULL
    )`
    this.schemaReady = true
  }

  /**
   * A conversation entry checked against this inbox: a conversation of its
   * organization that its member is in, with `peerId` the other person.
   * Throws for anything else, so a conversation object can never list
   * itself in the wrong inbox.
   */
  private checkedEntry(entry: ConversationEntry): ConversationEntry {
    const owner = this.owner
    // Read as whatever arrived over RPC, not as the type says it is.
    const { kind, name, peerId, teamspaceId, teamspaceName } = entry as {
      [Key in keyof ConversationEntry]?: unknown
    }
    if (
      typeof name !== "string" ||
      !isNameId(peerId) ||
      peerId === owner.userId
    ) {
      throw new TypeError("not a conversation of this inbox")
    }
    if (kind === "dm") {
      const ids = parseDirectMessageName(name)
      if (
        !ids ||
        ids.organizationId !== owner.organizationId ||
        peerIn(ids, owner.userId) !== peerId ||
        teamspaceId !== undefined ||
        teamspaceName !== undefined
      ) {
        throw new TypeError("not a direct message of this inbox")
      }
      return { kind, name, peerId }
    }
    if (kind === "side") {
      const ids = parseSideConversationName(name)
      if (
        !ids ||
        ids.organizationId !== owner.organizationId ||
        peerIn(ids, owner.userId) !== peerId ||
        teamspaceId !== ids.teamspaceId ||
        (teamspaceName !== undefined &&
          (typeof teamspaceName !== "string" ||
            teamspaceName.length > maxTeamspaceName))
      ) {
        throw new TypeError("not a side conversation of this inbox")
      }
      return {
        kind,
        name,
        peerId,
        teamspaceId: ids.teamspaceId,
        ...(typeof teamspaceName === "string" && teamspaceName
          ? { teamspaceName }
          : {}),
      }
    }
    throw new TypeError("unknown conversation kind")
  }

  private checkedMessage(
    conversation: ConversationEntry,
    message: NotedMessage
  ): NotedMessage {
    const { seq, authorId, body, createdAt } = message as {
      [Key in keyof NotedMessage]?: unknown
    }
    const at = typeof createdAt === "string" ? Date.parse(createdAt) : NaN
    if (
      !isSeq(seq) ||
      typeof authorId !== "string" ||
      authorId === "" ||
      typeof body !== "string" ||
      Number.isNaN(at)
    ) {
      throw new TypeError("not a message this inbox can note")
    }
    // In a DM only its two people write.
    if (
      conversation.kind === "dm" &&
      authorId !== this.owner.userId &&
      authorId !== conversation.peerId
    ) {
      throw new TypeError("not a message of this direct message")
    }
    return {
      seq,
      authorId,
      body: preview(body),
      createdAt: new Date(at).toISOString(),
    }
  }

  /** Lists a conversation, or updates what it says about itself. */
  private list(conversation: ConversationEntry, at: string): void {
    this
      .sql`INSERT INTO inbox_conversations (name, kind, peer_id, teamspace_id, teamspace_name, updated_at)
      VALUES (${conversation.name}, ${conversation.kind}, ${conversation.peerId},
        ${conversation.teamspaceId ?? null}, ${conversation.teamspaceName ?? null}, ${at})
      ON CONFLICT (name) DO UPDATE SET
        kind = excluded.kind,
        peer_id = excluded.peer_id,
        teamspace_id = excluded.teamspace_id,
        teamspace_name = COALESCE(excluded.teamspace_name, teamspace_name)`
  }

  private readThrough(name: string, seq: number): void {
    this
      .sql`UPDATE inbox_conversations SET read_seq = MAX(read_seq, ${seq}) WHERE name = ${name}`
    this.sql`DELETE FROM inbox_unread WHERE name = ${name} AND seq <= ${seq}`
  }

  /** Keeps at most `MAX_UNREAD` unread messages for a conversation, the newest. */
  private capUnread(name: string): void {
    const count = this.unreadIn(name)
    if (count <= MAX_UNREAD) return
    this.sql`DELETE FROM inbox_unread WHERE name = ${name} AND seq IN (
      SELECT seq FROM inbox_unread WHERE name = ${name}
      ORDER BY seq ASC LIMIT ${count - MAX_UNREAD}
    )`
  }

  private forgetConversation(name: string): void {
    this.sql`DELETE FROM inbox_conversations WHERE name = ${name}`
    this.sql`DELETE FROM inbox_unread WHERE name = ${name}`
    this.tell({
      type: "inbox-removed",
      name,
      unreadTotal: this.unreadTotal(),
    })
  }

  /** Sends the member's sockets a conversation as it stands now. */
  private announce(name: string): void {
    const row = this.row(name)
    if (!row) return
    this.tell({
      type: "inbox-conversation",
      conversation: conversationOf(row),
      unreadTotal: this.unreadTotal(),
    })
  }

  /**
   * Registers the inbox with its organization's registry, once: every write
   * tries until it has, and a failure is also tried again on a schedule.
   */
  private async register(): Promise<void> {
    if (this.meta("registered") === "1") return
    const { organizationId, userId } = this.owner
    try {
      const registry = await getAgentByName(
        this.env.RealtimeRegistryAgent,
        registryName(organizationId)
      )
      await registry.register(userId)
      this.setMeta("registered", "1")
    } catch (error) {
      console.error("inbox: could not register with its organization", error)
      if (!this.meta("registration_retry")) {
        const schedule = await this.schedule(
          registrationRetrySeconds,
          "retryRegistration"
        )
        this.setMeta("registration_retry", schedule.id)
      }
    }
  }

  private row(name: string): ConversationRow | undefined {
    return this.sql<ConversationRow>`SELECT c.*, (
        SELECT COUNT(*) FROM inbox_unread u WHERE u.name = c.name
      ) AS unread
      FROM inbox_conversations c WHERE c.name = ${name}`.at(0)
  }

  private listed(limit = -1): Conversation[] {
    return this.sql<ConversationRow>`SELECT c.*, (
        SELECT COUNT(*) FROM inbox_unread u WHERE u.name = c.name
      ) AS unread
      FROM inbox_conversations c
      ORDER BY c.updated_at DESC, c.name ASC LIMIT ${limit}`.map(conversationOf)
  }

  private unreadIn(name: string): number {
    return (
      this.sql<{
        count: number
      }>`SELECT COUNT(*) AS count FROM inbox_unread WHERE name = ${name}`.at(0)
        ?.count ?? 0
    )
  }

  private unreadTotal(): number {
    return (
      this.sql<{
        count: number
      }>`SELECT COUNT(*) AS count FROM inbox_unread`.at(0)?.count ?? 0
    )
  }

  /** Sends the member's sockets a message: those `onConnect` has let in. */
  private tell(message: InboxServerMessage): void {
    const data = JSON.stringify(message)
    for (const connection of this.getConnections()) {
      const state = connection.state as InboxConnectionState | null
      if (typeof state?.userId === "string") send(connection, data)
    }
  }

  /** Every conversation the member is in, listed or only known: what a purge empties. */
  private everyConversation(): Array<{
    name: string
    kind: ConversationKind
    peerId: string
  }> {
    return this.sql<{ name: string; kind: ConversationKind; peer_id: string }>`
      SELECT name, kind, peer_id FROM inbox_conversations
      UNION
      SELECT name, kind, peer_id FROM inbox_known`.map((row) => ({
      name: row.name,
      kind: row.kind,
      peerId: row.peer_id,
    }))
  }

  private meta(key: string): string | undefined {
    return this.sql<{
      value: string
    }>`SELECT value FROM inbox_meta WHERE key = ${key}`.at(0)?.value
  }

  private setMeta(key: string, value: string): void {
    this.sql`INSERT INTO inbox_meta (key, value) VALUES (${key}, ${value})
      ON CONFLICT (key) DO UPDATE SET value = excluded.value`
  }

  private deleteMeta(key: string): void {
    this.sql`DELETE FROM inbox_meta WHERE key = ${key}`
  }
}
