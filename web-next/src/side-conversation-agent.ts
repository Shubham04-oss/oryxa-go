import { Agent, getAgentByName } from "agents"
import type { Connection, ConnectionContext, Schedule, WSMessage } from "agents"
import { primaryRefusals, relayGoRun } from "./chat-relay"
import type { RelayWords } from "./chat-relay"
import type { ConversationEnv } from "./conversation-env"
import {
  dmInboxRetry,
  dmSendLimit,
  dmTypingRetellMs,
} from "./direct-message-agent"
import type {
  ConversationEntry,
  NotedMessage,
  RestatedConversation,
} from "./inbox-agent"
import { ApiError, requestError } from "./lib/api/client"
import { organizationPath, withSearch } from "./lib/agents/http"
import { inboxName } from "./lib/dms/names"
import { MAX_UNREAD } from "./lib/dms/protocol"
import {
  PROMPT_TRANSCRIPT_LINES,
  agentRequestBody,
  cardFields,
  clip,
  heldAnswerOf,
  isOpen,
  ownAgentPrompt,
  personName,
  resultText,
  statusLineText,
} from "./lib/side/asking"
import type { CardFields, TranscriptLine } from "./lib/side/asking"
import { parseSideConversationName, peerIn } from "./lib/side/names"
import type { SideConversationIds } from "./lib/side/names"
import {
  SIDE_HISTORY_LIMIT,
  SIDE_MAX_AGENT_TEXT,
  SIDE_MAX_BODY_LENGTH,
  SIDE_MAX_DECISION_MESSAGE,
  SIDE_PAGE_SIZE,
  SIDE_TYPING_TTL_MS,
  codePoints,
  decodeRoster,
  emptyRoster,
  parseRoster,
  parseSideClientMessage,
  preview,
  sideGoneCloseCode,
} from "./lib/side/protocol"
import type {
  SideAgentMessageStatus,
  SideConfirmation,
  SideFailure,
  SideHeldAnswer,
  SideOutcome,
  SideRead,
  SideRequestStatus,
  SideReviewAction,
  SideRoster,
  SideServerMessage,
  SideVia,
  SideWireItem,
  SideWireRequestCard,
} from "./lib/side/protocol"
import { agentsOf, rosterOf } from "./lib/side/roster"
import { sessionExpiredCode } from "./lib/threads/chat-messages"
import type { Item, RunInput } from "./lib/threads/client"
import {
  EventOutbox,
  agentAuthor,
  memberAuthor,
  realtimeEventsConfig,
} from "./realtime-events"
import type { SqlValue } from "./realtime-events"
import { isPurgeInstant } from "./realtime-registry-agent"
import type { ThreadChatEnv } from "./thread-chat-agent"

/** What the side conversation reads from the Worker's environment: Go's origin, and the other conversation objects. */
export interface SideConversationEnv extends ConversationEnv, ThreadChatEnv {}

/**
 * The headers the Worker sets on a request it routes to a side
 * conversation, replacing any `X-Oryxa-*` header the browser sent, once Go
 * has said who the member is, that they are on the teamspace's team, and
 * that the other person is too.
 */
export const sideRequestHeaders = {
  organization: "X-Oryxa-Organization",
  /** The member's user id, as Go answered the Worker for their cookie. */
  user: "X-Oryxa-User",
  /** Their cookie: the object asks Go as them, for their agent, requests and decisions. */
  cookie: "X-Oryxa-Cookie",
  /** Who is in the conversation, as the Worker read the teamspace (`encodeRoster`). */
  roster: "X-Oryxa-Side-Roster",
} as const

/**
 * What a socket keeps, in its attachment (so hibernation keeps it): who it
 * is, as the Worker determined it, the cookie and origin it asks Go with,
 * whether its tab is visible, and when it opened.
 */
export type SideConnectionState = {
  userId: string
  cookie: string
  /** The page's origin: Go takes a cookie-authenticated change only from an allowed one. */
  origin: string
  /** False until the page says. */
  visible: boolean
  /** When the Worker let it in, as `toISOString` writes it: a purge ends the sockets from before a member left. */
  connectedAt: string
  /** Go answered 401 for its cookie, which is never used again: the page reopens with a fresh one. */
  expired?: boolean
}

/** A purge for one of the two, who left the organization at `removedAt` (a `PurgeInstant`). */
export type SidePurgeScope = { userId: string; removedAt: string }

/** How often, and how long, the object works with Go. */
export const sideTiming = {
  /** While a request is open (pending or approved): how often Go is read for it, in seconds. */
  pollSeconds: 3,
  /** A socket that opens reads Go's requests, unless they were read this recently. */
  catchUpMs: 15_000,
  /** The longest an agent's answer may take. */
  runTimeoutMs: 5 * 60_000,
  /** The longest a request, a decision or a read may take. */
  requestTimeoutMs: 30_000,
  /**
   * A request an agent filed is shown here when it was made after the
   * conversation first opened, less this much, for the two clocks.
   */
  adoptionSlackMs: 60_000,
}

/** The most requests one person may have waiting on a decision here at once. */
export const maxOpenRequestsPerPerson = 5

/** The most items one inbox update carries: an inbox that missed more is told the latest. */
const maxNotedPerUpdate = 200

/** How many requests a read of Go's listing asks for. */
const listedRequests = 50

// The most one snapshot or history page carries, in characters of JSON.
const maxPageCharacters = 256 * 1024

type ItemKind = SideWireItem["kind"]

type ItemRow = {
  seq: number
  id: string
  kind: ItemKind
  /** Whose side it is: a message's author, an agent's owner, a request's requester, a status line's target owner. */
  author_id: string
  client_id: string | null
  request_id: string | null
  /** An agent message's status, a card's request status, a status line's outcome. */
  state: string | null
  body: string
  /** Each kind's own fields, as JSON. */
  data: string
  created_at: string
  updated_at: string
  /** When the socket it came through opened; none for what Go's requests brought. */
  session_at: string | null
  /** 1 once it may be told to the inboxes (an agent's message once its run ends). */
  settled: number
  /** Which inboxes were told of it: 1 the first person's, 2 the second's. */
  noted: number
}

type MessageData = { asksAgentId?: string }

type AgentData = {
  agentId: string
  agentName: string
  error?: string
  code?: string
  replyTo?: number
  via?: SideVia
  confirmations?: SideConfirmation[]
}

/**
 * What a card keeps of Go's request beside its columns. A held answer is
 * never among it: it stays in memory (`heldAnswers`).
 */
type CardData = Omit<
  SideWireRequestCard,
  | "kind"
  | "id"
  | "seq"
  | "requestId"
  | "requesterId"
  | "status"
  | "task"
  | "createdAt"
  | "updatedAt"
  | "clientId"
  | "heldAnswer"
> & {
  /** Go's revision, to tell a change. */
  revision?: number
  /** Go's `created_at`. */
  goCreatedAt: string
  /** The item its end left: its result, or its status line. */
  outcomeSeq?: number
}

type StatusData = {
  outcome: SideOutcome
  /** The agent the request asked: an inbox counts the line as its. */
  agentId: string
}

/** An agent's answer as its run streams it, in memory. */
type Draft = {
  /** The finished messages of the run (and, continued, what it said before). */
  finished: string[]
  /** The next message, as its deltas arrive. */
  draft: string
  confirmations: SideConfirmation[]
  failure?: { error: string; code?: string }
  /** A message stopped without text, in the model's words. */
  stopped?: string
  /** Its body reached `SIDE_MAX_AGENT_TEXT`: what else arrives is not kept. */
  full: boolean
}

/** Somebody typing: until `timer` fires, and when the other person was last told. In memory only. */
type Typing = { timer: ReturnType<typeof setTimeout>; told: number }

/** A socket and what it keeps, when the object needs to act as its person. */
type Actor = { connection: Connection; state: SideConnectionState }

const refusals: Partial<Record<SideFailure, string>> = {
  empty: "Write something to send.",
  too_long: `A message holds at most ${SIDE_MAX_BODY_LENGTH.toLocaleString("en-US")} characters.`,
  rate_limited: "You're sending messages too quickly. Wait a moment.",
  unavailable: "That couldn't be done just now. Try again.",
  session_expired: "Your session has ended. Sign in again to continue.",
}

/** The words written on an agent's message whose run did not finish; both people read them. */
const runWords: RelayWords = {
  refusals: {
    ...primaryRefusals,
    run_in_progress: "The agent is still answering. Ask again once it has.",
    confirmation_pending:
      "The agent waits for a confirmation. Answer it to continue.",
    agents_unavailable: "The agent isn't available right now.",
    not_found: "The agent's conversation is gone. Ask again.",
  },
  cutOff: "The answer was cut off before it finished.",
  unreachable: "The agent could not be reached. Try again.",
}

const sessionEndedWords = "The session had ended before the agent could answer."

/** Why Send masked is refused for an answer the guard could not check. */
const noMaskedWords =
  "This answer couldn't be checked, so it has no masked version. Send it as it is, or don't send it."

type Refusal = { code: SideFailure; message: string }

/** Go's refusal of an owner's decision, in words. */
function decisionRefusal(error: ApiError): Refusal {
  return error.status === 409
    ? {
        code: "already_decided",
        message: "This request was decided already, or expired.",
      }
    : {
        code: "not_allowed",
        message: error.message || "The decision was refused.",
      }
}

/**
 * Go's refusal of an owner's review, in words: an answer reviewed already
 * (or one without the masked version asked for, while still in review), a
 * request past its time, a choice Go does not take, and a request that is
 * not theirs to review or not there.
 */
function reviewRefusal(
  error: ApiError,
  action: unknown,
  stillInReview: boolean
): Refusal {
  if (error.status === 409 && error.code === "request_expired") {
    return {
      code: "already_decided",
      message: "This request expired before its answer was reviewed.",
    }
  }
  if (error.status === 409) {
    return action === "send_masked" && stillInReview
      ? { code: "not_allowed", message: noMaskedWords }
      : {
          code: "already_decided",
          message: "This answer was reviewed already, or expired.",
        }
  }
  switch (error.status) {
    case 400:
      return {
        code: "not_allowed",
        message: "That review wasn't understood. Try again.",
      }
    case 403:
      return {
        code: "not_allowed",
        message: "Only the person whose agent answered can review its answer.",
      }
    case 404:
      return {
        code: "not_allowed",
        message: "This request can't be found any more.",
      }
    default:
      return {
        code: "not_allowed",
        message: error.message || "The review was refused.",
      }
  }
}

/** A request's state as an agent reads it in the transcript, where Go's word would not say it. */
const requestStateWords: Partial<Record<string, string>> = {
  review: "waiting for its owner to review the answer",
}

/** Whether two lists hold the same names in the same order; none is an empty list. */
function sameList(
  left: readonly string[] | undefined,
  right: readonly string[] | undefined
): boolean {
  const a = left ?? []
  const b = right ?? []
  return a.length === b.length && a.every((item, index) => item === b[index])
}

function stateOf(connection: Connection): SideConnectionState | undefined {
  const state = connection.state as SideConnectionState | null
  return state &&
    typeof state.userId === "string" &&
    typeof state.cookie === "string"
    ? state
    : undefined
}

