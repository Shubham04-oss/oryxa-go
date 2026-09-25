// The conversation events a DM and a side conversation send Go, for the
// platform's event bus (REALTIME.md, "Conversation events"): a message
// stored, and a member's read position advancing. Each object keeps an
// outbox in its SQLite, written in the same transaction as what an event is
// about, and drains it to `POST {ORYXA_API_ORIGIN}/v1/realtime/events`,
// signed with the secret Go signs its purges with. Go publishes what it
// takes as `oryxa.realtime` events. An event carries identities and
// positions, never what anybody wrote.
import { minPurgeSecretLength, realtimeSignature } from "./realtime-purge"

/** Where the objects send their events, on Go's origin. */
export const REALTIME_EVENTS_PATH = "/v1/realtime/events"

/** The event types Go takes from the Worker. */
export const realtimeEventTypes = {
  /** A stored, finished message: a person's, or an agent's final answer in a side conversation. */
  messagePosted: "realtime.message.posted.v1",
  /** A member's read position advanced. */
  conversationRead: "realtime.conversation.read.v1",
} as const

export type RealtimeEventType =
  (typeof realtimeEventTypes)[keyof typeof realtimeEventTypes]

/** The headers of a request of events: a purge's, signed differently. */
export const eventsRequestHeaders = {
  /** When the Worker signed it, in Unix seconds. */
  timestamp: "X-Oryxa-Timestamp",
  /** `signEvents`: never a purge's signature. */
  signature: "X-Oryxa-Signature",
} as const

/** The most one request carries, as Go takes it: more answers 413. */
export const eventsRequestLimits = { events: 100, bodyBytes: 256 * 1024 }

/** How the outbox drains. */
export const eventsTiming = {
  /**
   * The least time between two drains. The SDK keeps a schedule's time in
   * whole seconds, rounded down: a drain set sooner could land in the second
   * it ran in, run again at once, and spin.
   */
  minGapMs: 1_000,
  /** After a request Go did not answer 204, 400 or 413, the wait before the next: doubling with each such answer in a row, from this… */
  firstRetryMs: 1_000,
  /** …to this. */
  maxRetryMs: 5 * 60_000,
  /** How long an event waits for Go before it is dropped, and a refused one is kept. */
  retentionMs: 7 * 24 * 60 * 60_000,
  /** The longest one request may take. */
  requestTimeoutMs: 10_000,
}

/** The most of Go's answer a dead letter keeps, in characters. */
const maxAnswerCharacters = 2_000

/** A conversation as its events name it: its object's name, and a side conversation's teamspace. */
export type EventConversation =
  | { kind: "direct"; id: string }
  | { kind: "side"; id: string; teamspace_id: string }

/** Who wrote a message: a member, or an agent, whose `user_id` is its owner. */
export type EventAuthor =
  | { kind: "member"; user_id: string }
  | { kind: "agent"; user_id: string; agent_id: string }

/** `realtime.message.posted.v1`. */
export type MessagePosted = {
  organization_id: string
  conversation: EventConversation
  /** The conversation's two people, sorted. */
  participant_user_ids: [string, string]
  /** The object's id for the message, and its place in the conversation. */
  message: { id: string; seq: number }
  author: EventAuthor
  posted_at: string
}

/** `realtime.conversation.read.v1`. */
export type ConversationRead = {
  organization_id: string
  conversation: EventConversation
  reader_user_id: string
  read_through_seq: number
  read_at: string
}

/** What the events of one conversation share: its organization, itself, and its two people, sorted. */
export type EventScope = {
  organizationId: string
  conversation: EventConversation
  participants: [string, string]
}

/** A person as a message's author. */
export function memberAuthor(userId: string): EventAuthor {
  return { kind: "member", user_id: userId }
}

/** An agent as a message's author: its owner, and itself. */
export function agentAuthor(ownerId: string, agentId: string): EventAuthor {
  return { kind: "agent", user_id: ownerId, agent_id: agentId }
}

/** Where the events go and what signs them. */
export type EventsConfig = { url: URL; secret: string }

/**
 * Where the events go, from the Worker's environment: Go's origin and the
 * purge's secret, trimmed as Go trims it. Nothing when either is unset, when
 * the secret is shorter than a purge's (`minPurgeSecretLength`: Go takes
 * no shorter one), or when the origin is not a URL: then nothing is
 * recorded.
 */
export function realtimeEventsConfig(env: {
  ORYXA_API_ORIGIN?: unknown
  REALTIME_PURGE_SECRET?: unknown
}): EventsConfig | undefined {
  const secret =
    typeof env.REALTIME_PURGE_SECRET === "string"
      ? env.REALTIME_PURGE_SECRET.trim()
      : ""
  const origin =
    typeof env.ORYXA_API_ORIGIN === "string" ? env.ORYXA_API_ORIGIN.trim() : ""
  if (secret.length < minPurgeSecretLength || !origin) return undefined
  try {
    return { url: new URL(REALTIME_EVENTS_PATH, origin), secret }
  } catch {
    return undefined
  }
}

/**
 * The `X-Oryxa-Signature` of a request of events: `v1=` and the lowercase
 * hex HMAC-SHA256, keyed with the purge's secret, of
 * `events.{timestamp}.{body}`. The prefix keeps the two directions apart: a
 * purge signs `{timestamp}.{body}`, so neither ever verifies as the other.
 */
export function signEvents(
  secret: string,
  timestamp: string,
  body: string | Uint8Array
): Promise<string> {
  return realtimeSignature(secret, `events.${timestamp}`, body)
}

/** An event as the outbox holds it: its detail is JSON, sent byte for byte. */
export type StoredEvent = { id: string; type: string; detail: string }

/** One event of a request's body. */
function eventJson(event: StoredEvent): string {
  return `{"id":${JSON.stringify(event.id)},"type":${JSON.stringify(event.type)},"detail":${event.detail}}`
}

/** A request's body: `{"events":[…]}`, each detail as it was recorded. */
export function eventsBody(events: readonly StoredEvent[]): string {
  return `{"events":[${events.map(eventJson).join(",")}]}`
}

const encoder = new TextEncoder()

/** The first of `rows` whose body stays within Go's limit (at least one). */
function fitted<TRow extends StoredEvent>(rows: readonly TRow[]): TRow[] {
  let size = encoder.encode(eventsBody([])).length
  const kept: TRow[] = []
  for (const row of rows) {
    const bytes =
      encoder.encode(eventJson(row)).length + (kept.length > 0 ? 1 : 0)
    if (kept.length > 0 && size + bytes > eventsRequestLimits.bodyBytes) break
    kept.push(row)
    size += bytes
  }
  return kept
}

function randomId(): string {
  return crypto.randomUUID().replace(/-/g, "")
}

/** Why a request did not go through, in a log. */
function reasonOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

/**
 * Whether an answer is Go's own refusal of a request, which it never takes
 * later: 400 or 413 with Go's error body,
 * `{"error":{"code":"invalid_request",…}}`. The same status from anything
 * between the Worker and Go (a proxy's or a gateway's page) says nothing
 * about the events, which go again.
 */
function refusedByGo(answer: { status: number; text: string }): boolean {
  if (answer.status !== 400 && answer.status !== 413) return false
  try {
    const body = JSON.parse(answer.text) as {
      error?: { code?: unknown }
    } | null
    return body?.error?.code === "invalid_request"
  } catch {
    return false
  }
}

/** A value the SDK's `sql` binds. */
export type SqlValue = string | number | boolean | null

/** An event waiting in the outbox, at its place in it. */
type OutboxRow = StoredEvent & { position: number; created_at: number }

/**
 * Go not taking the outbox's requests: how many in a row it did not take,
 * and when the next may go.
 */
type Backoff = { failures: number; until: number }