/** Changes a socket's state; nothing when it has none or closed meanwhile. */
function update(
  connection: Connection,
  change: Partial<SideConnectionState>
): SideConnectionState | undefined {
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

function send(connection: Connection, message: SideServerMessage | string) {
  try {
    connection.send(
      typeof message === "string" ? message : JSON.stringify(message)
    )
  } catch {
    // It closed meanwhile.
  }
}

function randomId(): string {
  return crypto.randomUUID().replace(/-/g, "")
}

function parseData<T>(row: ItemRow): T {
  try {
    return JSON.parse(row.data) as T
  } catch {
    return {} as T
  }
}

/** The body of a streaming answer so far: its finished messages, then the one being written. */
function bodyOf(draft: Draft): string {
  return [...draft.finished, draft.draft]
    .filter((part) => part !== "")
    .join("\n\n")
}

/** Whether Go refused a cookie: the page must renew its session. */
function isExpiry(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401
}

/**
 * A teamspace's side conversation: two teammates and both their agents,
 * about work in that teamspace, away from its room. One object per pair and
 * teamspace, named `sideConversationName(org, teamspace, userA, userB)`,
 * both people's sockets on it (every view opens its own).
 *
 * - **People's messages** live only here, in SQLite, as a DM's do, with a
 *   DM's semantics: sent by the socket's own person, stored once per
 *   author and page id, rate-limited, paged, read, typed and present.
 * - **A person's own agent** answers when they ask it (`side-ask-mine`):
 *   the object runs their primary agent as them, on a Go thread of theirs
 *   kept for this conversation, with the conversation framed as untrusted
 *   data, and streams the answer to both people as an agent's message. A
 *   confirmation the run asks for goes to its owner alone, who answers it.
 * - **A teammate's agent** is asked through Go's agent requests
 *   (`side-ask-teammate`), filed as the asker in this teamspace. The card
 *   mirrors the request; its owner approves or declines one held for them
 *   from their own socket, as themself. While a request is open and anybody
 *   is here, the object reads Go every few seconds on the SDK's schedule;
 *   the result arrives as the teammate's agent's message, and a decline, a
 *   failure or an expiry as a quiet line. Requests an agent of either filed
 *   in this teamspace since the conversation began show here too.
 * - **Inboxes.** Both people's inboxes list it (`kind: "side"`, with its
 *   teamspace); every visible item counts, an agent's message once its run
 *   ends.
 * - **Events.** Each person's message, each agent's final answer and each
 *   read that goes further is recorded for Go in the same transaction
 *   (src/realtime-events.ts), and sent on the `drainEvents` schedule; never
 *   an answer's deltas, a request card, a quiet line or what anybody wrote.
 *   A person's message (a question too) reads through itself with no
 *   event of its own; a request card, which is none, reads through itself
 *   with one.
 *
 * Only its two people reach it: the Worker routes a socket only once Go has
 * said who the member is, that they are on the teamspace's team, and that
 * the other person is too.
 */
export class SideConversationAgent extends Agent<SideConversationEnv> {
  private readonly sendBuckets = new Map<
    string,
    { tokens: number; at: number }
  >()
  private readonly typing = new Map<string, Typing>()
  /** The agent runs in flight, by their owner: one each at a time. */
  private readonly runs = new Map<
    string,
    { seq: number; controller: AbortController }
  >()
  /** The answers being streamed, by seq. */
  private readonly drafts = new Map<number, Draft>()
  /** Requests being filed, and decisions and reviews being sent, so none goes twice at once. */
  private readonly inFlight = new Set<string>()
  /**
   * The answers the answer guard holds for their owner's review, by
   * request, as Go showed them to that owner: in memory only, never stored,
   * and sent to the owner's sockets alone. An instance without one reads it
   * again as its owner while they are here (`readHeldAnswers`).
   */
  private readonly heldAnswers = new Map<string, SideHeldAnswer>()
  /** When each held answer was last read as its owner, so a Go that shows none is not asked on every poll. */
  private readonly heldReads = new Map<string, number>()
  /** Counts purges in this instance: work that began before one stops. */
  private epoch = 0
  private schemaReady = false
  private syncing: Promise<void> | undefined
  private syncAgain = false
  private reading: Promise<void> | undefined
  private lastRead = 0
  /** What Go is told of the conversation: messages and reads, no content. */
  private readonly events = new EventOutbox({
    label: "side conversation",
    name: () => this.name,
    scope: () => {
      const { organizationId, teamspaceId, userA, userB } = this.ids
      return {
        organizationId,
        conversation: {
          kind: "side",
          id: this.name,
          teamspace_id: teamspaceId,
        },
        participants: [userA, userB],
      }
    },
    config: () => realtimeEventsConfig(this.env),
    sql: <T>(strings: TemplateStringsArray, ...values: SqlValue[]) =>
      this.sql<T>(strings, ...values),
    transaction: (work) => this.transaction(work),
    schedule: (at) => this.schedule(at, "drainEvents"),
    drains: async () =>
      (await this.listSchedules()).filter(
        (schedule) => schedule.callback === "drainEvents"
      ),
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
      throw new Error("A side conversation has no state a page may set.")
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

  // After a restart: a run the instance lost is never replayed (it may have
  // called tools), an inbox never told is told, open requests are read, and
  // the events still waiting go.
  override async onStart(): Promise<void> {
    this.schema()
    const interrupted = this.sql<ItemRow>`SELECT * FROM side_items
      WHERE kind = 'agent-message' AND state = 'streaming'`
    for (const row of interrupted) {
      const data = parseData<AgentData>(row)
      this.rewrite(row, {
        state: "failed",
        settled: 1,
        data: { ...data, error: runWords.cutOff },
      })
    }
    if (this.syncPending()) await this.armRetry()
    await this.planPoll()
    await this.events.plan()
  }

  override async onConnect(connection: Connection, context: ConnectionContext) {
    const { headers } = context.request
    const ids = parseSideConversationName(this.name)
    const userId = headers.get(sideRequestHeaders.user)
    const cookie = headers.get(sideRequestHeaders.cookie)
    const roster = decodeRoster(headers.get(sideRequestHeaders.roster))
    if (
      !ids ||
      !userId ||
      !cookie ||
      !roster ||
      headers.get(sideRequestHeaders.organization) !== ids.organizationId ||
      !peerIn(ids, userId)
    ) {
      connection.close(1008, "Unauthorized")
      return
    }
    this.schema()
    // Who is in it, as Go just said: the sockets already here are told
    // when that changed, and this one is sent it with the conversation.
    this.setRoster(roster)
    connection.setState({
      userId,
      cookie,
      origin: headers.get("Origin") ?? "",
      visible: false,
      connectedAt: new Date().toISOString(),
    } satisfies SideConnectionState)
    send(connection, this.snapshot(userId))
    // The conversation is its people's from the moment it opens: both
    // inboxes learn it exists, so purging either person finds it.
    if (this.meta("opened") !== "1") this.setMeta("opened", "1")
    if (!this.meta("adopt_after")) {
      this.setMeta(
        "adopt_after",
        String(Date.now() - sideTiming.adoptionSlackMs)
      )
    }
    if (this.syncPending()) await this.syncInboxes()
    // What changed in Go while nobody was here.
    if (Date.now() - this.lastRead >= sideTiming.catchUpMs) {
      await this.readRequests()
    }
    // An owner who opens it is shown the answers held for them; a read of
    // one may find it reviewed or withheld meanwhile, and leave its end.
    if (await this.readHeldAnswers()) {
      await this.events.plan()
      await this.syncInboxes()
    }
    await this.planPoll()
  }

  override async onMessage(connection: Connection, message: WSMessage) {
    const said = parseSideClientMessage(message)
    const state = stateOf(connection)
    if (!said || !state) return
    this.schema()
    switch (said.type) {
      case "side-visibility":
        this.see(connection, state, said.visible)
        return
      case "side-send":
        await this.post(connection, state, said.clientId, said.body)
        return
      case "side-ask-mine":
        await this.askMine({ connection, state }, said.clientId, said.body)
        return
      case "side-ask-teammate":
        await this.askTeammate(
          { connection, state },
          said.clientId,
          said.agentId,
          said.body
        )
        return
      case "side-confirm":
        await this.confirm({ connection, state }, said.seq, said.answers)
        return
      case "side-decide":
        await this.decide(
          { connection, state },
          said.requestId,
          said.decision,
          said.message,
          said.allowedTools
        )
        return
      case "side-review":
        await this.review({ connection, state }, said.requestId, said.action)
        return
      case "side-history": {
        const page = this.page(said.beforeSeq, said.limit)
        send(connection, {
          type: "side-history",
          requestId: said.requestId,
          items: page.map((row) => this.wire(row, state.userId)),
          hasOlder: this.hasOlder(page),
        })
        return
      }
      case "side-read":
        await this.read(state.userId, said.seq)
        return
      case "side-typing":
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
        { type: "side-presence", userId: state.userId, online: false },
        undefined,
        connection.id
      )
    }
    if (staying.length === 0) this.stopTyping(state.userId, connection.id)
  }

  /**
   * The purge (src/realtime-purge.ts), over RPC from an inbox's, with a
   * DM's semantics (`DirectMessageAgent.purge`).
   *
   * Without a `scope` (an organization deleted), it empties the
   * conversation: every item, what its inboxes were told and which Go
   * threads its people's agents answered on, stops every agent run and
   * read, and tells every socket `side-gone` and closes it. As after any
   * purge, how far each person read and the highest seq used stay
   * (`keepSeqFloor`), so a later run never uses a seq again nor moves a
   * read back.
   *
   * For one of its people who left at `scope.removedAt`, the items stored at
   * or before then go, and so do any that came through that person's
   * sockets opened before it (their messages, their agent's answers, the
   * requests they filed); the thread their agent answered on is forgotten.
   * What is newer stays, as a new run of the conversation: both inboxes are
   * restated from it (`InboxAgent.restateConversation`), and sockets opened
   * since are sent it, while those opened before are told `side-gone` and
   * closed, so each page asks the Worker whether it may open it again.
   * Requests from before then are not brought back by a later read. Nothing
   * newer and no socket opened since: it is emptied as above.
   *
   * Resolves whether it was emptied, and the caller then takes it off both
   * inboxes; otherwise it stays in them, restated. Safe to call again.
   *
   * Either way, the events waiting for Go and those Go refused go with it
   * (`EventOutbox.purge`): nothing recorded before the purge is sent, and a
   * request already on its way is waited for, two seconds at most.
   */
  async purge(scope?: SidePurgeScope): Promise<{ emptied: boolean }> {
    // A scope that is not this conversation's is refused before anything
    // goes.
    const leaving = scope ? this.checkedScope(scope) : undefined
    this.epoch += 1
    this.schema()
    const eventsGone = this.events.purge()
    for (const held of this.typing.values()) clearTimeout(held.timer)
    this.typing.clear()
    this.sendBuckets.clear()
    this.inFlight.clear()
    this.heldAnswers.clear()
    this.heldReads.clear()
    if (!leaving) {
      await this.reset()
      this.endSockets(() => true)
      await eventsGone
      return { emptied: true }
    }
    const { userId, removedAt } = leaving
    // The newest may go too (what their page from before said after).
    this.keepSeqFloor()
    this.sql`DELETE FROM side_items WHERE created_at <= ${removedAt}
      OR (author_id = ${userId} AND session_at IS NOT NULL AND session_at <= ${removedAt})`
    this.sql`DELETE FROM side_threads WHERE user_id = ${userId}`
    // A run whose answer went stops, and nothing it streams lands.
    for (const [owner, run] of [...this.runs]) {
      if (this.item(run.seq)) continue
      run.controller.abort()
      this.runs.delete(owner)
      this.drafts.delete(run.seq)
    }
    // Go still lists the requests from before: a read does not bring them
    // back.
    this.setMeta(
      "adopt_after",
      String(
        Math.max(Number(this.meta("adopt_after") ?? "0"), Date.parse(removedAt))
      )
    )
    const openedBefore = (state: SideConnectionState) =>
      state.connectedAt <= removedAt
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
    // What stays is a new run of the conversation. The reads stay as they
    // were: every seq the run takes is newer than they are.
    if (this.latestSeq() === 0) {
      this.deleteMeta("conversation")
      this.setMeta("opened", "1")
    } else {
      this.setMeta("conversation", `sdc_${randomId()}`)
    }
    const { userA, userB } = this.ids
    for (const user of [userA, userB]) this.setMeta(`restate:${user}`, "1")
    this.endSockets(openedBefore)
    for (const { connection, state } of staying) {
      send(connection, this.snapshot(state.userId))
    }
    await this.syncInboxes()
    await this.planPoll()
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

  /**
   * The requests' schedule: reads Go for the open requests, as whoever is
   * here, then sets the next read while any stays open.
   */
  async pollRequests(
    _payload?: unknown,
    schedule?: Schedule<unknown>
  ): Promise<void> {
    this.schema()
    if (!schedule || this.meta("poll") === schedule.id) this.deleteMeta("poll")
    await this.readRequests()
    await this.planPoll()
  }

  private get ids(): SideConversationIds {
    const ids = parseSideConversationName(this.name)
    if (!ids) throw new Error(`not a side conversation's name: ${this.name}`)
    return ids
  }

  private schema(): void {
    if (this.schemaReady) return
    this.sql`CREATE TABLE IF NOT EXISTS side_items (
      seq INTEGER PRIMARY KEY,
      id TEXT NOT NULL,
      kind TEXT NOT NULL,
      author_id TEXT NOT NULL,
      client_id TEXT,
      request_id TEXT,
      state TEXT,
      body TEXT NOT NULL,
      data TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      session_at TEXT,
      settled INTEGER NOT NULL,
      noted INTEGER NOT NULL DEFAULT 0
    )`
    this.sql`CREATE UNIQUE INDEX IF NOT EXISTS side_items_by_client
      ON side_items (author_id, client_id) WHERE client_id IS NOT NULL`
    this.sql`CREATE UNIQUE INDEX IF NOT EXISTS side_items_by_request
      ON side_items (request_id) WHERE kind = 'request-card'`
    this.sql`CREATE INDEX IF NOT EXISTS side_items_open
      ON side_items (kind, state)`
    this.sql`CREATE TABLE IF NOT EXISTS side_reads (
      user_id TEXT PRIMARY KEY,
      seq INTEGER NOT NULL
    )`
    // How far each person's inbox has been told they read.
    this.sql`CREATE TABLE IF NOT EXISTS side_inbox_sync (
      user_id TEXT PRIMARY KEY,
      read_seq INTEGER NOT NULL DEFAULT 0
    )`
    // The Go thread each person's own agent answers on, here.
    this.sql`CREATE TABLE IF NOT EXISTS side_threads (
      user_id TEXT PRIMARY KEY,
      thread_id TEXT NOT NULL
    )`
    this.sql`CREATE TABLE IF NOT EXISTS side_meta (
      key TEXT PRIMARY KEY,
      value TEXT NOT NULL
    )`
    this.schemaReady = true
  }

  // ---- What the sockets are sent ----

  /** The conversation as a socket opens. */
  private snapshot(viewer: string): SideServerMessage {
    const page = this.page(0, SIDE_PAGE_SIZE)
    return {
      type: "side-snapshot",
      conversation: this.meta("conversation") ?? "",
      items: page.map((row) => this.wire(row, viewer)),
      hasOlder: this.hasOlder(page),
      reads: this.reads(),
      online: this.online(),
      typing: [...this.typing.keys()].filter((user) => user !== viewer),
      roster: this.roster(),
    }
  }

  /**
   * Up to `limit` items before `beforeSeq` (0: the latest), oldest first, as
   * many of the newest as fit one frame.
   */
  private page(beforeSeq: number, limit: number): ItemRow[] {
    const rows =
      beforeSeq > 0
        ? this.sql<ItemRow>`SELECT * FROM side_items WHERE seq < ${beforeSeq}
            ORDER BY seq DESC LIMIT ${limit}`
        : this.sql<ItemRow>`SELECT * FROM side_items
            ORDER BY seq DESC LIMIT ${limit}`
    const kept: ItemRow[] = []
    let size = 0
    for (const row of rows) {
      const rowSize = row.body.length + row.data.length + 200
      if (kept.length > 0 && size + rowSize > maxPageCharacters) break
      kept.push(row)
      size += rowSize
    }
    return kept.reverse()
  }

  private hasOlder(page: readonly ItemRow[]): boolean {
    const oldest = page.at(0)?.seq
    return (
      oldest !== undefined &&
      this.sql<{ older: number }>`SELECT EXISTS (
        SELECT 1 FROM side_items WHERE seq < ${oldest}
      ) AS older`.at(0)?.older === 1
    )
  }

  /** An item as `viewer` is sent it: a page id only on their own, a confirmation only to its agent's owner. */
  private wire(row: ItemRow, viewer: string): SideWireItem {
    const base = { id: row.id, seq: row.seq, createdAt: row.created_at }
    const own = row.author_id === viewer && row.client_id !== null
    switch (row.kind) {
      case "message": {
        const data = parseData<MessageData>(row)
        return {
          kind: "message",
          ...base,
          authorId: row.author_id,
          body: row.body,
          ...(own ? { clientId: row.client_id ?? undefined } : {}),
          ...(data.asksAgentId ? { asksAgentId: data.asksAgentId } : {}),
        }
      }
      case "agent-message": {
        const data = parseData<AgentData>(row)
        const draft = this.drafts.get(row.seq)
        const status = (row.state ?? "done") as SideAgentMessageStatus
        return {
          kind: "agent-message",
          ...base,
          agentId: data.agentId,
          agentName: data.agentName,
          ownerId: row.author_id,
          body: draft ? clip(bodyOf(draft), SIDE_MAX_AGENT_TEXT) : row.body,
          updatedAt: row.updated_at,
          status,
          ...(data.error ? { error: data.error } : {}),
          ...(data.code ? { code: data.code } : {}),
          ...(data.replyTo ? { replyTo: data.replyTo } : {}),
          ...(data.via ? { via: data.via } : {}),
          ...(status === "waiting" &&
          row.author_id === viewer &&
          data.confirmations?.length
            ? { confirmations: data.confirmations }
            : {}),
        }
      }
      case "request-card": {
        const data = parseData<CardData>(row)
        // What the answer guard found, and an answer it holds, are its
        // target's owner's alone to see.
        const owner = viewer === data.targetId
        const held =
          owner && row.state === "review" && row.request_id
            ? this.heldAnswers.get(row.request_id)
            : undefined
        return {
          kind: "request-card",
          ...base,
          requestId: row.request_id ?? "",
          requesterId: row.author_id,
          requesterAgentId: data.requesterAgentId,
          requesterAgentName: data.requesterAgentName,
          targetId: data.targetId,
          targetAgentId: data.targetAgentId,
          targetAgentName: data.targetAgentName,
          approval: data.approval,
          status: (row.state ?? "pending") as SideRequestStatus,
          task: row.body,
          updatedAt: row.updated_at,
          expiresAt: data.expiresAt,
          ...(data.decision ? { decision: data.decision } : {}),
          ...(own ? { clientId: row.client_id ?? undefined } : {}),
          ...(data.requestedTools?.length
            ? { requestedTools: data.requestedTools }
            : {}),
          ...(owner && data.guard ? { guard: data.guard } : {}),
          ...(held ? { heldAnswer: held } : {}),
          ...(data.review ? { review: data.review } : {}),
        }
      }
      case "status": {
        const data = parseData<StatusData>(row)
        return {
          kind: "status",
          ...base,
          requestId: row.request_id ?? "",
          outcome: data.outcome,
          authorId: row.author_id,
          text: row.body,
        }
      }
    }
  }

  /** Sends every socket an item as it now stands, each as its person sees it, or those `to` picks. */
  private announce(
    row: ItemRow,
    to?: (state: SideConnectionState) => boolean
  ): void {
    for (const connection of this.getConnections()) {
      const state = stateOf(connection)
      if (!state || (to && !to(state))) continue
      send(connection, {
        type: "side-item",
        item: this.wire(row, state.userId),
      })
    }
  }

  // ---- People's messages ----

  /** Says `body` as the socket's person, or tells the socket why not. */
  private async post(
    connection: Connection,
    state: SideConnectionState,
    clientId: string,
    body: string
  ): Promise<void> {
    const author = state.userId
    const stored = this.itemByClient(author, clientId)
    if (stored) {
      // Sent again: the page never heard it arrived.
      send(connection, { type: "side-item", item: this.wire(stored, author) })
      return
    }
    const text = this.checkBody(connection, clientId, body, author)
    if (text === undefined) return
    let row: ItemRow
    try {
      // The message and its event for Go, as one: when either fails,
      // neither is kept.
      row = this.transaction(() => {
        const message = this.insert({
          kind: "message",
          authorId: author,
          clientId,
          body: text,
          data: {} satisfies MessageData,
          sessionAt: state.connectedAt,
          settled: true,
        })
        this.events.recordPosted(
          message,
          memberAuthor(author),
          message.created_at
        )
        return message
      })
    } catch (error) {
      console.error("side conversation: could not store a message", error)
      this.refuse(connection, clientId, "unavailable")
      return
    }
    this.announce(row)
    this.readOwn(author, row.seq, false)
    await this.events.plan()
    await this.syncInboxes()
  }

  /** A body as it is stored, or nothing once the socket is told why it was refused. */
  private checkBody(
    connection: Connection,
    clientId: string,
    body: string,
    author: string
  ): string | undefined {
    const text = body.trim()
    const refused: SideFailure | undefined =
      text === ""
        ? "empty"
        : codePoints(text) > SIDE_MAX_BODY_LENGTH
          ? "too_long"
          : !this.allowSend(author)
            ? "rate_limited"
            : undefined
    if (refused) {
      this.refuse(connection, clientId, refused)
      return undefined
    }
    return text
  }

  private refuse(
    connection: Connection,
    clientId: string,
    code: SideFailure,
    message?: string
  ): void {
    send(connection, {
      type: "side-failed",
      clientId,
      code,
      message: message ?? refusals[code] ?? "That couldn't be done.",
    })
  }

  private refuseTarget(
    connection: Connection,
    target: string,
    code: SideFailure,
    message: string
  ): void {
    send(connection, { type: "side-refused", target, code, message })
  }

  /**
   * The author's own item reads the conversation through it, and stops
   * their typing. The read is an event for Go only when `record` says so:
   * a message's (a question's too) is its own event's to say.
   */
  private readOwn(author: string, seq: number, record: boolean): void {
    this.setRead(author, seq, record)
    this.tell({ type: "side-read", userId: author, seq })
    this.stopTyping(author)
  }

  // ---- Asking one's own agent ----

  /**
   * A message that asks the socket's person's own agent (their primary
   * agent), which then answers as an agent's message both people see live.
   * Sent again under the same page id, it is answered with what is stored,
   * and an answer that failed only because the socket's session had ended
   * is asked for again (the page renewed it and reopened the socket).
   */
  private async askMine(
    actor: Actor,
    clientId: string,
    body: string
  ): Promise<void> {
    const { connection, state } = actor
    const owner = state.userId
    const stored = this.itemByClient(owner, clientId)
    if (stored) {
      send(connection, { type: "side-item", item: this.wire(stored, owner) })
      const answer = this.answerTo(stored.seq)
      if (!answer) return
      const data = parseData<AgentData>(answer)
      if (
        answer.state === "failed" &&
        data.code === sessionExpiredCode &&
        !this.runs.has(owner)
      ) {
        await this.runOwnAgent(actor, answer.seq, { message: stored.body })
      } else {
        send(connection, { type: "side-item", item: this.wire(answer, owner) })
      }
      return
    }
    const agent = this.roster().people.find((person) => person.userId === owner)
    if (!agent) {
      this.refuse(
        connection,
        clientId,
        "not_allowed",
        "You have no agent in this teamspace to ask."
      )
      return
    }
    if (this.runs.has(owner)) {
      this.refuse(
        connection,
        clientId,
        "busy",
        `${agent.agentName} is still answering here. Ask again once it has.`
      )
      return
    }
    const text = this.checkBody(connection, clientId, body, owner)
    if (text === undefined) return
    let question: ItemRow
    let answer: ItemRow
    try {
      // The question (a person's message, with its event for Go) and the
      // answer it waits for, as one. The answer is an event only once its
      // run has ended with something said (`settleRun`).
      ;[question, answer] = this.transaction<[ItemRow, ItemRow]>(() => {
        const asked = this.insert({
          kind: "message",
          authorId: owner,
          clientId,
          body: text,
          data: { asksAgentId: agent.agentId } satisfies MessageData,
          sessionAt: state.connectedAt,
          settled: true,
        })
        this.events.recordPosted(asked, memberAuthor(owner), asked.created_at)
        const awaited = this.insert({
          kind: "agent-message",
          authorId: owner,
          body: "",
          state: "streaming",
          data: {
            agentId: agent.agentId,
            agentName: agent.agentName,
            replyTo: asked.seq,
          } satisfies AgentData,
          sessionAt: state.connectedAt,
          settled: false,
        })
        return [asked, awaited]
      })
    } catch (error) {
      console.error("side conversation: could not store a question", error)
      this.refuse(connection, clientId, "unavailable")
      return
    }
    this.announce(question)
    this.announce(answer)
    this.readOwn(owner, question.seq, false)
    await this.events.plan()
    await this.syncInboxes()
    await this.runOwnAgent(actor, answer.seq, { message: text })
  }

  /**
   * The owner's answers to what their agent's message waits on: every
   * confirmation, together, as Go takes them. The run goes on in the same
   * message.
   */
  private async confirm(
    actor: Actor,
    seq: number,
    answers: Array<{ id: string; confirmed: boolean }>
  ): Promise<void> {
    const { connection, state } = actor
    const row = this.item(seq)
    const data = row ? parseData<AgentData>(row) : undefined
    const waiting = new Set(
      (data?.confirmations ?? []).map((item) => item.callId)
    )
    const answered = new Set(answers.map((answer) => answer.id))
    if (
      !row ||
      row.kind !== "agent-message" ||
      row.author_id !== state.userId ||
      row.state !== "waiting" ||
      waiting.size !== answered.size ||
      [...waiting].some((id) => !answered.has(id))
    ) {
      this.refuseTarget(
        connection,
        String(seq),
        "not_allowed",
        "There is nothing of yours waiting for that answer."
      )
      return
    }
    if (this.runs.has(state.userId)) {
      this.refuseTarget(
        connection,
        String(seq),
        "busy",
        "Your agent is still answering here."
      )
      return
    }
    await this.runOwnAgent(actor, seq, { confirmations: answers })
  }

  /**
   * Runs the owner's primary agent as them on their thread for this
   * conversation, and streams what it says into the agent's message `seq`:
   * deltas to every socket as they come, each finished message stored.
   * A thread Go no longer has is started again, once.
   */
  private async runOwnAgent(
    actor: Actor,
    seq: number,
    input: RunInput
  ): Promise<void> {
    const { state } = actor
    const owner = state.userId
    const row = this.item(seq)
    if (!row) return
    const epoch = this.epoch
    const controller = new AbortController()
    const timeout = setTimeout(
      () => controller.abort(),
      sideTiming.runTimeoutMs
    )
    this.runs.set(owner, { seq, controller })
    const draft: Draft = {
      finished: "confirmations" in input && row.body ? [row.body] : [],
      draft: "",
      confirmations: [],
      full: false,
    }
    this.drafts.set(seq, draft)
    const data = parseData<AgentData>(row)
    this.announce(
      this.rewrite(row, {
        state: "streaming",
        settled: 0,
        data: { ...data, error: undefined, code: undefined, confirmations: [] },
      })
    )
    try {
      await this.keepAliveWhile(async () => {
        for (let attempt = 0; attempt < 2; attempt += 1) {
          const threadId = await this.threadFor(state)
          if (this.epoch !== epoch) return
          // Set from the stream: Go no longer has the thread.
          const attemptState = { threadGone: false }
          await relayGoRun({
            connection: state,
            words: runWords,
            label: "side conversation",
            signal: controller.signal,
            request: () => ({
              url: new URL(
                organizationPath(
                  this.ids.organizationId,
                  "threads",
                  threadId,
                  "runs"
                ),
                this.env.ORYXA_API_ORIGIN
              ),
              body: this.runBody(owner, seq, input),
              input,
            }),
            write: (item) => {
              if (
                item.type === "run_failed" &&
                item.code === "not_found" &&
                attempt === 0 &&
                !("confirmations" in input)
              ) {
                attemptState.threadGone = true
                return
              }
              this.fold(seq, draft, item)
            },
          })
          if (!attemptState.threadGone) return
          // The member deleted the thread: the next attempt starts another.
          this.sql`DELETE FROM side_threads WHERE user_id = ${owner}`
        }
      })
    } catch (error) {
      if (!controller.signal.aborted) {
        draft.failure = isExpiry(error)
          ? { error: sessionEndedWords, code: sessionExpiredCode }
          : { error: runWords.unreachable }
        if (!isExpiry(error)) {
          console.error("side conversation: could not start a run", error)
        }
      }
    } finally {
      clearTimeout(timeout)
      if (this.runs.get(owner)?.controller === controller) {
        this.runs.delete(owner)
      }
    }
    if (this.epoch !== epoch || !this.item(seq)) {
      this.drafts.delete(seq)
      return
    }
    this.settleRun(seq, draft, state)
    await this.events.plan()
    await this.syncInboxes()
    // Its agent may have asked the teammate's agent for something.
    await this.readRequests()
    await this.planPoll()
  }

  /**
   * What a run is sent: answers go as they are; a question goes with the
   * conversation before it, framed as untrusted data.
   */
  private runBody(owner: string, seq: number, input: RunInput): RunInput {
    if ("confirmations" in input) return input
    const ids = this.ids
    const roster = this.roster()
    const peer = peerIn(ids, owner) ?? ""
    const answer = this.item(seq)
    const replyTo = answer ? parseData<AgentData>(answer).replyTo : undefined
    const self = roster.people.find((person) => person.userId === owner)
    const transcript = this.transcript(
      replyTo ?? seq,
      PROMPT_TRANSCRIPT_LINES,
      self?.agentId
    )
    return {
      message: ownAgentPrompt({
        teamspaceId: ids.teamspaceId,
        teamspaceName: roster.teamspaceName || "this teamspace",
        askerName: personName(roster, owner, "Your member"),
        agentName: self?.agentName ?? "their agent",
        peerName: personName(roster, peer),
        peerAgents: agentsOf(roster, peer),
        transcript,
        question: input.message,
      }),
    }
  }

  /** Folds one item of a run into the answer it streams. */
  private fold(seq: number, draft: Draft, item: Item): void {
    const fromAgent = item.author !== "member"
    switch (item.type) {
      case "delta": {
        if (!fromAgent || !item.text || draft.full) return
        const at = bodyOf(draft).length
        const separator =
          draft.draft === "" && draft.finished.some((part) => part !== "")
            ? "\n\n"
            : ""
        draft.draft += item.text
        const length = at + separator.length + item.text.length
        // Counted in code points only once it could be too long: a code
        // point is one or two UTF-16 units.
        if (
          length > SIDE_MAX_AGENT_TEXT &&
          codePoints(bodyOf(draft)) > SIDE_MAX_AGENT_TEXT
        ) {
          draft.full = true
          this.persistDraft(seq, draft)
          return
        }
        this.tell({ type: "side-delta", seq, at, text: separator + item.text })
        return
      }
      case "message":
        if (!fromAgent) return
        if (item.text) {
          draft.finished.push(item.text)
        } else if (item.error) {
          draft.stopped = item.error
        }
        draft.draft = ""
        this.persistDraft(seq, draft)
        return
      case "confirmation":
        if (item.status !== "pending" || !item.call_id) return
        draft.confirmations.push({
          callId: item.call_id,
          tool: item.tool ?? "",
          ...(item.arguments ? { arguments: item.arguments } : {}),
          ...(item.hint ? { hint: item.hint } : {}),
        })
        return
      case "run_failed":
        draft.failure = {
          error:
            item.code === sessionExpiredCode
              ? sessionEndedWords
              : (item.error ?? runWords.unreachable),
          ...(item.code ? { code: item.code } : {}),
        }
        return
      default:
        // Tool calls, their results and files stay in the owner's thread.
        return
    }
  }

  /** Stores an answer's text so far and sends it whole: a socket that opens is sent it too. */
  private persistDraft(seq: number, draft: Draft): void {
    const row = this.item(seq)
    if (!row) return
    this.announce(
      this.rewrite(row, { body: clip(bodyOf(draft), SIDE_MAX_AGENT_TEXT) })
    )
  }

  /** An answer's run ended: it is done, waits on its owner, or failed. */
  private settleRun(
    seq: number,
    draft: Draft,
    state: SideConnectionState
  ): void {
    this.drafts.delete(seq)
    const row = this.item(seq)
    if (!row) return
    const data = parseData<AgentData>(row)
    const status: SideAgentMessageStatus = draft.failure
      ? "failed"
      : draft.confirmations.length > 0
        ? "waiting"
        : "done"
    const body = clip(bodyOf(draft), SIDE_MAX_AGENT_TEXT)
    const error =
      draft.failure?.error ??
      (body === "" && status === "done"
        ? (draft.stopped ?? "The agent had nothing to say.")
        : undefined)
    const settled = this.transaction(() => {
      const ended = this.rewrite(row, {
        body,
        state: status,
        settled: 1,
        data: {
          ...data,
          error,
          code: draft.failure?.code,
          confirmations: status === "waiting" ? draft.confirmations : [],
        },
      })
      // The agent's final answer is a message Go is told of, as it ends:
      // never its deltas, a wait on its owner, a failure, or nothing said.
      if (status === "done" && body !== "") {
        this.events.recordPosted(
          ended,
          agentAuthor(ended.author_id, data.agentId),
          ended.updated_at
        )
      }
      return ended
    })
    this.announce(settled)
    if (draft.failure?.code === sessionExpiredCode) this.expire(state.cookie)
  }

  /** The Go thread the person's own agent answers on here, started as them the first time. */
  private async threadFor(state: SideConnectionState): Promise<string> {
    const held = this.sql<{ thread_id: string }>`SELECT thread_id
      FROM side_threads WHERE user_id = ${state.userId}`.at(0)
    if (held) return held.thread_id
    // Marked as a side conversation's, so Go keeps it out of the person's
    // own chat list: its turns carry this conversation's framing, not theirs.
    const thread = await this.goAs<{ id?: unknown }>(
      state,
      organizationPath(this.ids.organizationId, "threads"),
      { method: "POST", body: { origin: "side_conversation" } }
    )
    if (typeof thread.id !== "string" || !thread.id) {
      throw new Error("Go answered a new thread without its id")
    }
    this.sql`INSERT INTO side_threads (user_id, thread_id)
      VALUES (${state.userId}, ${thread.id})
      ON CONFLICT (user_id) DO UPDATE SET thread_id = excluded.thread_id`
    return thread.id
  }

  // ---- Asking the teammate's agent ----

  /**
   * Files an agent request, as the socket's person, to one of the other
   * person's agents in this teamspace, and shows it as a card. Go decides
   * who approves it; the card says which. Sent again under the same page id,
   * it is answered with the card already filed.
   */
  private async askTeammate(
    actor: Actor,
    clientId: string,
    agentId: string,
    body: string
  ): Promise<void> {
    const { connection, state } = actor
    const asker = state.userId
    const stored = this.itemByClient(asker, clientId)
    if (stored) {
      send(connection, { type: "side-item", item: this.wire(stored, asker) })
      return
    }
    const key = `file:${asker}:${clientId}`
    if (this.inFlight.has(key)) return
    const peer = peerIn(this.ids, asker) ?? ""
    let target = agentsOf(this.roster(), peer).find(
      (agent) => agent.id === agentId
    )
    if (!target) {
      // A specialist contributed since the socket opened: the teamspace is
      // read again before the ask is refused.
      await this.refreshRoster(state)
      target = agentsOf(this.roster(), peer).find(
        (agent) => agent.id === agentId
      )
    }
    if (!target) {
      this.refuse(
        connection,
        clientId,
        "not_allowed",
        `That agent is not ${personName(this.roster(), peer, "your teammate")}'s in this teamspace.`
      )
      return
    }
    const open = this.sql<{ count: number }>`SELECT COUNT(*) AS count
      FROM side_items WHERE kind = 'request-card' AND author_id = ${asker}
      AND state = 'pending'`.at(0)?.count
    if ((open ?? 0) >= maxOpenRequestsPerPerson) {
      this.refuse(
        connection,
        clientId,
        "too_many_requests",
        `${maxOpenRequestsPerPerson} of your requests are waiting already. Wait for one to be decided.`
      )
      return
    }
    const text = this.checkBody(connection, clientId, body, asker)
    if (text === undefined) return
    const ids = this.ids
    const roster = this.roster()
    const epoch = this.epoch
    this.inFlight.add(key)
    let answer: unknown
    try {
      answer = await this.goAs<unknown>(
        state,
        organizationPath(ids.organizationId, "agent-requests"),
        {
          method: "POST",
          body: agentRequestBody({
            teamspaceId: ids.teamspaceId,
            teamspaceName: roster.teamspaceName || "this teamspace",
            askerName: personName(roster, asker),
            peerName: personName(roster, peer),
            targetAgentId: target.id,
            task: text,
            transcript: this.transcript(0, 12),
          }),
          headers: { "Idempotency-Key": `side-${clientId}` },
        }
      )
    } catch (error) {
      this.inFlight.delete(key)
      if (this.epoch !== epoch) return
      if (isExpiry(error)) {
        this.expire(state.cookie)
        this.refuse(connection, clientId, "session_expired")
      } else if (
        error instanceof ApiError &&
        error.status >= 400 &&
        error.status < 500
      ) {
        this.refuse(
          connection,
          clientId,
          "not_allowed",
          this.requestRefusal(error)
        )
      } else {
        console.error("side conversation: could not file a request", error)
        this.refuse(connection, clientId, "unavailable")
      }
      return
    }
    this.inFlight.delete(key)
    if (this.epoch !== epoch) return
    const fields = cardFields(answer)
    if (!fields || !this.isBetweenUs(fields)) {
      console.error("side conversation: Go answered a request it cannot show")
      this.refuse(connection, clientId, "unavailable")
      return
    }
    const adopted = this.cardRow(fields.requestId)
    if (adopted) {
      // A read found it first: it is this page's own now.
      const row = adopted.client_id
        ? adopted
        : this.rewrite(adopted, { clientId })
      this.announce(row)
    } else {
      const row = this.insert({
        kind: "request-card",
        authorId: asker,
        clientId,
        requestId: fields.requestId,
        body: fields.task,
        state: fields.status,
        data: this.cardData(fields, answer),
        sessionAt: state.connectedAt,
        settled: true,
      })
      this.announce(row)
      // A card is no event: the read it makes is one.
      this.readOwn(asker, row.seq, true)
    }
    await this.events.plan()
    await this.syncInboxes()
    await this.planPoll()
  }

  /** Go's words for a refused request, or the object's where Go's would not help. */
  private requestRefusal(error: ApiError): string {
    switch (error.code) {
      case "teamspace_disabled":
        return "This teamspace is paused, so its agents can't be asked for anything."
      case "forbidden":
        return "Only the teamspace's team can ask its agents for help."
      case "not_found":
        return "That agent can't be asked here any more."
      default:
        return error.message || "The request was refused."
    }
  }

  /**
   * The target's owner decides a request held for them, from their own
   * socket, as themself (`POST …/agent-requests/{id}/decision`, the only
   * way Go takes an owner's decision). An approval allows `allowedTools`,
   * which must be among what the request asked for, or none: nothing
   * grants a tool unless the owner's page named it.
   */
  private async decide(
    actor: Actor,
    requestId: string,
    decision: "approved" | "rejected",
    message: string | undefined,
    allowedTools: readonly string[] | undefined
  ): Promise<void> {
    const { connection, state } = actor
    const card = this.cardRow(requestId)
    const data = card ? parseData<CardData>(card) : undefined
    if (
      !card ||
      !data ||
      data.targetId !== state.userId ||
      data.approval !== "owner"
    ) {
      this.refuseTarget(
        connection,
        requestId,
        "not_allowed",
        "Only the person whose agent was asked can decide this request."
      )
      return
    }
    if (card.state !== "pending") {
      send(connection, {
        type: "side-item",
        item: this.wire(card, state.userId),
      })
      this.refuseTarget(
        connection,
        requestId,
        "already_decided",
        "This request was decided already, or expired."
      )
      return
    }
    const note = (message ?? "").trim()
    if (codePoints(note) > SIDE_MAX_DECISION_MESSAGE) {
      this.refuseTarget(
        connection,
        requestId,
        "too_long",
        `A decision's note holds at most ${SIDE_MAX_DECISION_MESSAGE.toLocaleString("en-US")} characters.`
      )
      return
    }
    // A decline allows nothing; an approval only what was asked for.
    const tools = decision === "approved" ? [...new Set(allowedTools)] : []
    const requested = data.requestedTools ?? []
    if (tools.some((tool) => !requested.includes(tool))) {
      this.refuseTarget(
        connection,
        requestId,
        "not_allowed",
        "Only the tools the request asked for can be allowed."
      )
      return
    }
    await this.asOwner(actor, requestId, "decision", {
      decision,
      // Go requires a note: without one, the decision says where it was made.
      message:
        note ||
        (decision === "approved"
          ? "Approved in the side conversation."
          : "Declined in the side conversation."),
      ...(decision === "approved" ? { allowed_tools: tools } : {}),
    })
  }

  /**
   * The target's owner reviews an answer the answer guard held for them,
   * from their own socket, as themself (`POST …/agent-requests/{id}/review`,
   * which Go takes from the owner's own session alone, as a decision): it
   * shares the answer as it stands (`send`), fully masked (`send_masked`),
   * or not at all (`withhold`).
   */
  private async review(
    actor: Actor,
    requestId: string,
    action: SideReviewAction
  ): Promise<void> {
    const { connection, state } = actor
    const card = this.cardRow(requestId)
    const data = card ? parseData<CardData>(card) : undefined
    if (!card || !data || data.targetId !== state.userId) {
      this.refuseTarget(
        connection,
        requestId,
        "not_allowed",
        "Only the person whose agent answered can review its answer."
      )
      return
    }
    if (card.state !== "review") {
      send(connection, {
        type: "side-item",
        item: this.wire(card, state.userId),
      })
      this.refuseTarget(
        connection,
        requestId,
        "already_decided",
        "This answer was reviewed already, or expired."
      )
      return
    }
    // An answer the guard could not check has no masked version to send.
    const held = this.heldAnswers.get(requestId)
    if (action === "send_masked" && held && held.masked === undefined) {
      this.refuseTarget(connection, requestId, "not_allowed", noMaskedWords)
      return
    }
    await this.asOwner(actor, requestId, "review", { action })
  }

  /**
   * Sends the owner's decision or review of a request to Go as them, one at
   * a time, and brings its card up to date with Go's answer; a refusal is
   * told to their socket in words. A request decided or reviewed meanwhile
   * is read again, so the card shows how it stands.
   */
  private async asOwner(
    actor: Actor,
    requestId: string,
    what: "decision" | "review",
    body: Record<string, unknown>
  ): Promise<void> {
    const { connection, state } = actor
    const key = `${what}:${requestId}`
    if (this.inFlight.has(key)) return
    this.inFlight.add(key)
    const epoch = this.epoch
    const path = organizationPath(
      this.ids.organizationId,
      "agent-requests",
      requestId,
      what
    )
    let answer: unknown
    try {
      answer = await this.goAs<unknown>(state, path, { method: "POST", body })
    } catch (error) {
      this.inFlight.delete(key)
      if (this.epoch !== epoch) return
      if (isExpiry(error)) {
        this.expire(state.cookie)
        this.refuseTarget(
          connection,
          requestId,
          "session_expired",
          refusals.session_expired ?? ""
        )
        return
      }
      if (error instanceof ApiError && error.status === 409) {
        // Decided or reviewed meanwhile, or past its time: the card shows
        // how it stands.
        await this.readRequest(state, requestId)
        await this.events.plan()
      }
      if (
        error instanceof ApiError &&
        error.status >= 400 &&
        error.status < 500
      ) {
        const refusal =
          what === "decision"
            ? decisionRefusal(error)
            : reviewRefusal(
                error,
                body.action,
                this.cardRow(requestId)?.state === "review"
              )
        this.refuseTarget(connection, requestId, refusal.code, refusal.message)
        return
      }
      console.error(`side conversation: could not send a ${what}`, error)
      this.refuseTarget(
        connection,
        requestId,
        "unavailable",
        `The ${what} couldn't be sent. Try again.`
      )
      return
    }
    this.inFlight.delete(key)
    if (this.epoch !== epoch) return
    this.applyRequest(answer, false)
    await this.events.plan()
    await this.syncInboxes()
    await this.planPoll()
  }

  // ---- Reading Go's requests ----

  /**
   * Reads what Go has of the requests between the two in this teamspace, as
   * whoever is here (`GET …/agent-requests?teamspace_id=&with_user=`, both
   * directions, newest first), and brings each card up to date; an open card
   * the listing did not reach is read on its own. One read at a time.
   */
  private readRequests(): Promise<void> {
    if (this.reading) return this.reading
    this.reading = this.readRequestsOnce().finally(() => {
      this.reading = undefined
    })
    return this.reading
  }

  private async readRequestsOnce(): Promise<void> {
    const reader = this.reader()
    if (!reader) return
    const { state } = reader
    const epoch = this.epoch
    const ids = this.ids
    const peer = peerIn(ids, state.userId) ?? ""
    this.lastRead = Date.now()
    let listed: unknown[]
    try {
      const answer = await this.goAs<{ requests?: unknown }>(
        state,
        withSearch(organizationPath(ids.organizationId, "agent-requests"), {
          teamspace_id: ids.teamspaceId,
          with_user: peer,
          limit: listedRequests,
        })
      )
      listed = Array.isArray(answer.requests) ? answer.requests : []
    } catch (error) {
      if (this.epoch === epoch) this.goFailed(state, error, "read its requests")
      return
    }
    if (this.epoch !== epoch) return
    const seen = new Set<string>()
    // Go lists the newest first: the oldest is brought up to date first, so
    // what their ends leave lands in the order they were asked.
    for (const request of [...listed].reverse()) {
      const requestId = this.applyRequest(request, true)
      if (requestId) seen.add(requestId)
    }
    for (const card of this.openCards()) {
      if (card.request_id && !seen.has(card.request_id)) {
        await this.readRequest(state, card.request_id)
        if (this.epoch !== epoch) return
      }
    }
    await this.readHeldAnswers()
    if (this.epoch !== epoch) return
    await this.events.plan()
    await this.syncInboxes()
  }

  /**
   * Reads, as its owner, each answer held for review that this instance
   * does not hold, while that owner is here: Go shows a held answer to its
   * owner alone, and the object keeps it in memory only. A Go that shows
   * none is asked again once a socket's catch-up has passed, not on every
   * poll. Resolves whether it read any.
   */
  private async readHeldAnswers(): Promise<boolean> {
    const epoch = this.epoch
    let read = false
    const held = this.sql<ItemRow>`SELECT * FROM side_items
      WHERE kind = 'request-card' AND state = 'review'`
    for (const card of held) {
      const requestId = card.request_id
      if (!requestId || this.heldAnswers.has(requestId)) continue
      const readAt = this.heldReads.get(requestId)
      if (readAt !== undefined && Date.now() - readAt < sideTiming.catchUpMs) {
        continue
      }
      const owner = this.actorOf(parseData<CardData>(card).targetId)
      if (!owner) continue
      this.heldReads.set(requestId, Date.now())
      await this.readRequest(owner.state, requestId)
      if (this.epoch !== epoch) return false
      read = true
    }
    return read
  }

  /** Reads one request as `state`'s person and brings its card up to date. */
  private async readRequest(
    state: SideConnectionState,
    requestId: string
  ): Promise<void> {
    const epoch = this.epoch
    try {
      const request = await this.goAs<unknown>(
        state,
        organizationPath(this.ids.organizationId, "agent-requests", requestId)
      )
      if (this.epoch === epoch) this.applyRequest(request, false)
    } catch (error) {
      if (this.epoch === epoch) this.goFailed(state, error, "read a request")
    }
  }

  /**
   * Brings the card of Go's request up to date, and leaves what its end
   * brings: the result as the target agent's message, or a quiet line. A
   * request with no card yet gets one when `adopt` says so and it is between
   * the two, in this teamspace, made since the conversation began. Resolves
   * the request's id when it is one of this conversation's.
   */
  private applyRequest(request: unknown, adopt: boolean): string | undefined {
    const fields = cardFields(request)
    if (!fields || !this.isBetweenUs(fields)) return undefined
    const teamspaceId = (request as { teamspace_id?: unknown }).teamspace_id
    if (teamspaceId !== this.ids.teamspaceId) return undefined
    let row = this.cardRow(fields.requestId)
    if (!row) {
      if (
        !adopt ||
        !(
          Date.parse(fields.createdAt) > Number(this.meta("adopt_after") ?? "0")
        )
      ) {
        return undefined
      }
      row = this.insert({
        kind: "request-card",
        authorId: fields.requesterId,
        requestId: fields.requestId,
        body: fields.task,
        state: fields.status,
        data: this.cardData(fields, request),
        settled: true,
      })
      this.holdAnswer(fields, request)
      this.announce(row)
    } else {
      const data = parseData<CardData>(row)
      const revision = (request as { revision?: unknown }).revision
      // What the guard found stays as its owner's read brought it: the
      // requester's read may not carry it.
      const guard = fields.guard ?? data.guard
      const heldChanged = this.holdAnswer(fields, request)
      if (
        row.state !== fields.status ||
        (typeof revision === "number" && revision !== data.revision) ||
        Boolean(fields.decision) !== Boolean(data.decision) ||
        Boolean(fields.review) !== Boolean(data.review) ||
        !sameList(fields.requestedTools, data.requestedTools) ||
        !sameList(guard?.kinds, data.guard?.kinds) ||
        guard?.state !== data.guard?.state
      ) {
        row = this.rewrite(row, {
          state: fields.status,
          data: {
            ...this.cardData(fields, request),
            ...(guard ? { guard } : {}),
            outcomeSeq: data.outcomeSeq,
          },
        })
        this.announce(row)
      } else if (heldChanged) {
        // Only its owner is sent what arrived.
        this.announce(row, (state) => state.userId === data.targetId)
      }
    }
    this.leaveOutcome(row, request)
    return fields.requestId
  }

  /**
   * Keeps the answer Go holds for review, as it showed it to its owner, or
   * lets it go once the request is past review. Resolves whether what the
   * owner is shown changed.
   */
  private holdAnswer(fields: CardFields, request: unknown): boolean {
    if (fields.status !== "review") {
      this.heldReads.delete(fields.requestId)
      return this.heldAnswers.delete(fields.requestId)
    }
    // A read as the requester, or a Go that shows none, holds nothing new.
    const held = heldAnswerOf(request)
    if (!held) return false
    const before = this.heldAnswers.get(fields.requestId)
    if (
      before?.stored === held.stored &&
      before.masked === held.masked &&
      before.failed === held.failed
    ) {
      return false
    }
    this.heldAnswers.set(fields.requestId, held)
    return true
  }

  /** What a card keeps of Go's request beside its columns. */
  private cardData(fields: CardFields, request: unknown): CardData {
    const revision = (request as { revision?: unknown }).revision
    return {
      requesterAgentId: fields.requesterAgentId,
      requesterAgentName: fields.requesterAgentName,
      targetId: fields.targetId,
      targetAgentId: fields.targetAgentId,
      targetAgentName: fields.targetAgentName,
      approval: fields.approval,
      expiresAt: fields.expiresAt,
      goCreatedAt: fields.createdAt,
      ...(fields.decision ? { decision: fields.decision } : {}),
      ...(typeof revision === "number" ? { revision } : {}),
      ...(fields.requestedTools
        ? { requestedTools: fields.requestedTools }
        : {}),
      ...(fields.guard ? { guard: fields.guard } : {}),
      ...(fields.review ? { review: fields.review } : {}),
    }
  }

  /** A request that ended leaves one item, once: its result, or a quiet line. */
  private leaveOutcome(card: ItemRow, request: unknown): void {
    const data = parseData<CardData>(card)
    const status = card.state as SideRequestStatus
    if (data.outcomeSeq !== undefined || isOpen(status)) return
    const ownerName = personName(this.roster(), data.targetId, "Its owner")
    // The item, its event for Go (a result is the asked agent's final
    // answer; a quiet line is none) and the card's note of it, as one.
    const outcome = this.transaction((): ItemRow | undefined => {
      if (status === "completed") {
        const text = resultText(request)
        if (text === undefined) return undefined
        const result = this.insert({
          kind: "agent-message",
          authorId: data.targetId,
          requestId: card.request_id ?? undefined,
          body: clip(text, SIDE_MAX_AGENT_TEXT),
          state: "done",
          data: {
            agentId: data.targetAgentId,
            agentName: data.targetAgentName,
            via: {
              requestId: card.request_id ?? "",
              onBehalfOf: data.targetId,
              ...(data.approval === "owner"
                ? { approvedBy: data.targetId }
                : {}),
              // Held by the answer guard, it was shared once its owner
              // reviewed it.
              ...(data.review ? { reviewedBy: data.targetId } : {}),
            },
          } satisfies AgentData,
          settled: true,
        })
        this.events.recordPosted(
          result,
          agentAuthor(data.targetId, data.targetAgentId),
          result.created_at
        )
        this.rewrite(card, { data: { ...data, outcomeSeq: result.seq } })
        return result
      }
      if (
        status === "rejected" ||
        status === "failed" ||
        status === "expired" ||
        status === "withheld"
      ) {
        const failure =
          status === "failed"
            ? (request as { result?: { summary?: unknown } }).result?.summary
            : undefined
        const line = this.insert({
          kind: "status",
          authorId: data.targetId,
          requestId: card.request_id ?? undefined,
          body: statusLineText(
            status,
            {
              approval: data.approval,
              targetAgentName: data.targetAgentName,
              decision: data.decision,
              review: data.review,
            },
            ownerName,
            typeof failure === "string" ? failure : undefined
          ),
          state: status,
          data: {
            outcome: status,
            agentId: data.targetAgentId,
          } satisfies StatusData,
          settled: true,
        })
        this.rewrite(card, { data: { ...data, outcomeSeq: line.seq } })
        return line
      }
      return undefined
    })
    if (outcome) this.announce(outcome)
  }

  /** Whether a request is between this conversation's two people, either way. */
  private isBetweenUs(fields: {
    requesterId: string
    targetId: string
  }): boolean {
    const { userA, userB } = this.ids
    return (
      (fields.requesterId === userA && fields.targetId === userB) ||
      (fields.requesterId === userB && fields.targetId === userA)
    )
  }

  /** The cards whose request can still change: waiting for a decision, for a result, or for its owner's review. */
  private openCards(): ItemRow[] {
    return this.sql<ItemRow>`SELECT * FROM side_items
      WHERE kind = 'request-card' AND state IN ('pending', 'approved', 'review')`
  }

  /**
   * Sets the next read of Go's requests while any is open and somebody is
   * here to read as: one at a time, `pollSeconds` on (the SDK keeps whole
   * seconds, never less than one). Nothing is set otherwise; a read already
   * set runs once, finds nothing to do, and sets nothing.
   */
  private async planPoll(): Promise<void> {
    if (this.meta("poll")) return
    if (this.openCards().length === 0 || !this.reader()) return
    const schedule = await this.schedule(sideTiming.pollSeconds, "pollRequests")
    this.setMeta("poll", schedule.id)
  }

  /** A socket whose person Go still takes: whom the object reads Go as. */
  private reader(): Actor | undefined {
    for (const connection of this.getConnections()) {
      const state = stateOf(connection)
      if (state && !state.expired) return { connection, state }
    }
    return undefined
  }

  /** A socket of `userId` whose cookie Go still takes. */
  private actorOf(userId: string): Actor | undefined {
    for (const connection of this.getConnections()) {
      const state = stateOf(connection)
      if (state?.userId === userId && !state.expired) {
        return { connection, state }
      }
    }
    return undefined
  }

  /** Reads the teamspace again as `state`'s person, for who is in the conversation now. */
  private async refreshRoster(state: SideConnectionState): Promise<void> {
    try {
      const space = await this.goAs<unknown>(
        state,
        organizationPath(
          this.ids.organizationId,
          "teamspaces",
          this.ids.teamspaceId
        )
      )
      const roster = rosterOf(space, this.ids)
      if (roster) this.setRoster(roster)
    } catch (error) {
      this.goFailed(state, error, "read its teamspace")
    }
  }

  // ---- Go ----

  /**
   * Asks Go as `state`'s person: their cookie, and for a change their page's
   * origin, since Go takes a cookie-authenticated change only from an
   * allowed one. Throws Go's refusal as an `ApiError`.
   */
  private async goAs<T>(
    state: SideConnectionState,
    path: string,
    options: {
      method?: "GET" | "POST"
      body?: unknown
      headers?: Record<string, string>
    } = {}
  ): Promise<T> {
    if (state.expired) throw new ApiError("Your session has ended.", 401)
    const method = options.method ?? "GET"
    const headers: Record<string, string> = {
      Cookie: state.cookie,
      ...options.headers,
    }
    if (options.body !== undefined) headers["Content-Type"] = "application/json"
    if (method !== "GET" && state.origin) headers.Origin = state.origin
    const response = await fetch(new URL(path, this.env.ORYXA_API_ORIGIN), {
      method,
      headers,
      body:
        options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: AbortSignal.timeout(sideTiming.requestTimeoutMs),
    })
    if (!response.ok) throw await requestError(response)
    const answer: T = await response.json()
    return answer
  }

  /** What Go's failure means: a cookie it no longer takes is not used again; anything else is logged. */
  private goFailed(
    state: SideConnectionState,
    error: unknown,
    what: string
  ): void {
    if (isExpiry(error)) {
      this.expire(state.cookie)
      return
    }
    console.error(`side conversation: could not ${what}`, error)
  }

  /** Go no longer takes `cookie`: its sockets are told, so their pages renew it and reopen. */
  private expire(cookie: string): void {
    for (const connection of this.getConnections()) {
      const state = stateOf(connection)
      if (!state || state.cookie !== cookie || state.expired) continue
      update(connection, { expired: true })
      send(connection, { type: "side-session-expired" })
    }
  }

  // ---- The transcript as agents read it ----

  /**
   * The conversation before `beforeSeq` (0: all of it), its last `limit`
   * items that say something, as lines an agent reads: a person by name, an
   * agent as "Leo (Priya's agent)", or "you" when it is `selfAgentId`.
   */
  private transcript(
    beforeSeq: number,
    limit: number,
    selfAgentId?: string
  ): TranscriptLine[] {
    const roster = this.roster()
    const rows =
      beforeSeq > 0
        ? this.sql<ItemRow>`SELECT * FROM side_items WHERE seq < ${beforeSeq}
            ORDER BY seq DESC LIMIT ${limit * 2}`
        : this.sql<ItemRow>`SELECT * FROM side_items
            ORDER BY seq DESC LIMIT ${limit * 2}`
    const lines: TranscriptLine[] = []
    for (const row of rows) {
      const line = this.line(row, roster, selfAgentId)
      if (line) lines.push(line)
      if (lines.length >= limit) break
    }
    return lines.reverse()
  }

  private line(
    row: ItemRow,
    roster: SideRoster,
    selfAgentId?: string
  ): TranscriptLine | undefined {
    switch (row.kind) {
      case "message":
        return row.body
          ? { speaker: personName(roster, row.author_id), text: row.body }
          : undefined
      case "agent-message": {
        if (!row.body || row.state === "streaming") return undefined
        const data = parseData<AgentData>(row)
        const speaker =
          selfAgentId && data.agentId === selfAgentId
            ? "you"
            : `${data.agentName} (${personName(roster, row.author_id)}'s agent)`
        return { speaker, text: row.body }
      }
      case "request-card": {
        const data = parseData<CardData>(row)
        const state = row.state ?? "pending"
        return {
          speaker: personName(roster, row.author_id),
          text: `asked ${data.targetAgentName} (${personName(roster, data.targetId)}'s agent), which is ${requestStateWords[state] ?? state}: ${row.body}`,
        }
      }
      case "status":
        return { speaker: "(the conversation)", text: row.body }
    }
  }

  // ---- Storage ----

  /**
   * Stores an item and what it changes, synchronously, so that it is
   * written as one: the item, the oldest dropped past the limit, and the
   * conversation's run.
   */
  private insert(item: {
    kind: ItemKind
    authorId: string
    clientId?: string
    requestId?: string
    state?: string
    body: string
    data: object
    sessionAt?: string
    settled: boolean
  }): ItemRow {
    const now = new Date().toISOString()
    const row: ItemRow = {
      seq: this.nextSeq(),
      id: `sdi_${randomId()}`,
      kind: item.kind,
      author_id: item.authorId,
      client_id: item.clientId ?? null,
      request_id: item.requestId ?? null,
      state: item.state ?? null,
      body: item.body,
      data: JSON.stringify(item.data),
      created_at: now,
      updated_at: now,
      session_at: item.sessionAt ?? null,
      settled: item.settled ? 1 : 0,
      noted: 0,
    }
    this.sql`INSERT INTO side_items (seq, id, kind, author_id, client_id,
        request_id, state, body, data, created_at, updated_at, session_at,
        settled, noted)
      VALUES (${row.seq}, ${row.id}, ${row.kind}, ${row.author_id},
        ${row.client_id}, ${row.request_id}, ${row.state}, ${row.body},
        ${row.data}, ${row.created_at}, ${row.updated_at}, ${row.session_at},
        ${row.settled}, 0)`
    if (row.seq > SIDE_HISTORY_LIMIT) {
      this
        .sql`DELETE FROM side_items WHERE seq <= ${row.seq - SIDE_HISTORY_LIMIT}`
    }
    if (!this.meta("conversation")) {
      this.setMeta("conversation", `sdc_${randomId()}`)
    }
    return row
  }

  /** Changes a stored item, and returns it as it now stands. */
  private rewrite(
    row: ItemRow,
    change: {
      body?: string
      state?: string
      data?: object
      settled?: number
      clientId?: string
    }
  ): ItemRow {
    const next: ItemRow = {
      ...row,
      ...(change.body !== undefined ? { body: change.body } : {}),
      ...(change.state !== undefined ? { state: change.state } : {}),
      ...(change.data !== undefined
        ? { data: JSON.stringify(change.data) }
        : {}),
      ...(change.settled !== undefined ? { settled: change.settled } : {}),
      ...(change.clientId !== undefined ? { client_id: change.clientId } : {}),
      updated_at: new Date().toISOString(),
    }
    this.sql`UPDATE side_items SET body = ${next.body}, state = ${next.state},
        data = ${next.data}, settled = ${next.settled},
        client_id = ${next.client_id}, updated_at = ${next.updated_at}
      WHERE seq = ${row.seq}`
    return next
  }

  private item(seq: number): ItemRow | undefined {
    return this.sql<ItemRow>`SELECT * FROM side_items WHERE seq = ${seq}`.at(0)
  }

  private itemByClient(author: string, clientId: string): ItemRow | undefined {
    return this.sql<ItemRow>`SELECT * FROM side_items
      WHERE author_id = ${author} AND client_id = ${clientId}`.at(0)
  }

  private cardRow(requestId: string): ItemRow | undefined {
    return this.sql<ItemRow>`SELECT * FROM side_items
      WHERE kind = 'request-card' AND request_id = ${requestId}`.at(0)
  }

  /** The agent's message answering the question `seq`. */
  private answerTo(seq: number): ItemRow | undefined {
    return this.sql<ItemRow>`SELECT * FROM side_items
      WHERE kind = 'agent-message' AND seq > ${seq}
      ORDER BY seq ASC LIMIT 20`.find(
      (row) => parseData<AgentData>(row).replyTo === seq
    )
  }

  /** Who is in the conversation, as the latest socket brought it. */
  private roster(): SideRoster {
    const held = this.meta("roster")
    if (!held) return emptyRoster
    try {
      return parseRoster(JSON.parse(held)) ?? emptyRoster
    } catch {
      return emptyRoster
    }
  }

  /** Keeps the roster Go answered with last, and tells every socket when it changed. */
  private setRoster(roster: SideRoster): void {
    const next = JSON.stringify(roster)
    if (this.meta("roster") === next) return
    this.setMeta("roster", next)
    this.tell({ type: "side-roster", roster })
  }

  // ---- Reading, typing and presence (as a DM's) ----

  /** `reader` read through `seq` (at most the latest): both sockets and their inbox are told. */
  private async read(reader: string, seq: number): Promise<void> {
    const through = Math.min(seq, this.latestSeq())
    if (through <= this.readSeq(reader)) return
    this.setRead(reader, through)
    this.tell({ type: "side-read", userId: reader, seq: through })
    await this.events.plan()
    await this.syncInboxes()
  }

  private see(
    connection: Connection,
    state: SideConnectionState,
    visible: boolean
  ): void {
    if (state.visible === visible) return
    const before = this.isOnline(state.userId)
    if (!update(connection, { visible })) return
    const after = this.isOnline(state.userId)
    if (before !== after) {
      this.tell({ type: "side-presence", userId: state.userId, online: after })
    }
  }

  private startTyping(user: string): void {
    const held = this.typing.get(user)
    if (held) clearTimeout(held.timer)
    const timer = setTimeout(() => this.stopTyping(user), SIDE_TYPING_TTL_MS)
    const now = Date.now()
    if (held && now - held.told < dmTypingRetellMs) {
      this.typing.set(user, { timer, told: held.told })
      return
    }
    this.typing.set(user, { timer, told: now })
    this.tell(
      { type: "side-typing", userId: user, typing: true },
      (state) => state.userId !== user
    )
  }

  private stopTyping(user: string, without?: string): void {
    const held = this.typing.get(user)
    if (!held) return
    clearTimeout(held.timer)
    this.typing.delete(user)
    this.tell(
      { type: "side-typing", userId: user, typing: false },
      (state) => state.userId !== user,
      without
    )
  }

  /** Whether a message from `user` may go now; takes one of their sends if so (a DM's limit, shared by asks). */
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

  // ---- Inboxes ----

  /**
   * Tells both inboxes what they have not been told: each item that has
   * settled (the latest `maxNotedPerUpdate` of them), then how far their
   * person has read; after a purge that kept something, what stays, in
   * place of what they had. One run at a time; a change during it runs it
   * again. What fails is tried again on a schedule.
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
    // The alarm first: an instance lost before the inboxes answer still
    // tells them once the alarm wakes it.
    await this.armRetry()
    if (this.epoch !== epoch) return
    const results = await Promise.allSettled([
      this.syncInbox(ids, ids.userA, 1, epoch),
      this.syncInbox(ids, ids.userB, 2, epoch),
    ])
    if (this.epoch !== epoch) return
    const failed = results.filter((result) => result.status === "rejected")
    for (const failure of failed) {
      console.error(
        "side conversation: could not update an inbox",
        failure.reason
      )
    }
    if (failed.length > 0) return
    this.deleteMeta("retry_attempt")
    await this.disarmRetry()
  }

  private entryFor(ids: SideConversationIds, user: string): ConversationEntry {
    const teamspaceName = this.roster().teamspaceName
    return {
      kind: "side",
      name: this.name,
      peerId: peerIn(ids, user) ?? "",
      teamspaceId: ids.teamspaceId,
      ...(teamspaceName ? { teamspaceName } : {}),
    }
  }

  private async syncInbox(
    ids: SideConversationIds,
    user: string,
    bit: number,
    epoch: number
  ): Promise<void> {
    const restate = this.meta(`restate:${user}`) === "1"
    const known = this.meta(`known:${user}`) === "1"
    const readTold = this.readTold(user)
    const fresh = restate
      ? []
      : this.sql<ItemRow>`SELECT * FROM side_items
          WHERE settled = 1 AND (noted & ${bit}) = 0
          ORDER BY seq DESC LIMIT ${maxNotedPerUpdate}`.reverse()
    const read = this.readSeq(user)
    if (!restate && known && fresh.length === 0 && read <= readTold) return
    const inbox = await getAgentByName(
      this.env.InboxAgent,
      inboxName(ids.organizationId, user)
    )
    if (this.epoch !== epoch) return
    const entry = this.entryFor(ids, user)
    if (restate) {
      // After a purge that kept something: the inbox is told what stays, in
      // place of what it had.
      await inbox.restateConversation(entry, this.restated(user))
      if (this.epoch !== epoch) return
      this.sql`UPDATE side_items SET noted = noted | ${bit} WHERE settled = 1`
      this.setReadTold(user, read)
      this.setMeta(`known:${user}`, "1")
      this.deleteMeta(`restate:${user}`)
      return
    }
    const last = fresh.at(-1)
    if (last) {
      await inbox.noteMessage(entry, ...fresh.map((row) => this.noted(row)))
      if (this.epoch !== epoch) return
      // Older ones past the update's size are left untold: the latest stand.
      this.sql`UPDATE side_items SET noted = noted | ${bit}
        WHERE settled = 1 AND seq <= ${last.seq}`
    } else if (!known) {
      // Opened without an item yet: known to the inbox, listed by none.
      await inbox.knowConversation(entry)
      if (this.epoch !== epoch) return
    }
    if (!known) this.setMeta(`known:${user}`, "1")
    if (read > readTold) {
      // An inbox reads through its member's own item as it is noted: a read
      // that goes no further than that needs no word of its own.
      const readByNote =
        fresh.filter((row) => this.noted(row).authorId === user).at(-1)?.seq ??
        0
      if (read > readByNote) {
        await inbox.markRead(this.name, read)
        if (this.epoch !== epoch) return
      }
      this.setReadTold(user, read)
    }
  }

  /**
   * An item as an inbox is told of it. Its author is whose side it is to
   * that inbox: a person's own message or request reads through for them,
   * while an agent's message and a quiet line are the agent's, and count as
   * unread for both until read. The preview names the agent.
   */
  private noted(row: ItemRow): NotedMessage {
    switch (row.kind) {
      case "message":
        return {
          seq: row.seq,
          authorId: row.author_id,
          body: preview(row.body),
          createdAt: row.created_at,
        }
      case "agent-message": {
        const data = parseData<AgentData>(row)
        return {
          seq: row.seq,
          authorId: data.agentId,
          body: preview(`${data.agentName}: ${row.body || data.error || ""}`),
          // An answer counts from when it finished.
          createdAt: row.updated_at,
        }
      }
      case "request-card": {
        const data = parseData<CardData>(row)
        return {
          seq: row.seq,
          authorId: row.author_id,
          body: preview(`Asked ${data.targetAgentName}: ${row.body}`),
          createdAt: row.created_at,
        }
      }
      case "status": {
        const data = parseData<StatusData>(row)
        return {
          seq: row.seq,
          authorId: data.agentId || row.author_id,
          body: preview(row.body),
          createdAt: row.created_at,
        }
      }
    }
  }

  /** What a person's inbox keeps of the conversation, restated from what it holds now. */
  private restated(user: string): RestatedConversation {
    const settled = this.sql<ItemRow>`SELECT * FROM side_items
      WHERE settled = 1 ORDER BY seq DESC LIMIT ${MAX_UNREAD + 1}`
    const last = settled.at(0)
    const readSeq = this.readSeq(user)
    const unread = settled
      .filter((row) => row.seq > readSeq && this.noted(row).authorId !== user)
      .map((row) => row.seq)
      .slice(0, MAX_UNREAD)
      .reverse()
    return { ...(last ? { last: this.noted(last) } : {}), readSeq, unread }
  }

  /**
   * Whether an inbox has yet to be told something: that the conversation
   * exists, once it has opened, an item or a read since, or what stayed
   * after a purge.
   */
  private syncPending(): boolean {
    const latest = this.latestSeq()
    if (latest === 0 && this.meta("opened") !== "1") return false
    const { userA, userB } = this.ids
    return [
      [userA, 1],
      [userB, 2],
    ].some(([user, bit]) => {
      const who = user as string
      return (
        this.meta(`restate:${who}`) === "1" ||
        this.meta(`known:${who}`) !== "1" ||
        this.readTold(who) < this.readSeq(who) ||
        (this.sql<{ untold: number }>`SELECT EXISTS (
          SELECT 1 FROM side_items WHERE settled = 1 AND (noted & ${bit}) = 0
        ) AS untold`.at(0)?.untold ?? 0) === 1
      )
    })
  }

  /** Makes sure an alarm will run the sync again, backing off as a DM's does. */
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

  private readTold(user: string): number {
    return (
      this.sql<{ read_seq: number }>`SELECT read_seq FROM side_inbox_sync
        WHERE user_id = ${user}`.at(0)?.read_seq ?? 0
    )
  }

  private setReadTold(user: string, seq: number): void {
    this
      .sql`INSERT INTO side_inbox_sync (user_id, read_seq) VALUES (${user}, ${seq})
      ON CONFLICT (user_id) DO UPDATE SET read_seq = excluded.read_seq`
  }

  // ---- Purge ----

  /** A purge's scope checked against this conversation: one of its people, and an instant. */
  private checkedScope(scope: SidePurgeScope): SidePurgeScope {
    const { userId, removedAt } = scope as {
      [Key in keyof SidePurgeScope]?: unknown
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

  /**
   * Empties the conversation, stops its runs and lets its schedules go: all
   * it keeps but how far each person read and the highest seq used, so a
   * later run never uses a seq again nor moves a read back.
   */
  private async reset(): Promise<void> {
    for (const run of this.runs.values()) run.controller.abort()
    this.runs.clear()
    this.drafts.clear()
    const retry = this.meta("retry")
    const poll = this.meta("poll")
    this.keepSeqFloor()
    this.sql`DELETE FROM side_items`
    this.sql`DELETE FROM side_inbox_sync`
    this.sql`DELETE FROM side_threads`
    this.sql`DELETE FROM side_meta WHERE key != 'seq_floor'`
    if (retry) await this.cancelSchedule(retry).catch(() => false)
    if (poll) await this.cancelSchedule(poll).catch(() => false)
  }

  /**
   * Tells the sockets `ending` picks (and any never let in) that this run of
   * the conversation is over, and closes them: each page asks the Worker
   * whether it may open the conversation again.
   */
  private endSockets(ending: (state: SideConnectionState) => boolean): void {
    for (const connection of this.getConnections()) {
      const state = stateOf(connection)
      if (state && !ending(state)) continue
      send(connection, { type: "side-gone" })
      try {
        connection.close(sideGoneCloseCode, "Gone")
      } catch {
        // Already closed.
      }
    }
  }

  // ---- Small things ----

  /** The newest item's seq, or 0 without one. */
  private latestSeq(): number {
    return (
      this.sql<{
        seq: number | null
      }>`SELECT MAX(seq) AS seq FROM side_items`.at(0)?.seq ?? 0
    )
  }

  /**
   * The next item's seq: one past the highest ever used, which is the
   * newest item's, or the one a purge kept when it took the newest away
   * (`keepSeqFloor`). A conversation from before any purge goes on from
   * its newest item.
   */
  private nextSeq(): number {
    return Math.max(this.latestSeq(), this.seqFloor()) + 1
  }

  /** The highest seq a purge took away, which no item takes again. */
  private seqFloor(): number {
    return Number(this.meta("seq_floor") ?? "0")
  }

  /**
   * Before a purge takes items away, the newest perhaps: the highest seq
   * used so far is kept, and only ever goes up. Nothing else takes the
   * newest item away (the history limit drops the oldest), so an item
   * never reuses a seq Go was told of, nor one a read already covers.
   */
  private keepSeqFloor(): void {
    const latest = this.latestSeq()
    if (latest > this.seqFloor()) this.setMeta("seq_floor", String(latest))
  }

  private readSeq(user: string): number {
    return (
      this.sql<{
        seq: number
      }>`SELECT seq FROM side_reads WHERE user_id = ${user}`.at(0)?.seq ?? 0
    )
  }

  /**
   * Reads `user` through `seq`, only ever further, and records it for Go in
   * the same transaction when it goes further, unless `record` says the
   * read is an event's already (an author's through their own message).
   */
  private setRead(user: string, seq: number, record = true): void {
    this.transaction(() => {
      const before = this.readSeq(user)
      this.sql`INSERT INTO side_reads (user_id, seq) VALUES (${user}, ${seq})
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

  private reads(): SideRead[] {
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
    message: SideServerMessage,
    to?: (state: SideConnectionState) => boolean,
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
    return this.sql<{
      value: string
    }>`SELECT value FROM side_meta WHERE key = ${key}`.at(0)?.value
  }

  private setMeta(key: string, value: string): void {
    this.sql`INSERT INTO side_meta (key, value) VALUES (${key}, ${value})
      ON CONFLICT (key) DO UPDATE SET value = excluded.value`
  }

  private deleteMeta(key: string): void {
    this.sql`DELETE FROM side_meta WHERE key = ${key}`
  }
}