/** How many rows a prune reads at a time, from the head. */
const pruneChunk = 100

/** The rows at the head of `head` that `expired` holds for, up to the first it does not. */
function expiredHead<TRow>(
  head: readonly TRow[],
  expired: (row: TRow) => boolean
): TRow[] {
  const kept = head.findIndex((row) => !expired(row))
  return kept === -1 ? [...head] : head.slice(0, kept)
}

/** What an object lends its outbox: its conversation, its storage, its schedule and the Worker's settings. */
export type EventOutboxHost = {
  /** Who logs: "direct message", "side conversation". */
  label: string
  /** The object's name. */
  name: () => string
  /** The conversation the events are about. */
  scope: () => EventScope
  /** Where the events go: nothing when they are off. */
  config: () => EventsConfig | undefined
  sql: <T = Record<string, SqlValue>>(
    strings: TemplateStringsArray,
    ...values: SqlValue[]
  ) => T[]
  /** Runs `work` as one SQLite transaction: all it writes, or none of it when it throws. */
  transaction: <T>(work: () => T) => T
  /** Sets the object's drain (its `drainEvents`) for `at`. */
  schedule: (at: Date) => Promise<{ id: string }>
  cancelSchedule: (id: string) => Promise<boolean>
}

/**
 * A conversation's outbox of events, in its object's SQLite:
 *
 * - `event_outbox`: each event waiting for Go (`id`, `type`, `detail`,
 *   `created_at`), in the order they were recorded (`position`), with
 *   `reader_id` on a read so each reader has one at most;
 * - `event_dead_letters`: what Go refused (`refusedByGo`), with its
 *   answer, for seven days;
 * - `event_meta`: Go's wait (`backoff`: the requests in a row Go did not
 *   take, and when the next may go), the one value a retry writes; the
 *   drain that is set, and when the last one ran.
 *
 * Whatever waits, recording an event and a request Go did not take each
 * write a fixed few rows, and nothing reads more of the outbox than its
 * head: its first rows by `position`, and the first dead letter by rowid.
 *
 * An object records an event in the transaction that stores what it is
 * about, then `plan`s; the drain (`drain`, the object's `drainEvents`
 * schedule) sends what waits, oldest first, at most
 * `eventsRequestLimits.events` a request. Nothing is recorded while
 * `realtimeEventsConfig` says the events are off.
 */
export class EventOutbox {
  private schemaReady = false
  /** Counts purges: a drain that began before one sends nothing. */
  private epoch = 0
  /** The drain under way. */
  private draining: Promise<void> | undefined
  /** Plans one at a time, so two never set two drains. */
  private planning: Promise<void> = Promise.resolve()

  constructor(private readonly host: EventOutboxHost) {}

  /**
   * Records that a message was stored, in the transaction that stores it:
   * a person's message, or an agent's final answer. Nothing when the
   * events are off, or for an agent without an id, which Go would refuse
   * with the rest of its request.
   */
  recordPosted(
    message: { id: string; seq: number },
    author: EventAuthor,
    postedAt: string
  ): void {
    if (!this.host.config()) return
    if (author.kind === "agent" && !author.agent_id) {
      console.error(
        `${this.host.label}: an agent's message has no agent id; no event is recorded for it`
      )
      return
    }
    const scope = this.host.scope()
    const detail: MessagePosted = {
      organization_id: scope.organizationId,
      conversation: scope.conversation,
      participant_user_ids: scope.participants,
      message: { id: message.id, seq: message.seq },
      author,
      posted_at: postedAt,
    }
    this.insert(realtimeEventTypes.messagePosted, detail)
  }

  /**
   * Records that `reader` read through `seq`, in the transaction that
   * advances their read, in place of any read of theirs not yet sent: only
   * the newest goes. Nothing when the events are off.
   */
  recordRead(reader: string, seq: number, readAt: string): void {
    if (!this.host.config()) return
    const scope = this.host.scope()
    const detail: ConversationRead = {
      organization_id: scope.organizationId,
      conversation: scope.conversation,
      reader_user_id: reader,
      read_through_seq: seq,
      read_at: readAt,
    }
    this.insert(realtimeEventTypes.conversationRead, detail, reader)
  }

  /**
   * Makes sure a drain is set for what waits: when the oldest event may go
   * (or the oldest dead letter is due to be let go), never under
   * `eventsTiming.minGapMs` after the last drain, in a whole second. One
   * drain is set at a time: an event recorded while one is set goes with
   * it. Never throws; a drain that could not be set is set by the next
   * event or restart.
   */
  plan(): Promise<void> {
    const next = this.planning.then(() => this.planOnce())
    this.planning = next.catch((error: unknown) => {
      console.error(
        `${this.host.label}: could not set the events' drain`,
        error
      )
    })
    return this.planning
  }

  /**
   * The drain, from the object's `drainEvents` schedule: drops what waited
   * or was refused longer than `eventsTiming.retentionMs`, sends up to
   * `eventsRequestLimits.events` of what may go, signed (`signEvents`), and
   * acts on Go's answer:
   *
   * - 204: every event was taken, and leaves the outbox;
   * - Go's refusal (`refusedByGo`: 400 or 413 with Go's own error): Go
   *   takes none of it, then or later: its events move to the dead
   *   letters, with the answer;
   * - anything else (a 400 or 413 without Go's error too), or no answer:
   *   the outbox waits, from
   *   `eventsTiming.firstRetryMs`, doubling with each such answer in a row,
   *   to `eventsTiming.maxRetryMs`.
   *
   * Then it sets the next drain for what is left. Never throws: the SDK
   * would run it again at once.
   */
  async drain(schedule?: { id: string }): Promise<void> {
    try {
      this.schema()
      // The drain set is this one: it is no longer set. Any other stays set,
      // and the next plan keeps it or moves it.
      if (schedule && this.meta("drain") === schedule.id) {
        this.deleteMeta("drain")
        this.deleteMeta("drain_at")
      }
      // One at a time: the drain under way sets the next as it ends.
      if (this.draining) return
      const run = this.drainOnce()
      this.draining = run
      try {
        await run
      } finally {
        this.draining = undefined
      }
    } catch (error) {
      console.error(`${this.host.label}: the events' drain failed`, error)
    }
    await this.plan()
  }

  /**
   * The conversation's purge: every event waiting and every dead letter
   * goes before this returns, and nothing recorded before it is ever sent.
   * A request already on its way was sent before the purge: the promise
   * resolves once Go has answered it (or it failed), so the purge ends only
   * after anything about what went has reached Go, never later.
   */
  purge(): Promise<void> {
    this.schema()
    this.epoch += 1
    const planned = this.meta("drain")
    this.host.transaction(() => {
      this.host.sql`DELETE FROM event_outbox`
      this.host.sql`DELETE FROM event_dead_letters`
      this.deleteMeta("drain")
      this.deleteMeta("drain_at")
      this.endBackoff()
    })
    const draining = this.draining
    return (async () => {
      if (planned) await this.host.cancelSchedule(planned).catch(() => false)
      await draining?.catch(() => undefined)
    })()
  }

  private schema(): void {
    if (this.schemaReady) return
    this.host.transaction(() => {
      const earlier = this.setAsideEarlierTables()
      // `position` is the order events go in. Only a read has a
      // `reader_id`, so only reads are in its index. The ids are not
      // indexed: each is random, and a row goes by its position.
      this.host.sql`CREATE TABLE IF NOT EXISTS event_outbox (
        position INTEGER PRIMARY KEY,
        id TEXT NOT NULL,
        type TEXT NOT NULL,
        detail TEXT NOT NULL,
        reader_id TEXT,
        created_at INTEGER NOT NULL
      )`
      this.host.sql`CREATE UNIQUE INDEX IF NOT EXISTS event_outbox_by_reader
        ON event_outbox (reader_id) WHERE reader_id IS NOT NULL`
      // In the order they were refused, by rowid.
      this.host.sql`CREATE TABLE IF NOT EXISTS event_dead_letters (
        id TEXT PRIMARY KEY,
        type TEXT NOT NULL,
        detail TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        status INTEGER NOT NULL,
        answer TEXT NOT NULL,
        dead_at INTEGER NOT NULL
      )`
      this.host.sql`CREATE TABLE IF NOT EXISTS event_meta (
        key TEXT PRIMARY KEY,
        value TEXT NOT NULL
      )`
      if (earlier.outbox) {
        this.host.sql`INSERT INTO event_outbox
            (position, id, type, detail, reader_id, created_at)
          SELECT position, id, type, detail, reader_id, created_at
          FROM event_outbox_earlier`
        this.host.sql`DROP TABLE event_outbox_earlier`
      }
      if (earlier.deadLetters) {
        this.host.sql`INSERT INTO event_dead_letters
            (id, type, detail, created_at, status, answer, dead_at)
          SELECT id, type, detail, created_at, status, answer, dead_at
          FROM event_dead_letters_earlier ORDER BY rowid`
        this.host.sql`DROP TABLE event_dead_letters_earlier`
      }
    })
    this.schemaReady = true
  }

  /**
   * An earlier build kept a wait and a count of tries on every event (and
   * indexed the ids): its tables are set aside, to be made again as they
   * are now with what they held.
   */
  private setAsideEarlierTables(): { outbox: boolean; deadLetters: boolean } {
    const outbox = this.host.sql<{
      name: string
    }>`PRAGMA table_info(event_outbox)`.some(
      (column) => column.name === "next_at"
    )
    if (outbox) {
      this.host.sql`ALTER TABLE event_outbox RENAME TO event_outbox_earlier`
    }
    const deadLetters = this.host.sql<{
      name: string
    }>`PRAGMA table_info(event_dead_letters)`.some(
      (column) => column.name === "attempts"
    )
    if (deadLetters) {
      this.host
        .sql`ALTER TABLE event_dead_letters RENAME TO event_dead_letters_earlier`
    }
    return { outbox, deadLetters }
  }

  private insert(
    type: RealtimeEventType,
    detail: MessagePosted | ConversationRead,
    readerId?: string
  ): void {
    this.schema()
    // A newer read takes the place of the reader's one not yet sent, under a
    // new id: an answer to a request carrying the old one never settles it.
    if (readerId !== undefined) {
      this.host.sql`DELETE FROM event_outbox WHERE reader_id = ${readerId}`
    }
    // At the tail: while Go is waited for, it waits with the rest.
    this.host.sql`INSERT INTO event_outbox
        (id, type, detail, reader_id, created_at)
      VALUES (${`evt_${randomId()}`}, ${type}, ${JSON.stringify(detail)},
        ${readerId ?? null}, ${Date.now()})`
  }

  private async planOnce(): Promise<void> {
    this.schema()
    // The drain under way sets the next as it ends.
    if (this.draining) return
    const at = this.nextDrainAt()
    if (at === undefined) return
    const planned = this.meta("drain")
    const plannedAt = Number(this.meta("drain_at") ?? "0")
    // One as early is set already (and not long overdue, which would mean
    // it is lost).
    if (planned && plannedAt <= at && plannedAt > Date.now() - 60_000) return
    if (planned) await this.host.cancelSchedule(planned).catch(() => false)
    const schedule = await this.host.schedule(new Date(at))
    this.setMeta("drain", schedule.id)
    this.setMeta("drain_at", String(at))
  }

  /** When the next drain is due, in a whole second, or nothing when nothing waits. */
  private nextDrainAt(): number | undefined {
    const due: number[] = []
    // The outbox goes whole, oldest first, once Go may be asked again.
    if (this.host.sql`SELECT 1 FROM event_outbox LIMIT 1`.length > 0) {
      due.push(Math.max(Date.now(), this.backoff()?.until ?? 0))
    }
    // The oldest dead letter is let go seven days after it was refused.
    const refused = this.host.sql<{
      dead_at: number
    }>`SELECT dead_at FROM event_dead_letters ORDER BY rowid LIMIT 1`.at(0)
    if (refused) due.push(refused.dead_at + eventsTiming.retentionMs)
    if (due.length === 0) return undefined
    const last = Number(this.meta("last_drain_at") ?? "0")
    const at = Math.max(Math.min(...due), last + eventsTiming.minGapMs)
    // The SDK rounds a schedule's time down to the second: rounded up here,
    // the drain is never early.
    return Math.ceil(at / 1000) * 1000
  }

  private async drainOnce(): Promise<void> {
    const now = Date.now()
    const last = Number(this.meta("last_drain_at") ?? "0")
    if (now < last + eventsTiming.minGapMs) return
    this.setMeta("last_drain_at", String(now))
    this.prune(now)
    // Go is waited for: nothing goes before then.
    if (now < (this.backoff()?.until ?? 0)) return
    const due = this.host.sql<OutboxRow>`SELECT position, id, type, detail,
        created_at
      FROM event_outbox ORDER BY position LIMIT ${eventsRequestLimits.events}`
    if (due.length === 0) return
    const config = this.host.config()
    if (!config) {
      this.retryLater(due.length, now, "the events are not configured")
      return
    }
    const batch = fitted(due)
    const body = eventsBody(batch)
    const timestamp = String(Math.floor(now / 1000))
    const epoch = this.epoch
    const signature = await signEvents(config.secret, timestamp, body)
    // Purged meanwhile: nothing about it goes.
    if (this.epoch !== epoch) return
    // Given up on after `requestTimeoutMs`, its answer's body included.
    const controller = new AbortController()
    const timeout = setTimeout(() => {
      controller.abort(
        new DOMException(
          `Go did not answer within ${eventsTiming.requestTimeoutMs / 1000} s`,
          "TimeoutError"
        )
      )
    }, eventsTiming.requestTimeoutMs)
    let answer: { status: number; text: string }
    try {
      const response = await fetch(config.url, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          [eventsRequestHeaders.timestamp]: timestamp,
          [eventsRequestHeaders.signature]: signature,
        },
        body,
        // A redirect is not Go's answer, wherever it points.
        redirect: "manual",
        signal: controller.signal,
      })
      answer = {
        status: response.status,
        text: response.status === 204 ? "" : await response.text(),
      }
    } catch (error) {
      if (this.epoch === epoch) {
        this.retryLater(batch.length, Date.now(), error)
      }
      return
    } finally {
      clearTimeout(timeout)
    }
    if (this.epoch !== epoch) return
    if (answer.status === 204) {
      this.host.transaction(() => {
        for (const row of batch) this.remove(row)
        this.endBackoff()
      })
      return
    }
    if (refusedByGo(answer)) {
      this.deadLetter(batch, answer, Date.now())
      return
    }
    this.retryLater(
      batch.length,
      Date.now(),
      answer.status === 400 || answer.status === 413
        ? `answered ${answer.status}, but not as Go refuses a request`
        : `Go answered ${answer.status}`
    )
  }

  /**
   * Drops what waited longer than `eventsTiming.retentionMs`, and dead
   * letters as old. Each table is in the order it was written, so what is
   * past its time is at its head: nothing reads past the first row that is
   * not.
   */
  private prune(now: number): void {
    const cutoff = now - eventsTiming.retentionMs
    const expired: string[] = []
    for (;;) {
      const head = this.host.sql<{
        position: number
        id: string
        created_at: number
      }>`SELECT position, id, created_at FROM event_outbox
        ORDER BY position LIMIT ${pruneChunk}`
      const old = expiredHead(head, (row) => row.created_at <= cutoff)
      if (old.length === 0) break
      this.host.sql`DELETE FROM event_outbox
        WHERE position <= ${old[old.length - 1].position}`
      expired.push(...old.map((row) => row.id))
      if (old.length < head.length) break
    }
    if (expired.length > 0) {
      console.error(
        `${this.host.label}: dropped ${expired.length} events Go did not take in 7 days`,
        { conversation: this.host.name(), ids: expired }
      )
    }
    for (;;) {
      const head = this.host.sql<{
        row: number
        dead_at: number
      }>`SELECT rowid AS row, dead_at FROM event_dead_letters
        ORDER BY rowid LIMIT ${pruneChunk}`
      const old = expiredHead(head, (row) => row.dead_at <= cutoff)
      if (old.length === 0) break
      this.host.sql`DELETE FROM event_dead_letters
        WHERE rowid <= ${old[old.length - 1].row}`
      if (old.length < head.length) break
    }
  }

  /** A row the outbox sent: gone, unless a newer read took its place (and position) meanwhile. */
  private remove(row: OutboxRow): void {
    this.host.sql`DELETE FROM event_outbox
      WHERE position = ${row.position} AND id = ${row.id}`
  }

  /** Go refused the request: its events move to the dead letters, with the answer. */
  private deadLetter(
    batch: readonly OutboxRow[],
    answer: { status: number; text: string },
    at: number
  ): void {
    const text = answer.text.slice(0, maxAnswerCharacters)
    this.host.transaction(() => {
      for (const row of batch) {
        this.host.sql`INSERT OR REPLACE INTO event_dead_letters
            (id, type, detail, created_at, status, answer, dead_at)
          VALUES (${row.id}, ${row.type}, ${row.detail}, ${row.created_at},
            ${answer.status}, ${text}, ${at})`
        this.remove(row)
      }
      // Go answered: it is waited for no more.
      this.endBackoff()
    })
    console.error(
      `${this.host.label}: Go refused ${batch.length} events (${answer.status}); they are kept 7 days as dead letters`,
      {
        conversation: this.host.name(),
        ids: batch.map((row) => row.id),
        answer: text,
      }
    )
  }

  /**
   * Go did not take the request: the whole outbox waits (what was recorded
   * since goes after, together), doubling with each such request in a row.
   * One value says so, however much waits.
   */
  private retryLater(count: number, at: number, reason: unknown): void {
    const failures = (this.backoff()?.failures ?? 0) + 1
    const wait = Math.min(
      eventsTiming.firstRetryMs * 2 ** Math.min(failures - 1, 30),
      eventsTiming.maxRetryMs
    )
    this.setMeta(
      "backoff",
      JSON.stringify({ failures, until: at + wait } satisfies Backoff)
    )
    console.error(
      `${this.host.label}: Go did not take ${count} events (${reasonOf(reason)}); trying again in ${wait / 1000} s`,
      { conversation: this.host.name() }
    )
  }

  /** Go's wait, while there is one. */
  private backoff(): Backoff | undefined {
    const held = this.meta("backoff")
    if (held === undefined) return undefined
    try {
      const backoff = JSON.parse(held) as Partial<Backoff>
      return {
        failures: Number(backoff.failures) || 0,
        until: Number(backoff.until) || 0,
      }
    } catch {
      return undefined
    }
  }

  /** Go answered: the next request goes without waiting. */
  private endBackoff(): void {
    this.deleteMeta("backoff")
  }

  private meta(key: string): string | undefined {
    return this.host.sql<{
      value: string
    }>`SELECT value FROM event_meta WHERE key = ${key}`.at(0)?.value
  }

  private setMeta(key: string, value: string): void {
    this.host.sql`INSERT INTO event_meta (key, value) VALUES (${key}, ${value})
      ON CONFLICT (key) DO UPDATE SET value = excluded.value`
  }

  private deleteMeta(key: string): void {
    this.host.sql`DELETE FROM event_meta WHERE key = ${key}`
  }
}
