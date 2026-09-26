// What travels on a side conversation's sockets (src/side-conversation-agent.ts
// and useSideConversation), and the limits both sides keep. As on a DM's
// socket, every message is a JSON object with a `type`; each side parses
// what it is sent and drops anything else, the Agents SDK's own frames
// included. A person's message keeps the DM's limits, so a view can treat
// both alike.
import {
  DM_HISTORY_LIMIT,
  DM_MAX_BODY_LENGTH,
  DM_MAX_PAGE_SIZE,
  DM_PAGE_SIZE,
  DM_TYPING_REFRESH_MS,
  DM_TYPING_TTL_MS,
  codePoints,
  conversationGoneCloseCode,
  isClientId,
} from "@/lib/dms/protocol"

export { codePoints, isClientId, preview } from "@/lib/dms/protocol"

/** The longest message a person sends, or asks an agent, in Unicode code points after trimming. */
export const SIDE_MAX_BODY_LENGTH = DM_MAX_BODY_LENGTH

/** How many items a socket is sent as it opens, and a history page holds unless it asks for fewer. */
export const SIDE_PAGE_SIZE = DM_PAGE_SIZE

/** The most items one history page holds. */
export const SIDE_MAX_PAGE_SIZE = DM_MAX_PAGE_SIZE

/** The most items a conversation keeps: past this, the oldest go as new ones arrive. */
export const SIDE_HISTORY_LIMIT = DM_HISTORY_LIMIT

/** How long "typing" lasts after the last word of it, on the object and on a page. */
export const SIDE_TYPING_TTL_MS = DM_TYPING_TTL_MS

/** How often a page that is still typing says so again. */
export const SIDE_TYPING_REFRESH_MS = DM_TYPING_REFRESH_MS

/**
 * The longest agent message kept, in code points: a request's result is at
 * most Go's 4,000-character summary, a blank line and its 20,000-character
 * content. An answer longer than this is cut, and ends with "…".
 */
export const SIDE_MAX_AGENT_TEXT = 4_000 + 2 + 20_000

/** The longest note a decision carries (Go's `maxMessageRunes`). */
export const SIDE_MAX_DECISION_MESSAGE = 4_000

/** The most tools a decision allows, and the longest name of one, in code points (Go's `maxListItems` and `validateList`). */
export const SIDE_MAX_TOOLS = 32
export const SIDE_MAX_TOOL_NAME = 120

/**
 * The close code of a socket whose conversation was purged (a member left,
 * or the organization was deleted). The SDK does not reopen a socket closed
 * with it.
 */
export const sideGoneCloseCode = conversationGoneCloseCode

// A page's id for a history request.
const requestIdPattern = /^[\w-]{1,64}$/

// Go's ids for an agent and an agent request: printable, no spaces.
const goIdPattern = /^[\w-]{1,128}$/

// The largest frame the object reads from a page: a message's 4,000 code
// points are at most 16,000 UTF-16 units escaped, with room to spare.
const maxClientFrame = 64 * 1024

/**
 * A request's state, as Go has it: `expired` is a pending or approved one
 * past its time; `review` holds its finished answer for its target's owner
 * to review (the answer guard found personal data in it), and `withheld` is
 * one they did not share, or that expired before they reviewed it.
 */
export type SideRequestStatus =
  | "pending"
  | "approved"
  | "review"
  | "rejected"
  | "completed"
  | "failed"
  | "expired"
  | "withheld"

/** How a request that ended without a result ended, as its quiet line says. */
export type SideOutcome = "rejected" | "failed" | "expired" | "withheld"

/** The owner's choice for an answer held for review: share it, share it fully masked, or not at all. */
export type SideReviewAction = "send" | "send_masked" | "withhold"

/**
 * What the answer guard found in a request's answer (Go's `guard`): only
 * kinds it always masks (`masked`), a kind it held for review (`held`), or
 * nothing (`clear`). `kinds` are entity type names, never what matched;
 * `UNCHECKED` when the check could not run.
 */
export type SideGuard = {
  state: "clear" | "masked" | "held"
  kinds: string[]
}

/**
 * An answer held for review, as its owner alone is shown it: the answer as
 * it would be shared (`stored`: the kinds the guard always masks already
 * masked), and with every kind it found masked too (`masked`, absent for an
 * answer the guard could not check, which has no masked version). `failed`
 * marks an answer that was its agent's failure: shared, it fails the
 * request with it.
 */
export type SideHeldAnswer = { stored: string; masked?: string; failed?: true }

/**
 * Who decides a request: its target's owner themself (`owner`: a teammate's
 * primary agent, or a specialist they contributed with `request`
 * delegation), or the agent itself (`agent`: a specialist contributed with
 * `automatic` delegation). Go fixes it when the request is made.
 */
export type SideApproval = "owner" | "agent"

/** How an agent's message stands. */
export type SideAgentMessageStatus =
  /** Its run is answering: `side-delta` frames add to it. */
  | "streaming"
  /** Its run waits for its owner to confirm a tool call; only the owner is shown what. */
  | "waiting"
  | "done"
  /** The run was refused or broke off: `error` says why, `code` is Go's refusal code or `session_expired`. */
  | "failed"

/** A tool call the owner's agent waits on the owner's word for. */
export type SideConfirmation = {
  /** What the owner's answer names. */
  callId: string
  tool: string
  /** What the tool would be called with: nobody approves what they cannot see. */
  arguments?: Record<string, unknown>
  /** Why it asks, in the tool's own words, when it has them. */
  hint?: string
}

/** A person's message. */
export type SideWireMessage = {
  kind: "message"
  id: string
  /** The conversation's own order over every kind of item: never used twice, even after a purge. */
  seq: number
  authorId: string
  body: string
  /** When the object stored it: ISO 8601, UTC, milliseconds. */
  createdAt: string
  /** The page's id for it; on the author's own items only. */
  clientId?: string
  /** It asked the author's own agent (`askMyAgent`): that agent's id. Its answer follows, `replyTo` this seq. */
  asksAgentId?: string
}

/** An agent's message: an answer to its owner (`askMyAgent`), or a request's result. */
export type SideWireAgentMessage = {
  kind: "agent-message"
  id: string
  seq: number
  agentId: string
  agentName: string
  /** Whose agent it is. */
  ownerId: string
  /** What it said, so far while `streaming`. */
  body: string
  createdAt: string
  updatedAt: string
  status: SideAgentMessageStatus
  error?: string
  code?: string
  /** An answer to its owner: the seq of the message that asked. */
  replyTo?: number
  /**
   * A request's result: the request, the member the agent worked for (its
   * owner), who approved it (the owner, or nobody when the agent decided
   * itself: `automatic` delegation), and who reviewed it before it was
   * shared, when the answer guard held it for them.
   */
  via?: SideVia
  /** While `waiting`, what the run waits on. Sent to its owner's sockets only. */
  confirmations?: SideConfirmation[]
}

/** Whom a request's result was for, and who approved and reviewed it. */
export type SideVia = {
  requestId: string
  onBehalfOf: string
  approvedBy?: string
  reviewedBy?: string
}

/** A request to a teammate's agent, mirroring Go's (`agents.Interaction`) as the object last read it. */
export type SideWireRequestCard = {
  kind: "request-card"
  id: string
  seq: number
  requestId: string
  /** The member who asked, and the agent Go records as asking for them. */
  requesterId: string
  requesterAgentId: string
  requesterAgentName: string
  /** The member whose agent was asked. */
  targetId: string
  targetAgentId: string
  targetAgentName: string
  approval: SideApproval
  status: SideRequestStatus
  task: string
  createdAt: string
  /** When its status last changed here. */
  updatedAt: string
  expiresAt: string
  decision?: SideDecision
  /** The requester's page id, on their own card, when it was asked for here. */
  clientId?: string
  /** What the request asks to use (Go's `requested_tools`): toolbox names, `apps` and `knowledge`. Absent when none. */
  requestedTools?: string[]
  /** What the answer guard found in its answer. On its target's owner's copy only. */
  guard?: SideGuard
  /** While `review`, the answer held for its target's owner, on their copy only, once the object has read it as them. */
  heldAnswer?: SideHeldAnswer
  /** How a held answer was reviewed: by its target's owner, or withheld by Oryxa (`system`), saying why. */
  review?: SideReview
}

/**
 * A request's decision: `by` its target's owner, their agent (whose
 * message is fixed words, never its own), or Oryxa (`system`, closing a
 * request that no longer stands, its message saying why); absent from a
 * decision Go recorded no decider for.
 */
export type SideDecision = {
  decision: "approved" | "rejected"
  message: string
  decidedAt: string
  by?: "owner" | "agent" | "system"
}

/**
 * A held answer's review: its target's owner's (`owner`), or Oryxa's
 * withholding (`system`, `withhold`), with Go's `reason`: "expired before
 * review", or why the request no longer stood in its teamspace.
 */
export type SideReview = {
  action: SideReviewAction
  decidedAt: string
  by: "owner" | "system"
  reason?: string
}

/** A quiet line: a request declined, failed or expired, or an answer not shared. */
export type SideWireStatusLine = {
  kind: "status"
  id: string
  seq: number
  requestId: string
  outcome: SideOutcome
  /** Whose side it is: the target agent's owner. */
  authorId: string
  /** The object's words, which name who did what. */
  text: string
  createdAt: string
}

/** One item of the transcript, as the object sends it to one person. */
export type SideWireItem =
  | SideWireMessage
  | SideWireAgentMessage
  | SideWireRequestCard
  | SideWireStatusLine

/** A teammate as the teamspace lists them: their primary agent's seat. */
export type SideRosterPerson = {
  userId: string
  displayName?: string
  agentId: string
  agentName: string
}

/** A specialist one of the two contributed to the teamspace. */
export type SideRosterAgent = {
  agentId: string
  ownerId: string
  name: string
  role?: string
  delegationMode: "request" | "automatic"
}

/**
 * Who is in the conversation, from Go's `GET …/teamspaces/{id}` as the
 * Worker read it for the latest socket: the teamspace's name and whether it
 * is paused, the two people's primary agents, and the active specialists
 * either contributed there.
 */
export type SideRoster = {
  teamspaceName: string
  paused: boolean
  people: SideRosterPerson[]
  contributions: SideRosterAgent[]
}

/** How far a person has read: through `seq`, 0 for nothing. */
export type SideRead = { userId: string; seq: number }

/** Why the object refused what a page sent, asked or answered. */
export type SideFailure =
  | "empty"
  | "too_long"
  | "rate_limited"
  /** The asker's own agent is still answering here. */
  | "busy"
  /** Too many of the asker's requests wait here already. */
  | "too_many_requests"
  /** Not the asker's to do: not the teammate's agent here, not theirs to decide or confirm, or Go refused it (its words). */
  | "not_allowed"
  /** The request was decided already, or expired. */
  | "already_decided"
  /** The socket's cookie expired before anything was done: the page renews it, reopens and sends again, once. */
  | "session_expired"
  /** Go or the object's storage failed; try again. */
  | "unavailable"

/** What a page says on a side conversation's socket. */
export type SideClientMessage =
  /** Whether the page's tab is visible: first on every socket, then on every change. */
  | { type: "side-visibility"; visible: boolean }
  /** A message from the socket's own person, whoever the page says; stored once per author and `clientId`. */
  | { type: "side-send"; clientId: string; body: string }
  /** A message that asks the socket's person's own agent, which answers for both to see. */
  | { type: "side-ask-mine"; clientId: string; body: string }
  /** Asks the teammate's agent `agentId` for something: an agent request, filed as the socket's person. */
  | {
      type: "side-ask-teammate"
      clientId: string
      agentId: string
      body: string
    }
  /** The owner's answers to every confirmation their agent's message `seq` waits on. */
  | {
      type: "side-confirm"
      seq: number
      answers: Array<{ id: string; confirmed: boolean }>
    }
  /**
   * The target's owner decides a request held for them, from their own
   * socket. An approval allows `allowedTools`, a subset of the request's
   * `requestedTools`; without them it allows none.
   */
  | {
      type: "side-decide"
      requestId: string
      decision: "approved" | "rejected"
      message?: string
      allowedTools?: string[]
    }
  /** The target's owner reviews an answer held for them, from their own socket. */
  | { type: "side-review"; requestId: string; action: SideReviewAction }
  /** Up to `limit` items before `beforeSeq` (0: the latest). */
  | {
      type: "side-history"
      requestId: string
      beforeSeq: number
      limit: number
    }
  /** The socket's person has read through `seq`. */
  | { type: "side-read"; seq: number }
  /** The socket's person is typing, or stopped. Never stored. */
  | { type: "side-typing"; typing: boolean }

/** What the object says on a side conversation's socket. */
export type SideServerMessage =
  /**
   * The conversation as the socket opens: its latest page of items, whether
   * older ones exist, both read states, who is online and typing, and who is
   * in it. `conversation` names this run of it: it changes after a purge.
   */
  | {
      type: "side-snapshot"
      conversation: string
      items: SideWireItem[]
      hasOlder: boolean
      reads: SideRead[]
      online: string[]
      typing: string[]
      roster: SideRoster
    }
  /** A new item, or one that changed (same seq): to every socket, or a stored one to a socket that sent its `clientId` again. */
  | { type: "side-item"; item: SideWireItem }
  /** More of a streaming agent message: `text` goes at `at` (its body's length before it); a page that holds another length waits for the next `side-item`. */
  | { type: "side-delta"; seq: number; at: number; text: string }
  /** The object refused what a page sent or asked under `clientId`; only that socket hears. */
  | {
      type: "side-failed"
      clientId: string
      code: SideFailure
      message: string
    }
  /** The object refused a decision, a review or a confirmation; only that socket hears. */
  | {
      type: "side-refused"
      /** The request decided or reviewed, or the agent message confirmed (its seq, as a string). */
      target: string
      code: SideFailure
      message: string
    }
  /** A history page, oldest first, to the socket that asked. */
  | {
      type: "side-history"
      requestId: string
      items: SideWireItem[]
      hasOlder: boolean
    }
  | ({ type: "side-read" } & SideRead)
  | { type: "side-typing"; userId: string; typing: boolean }
  | { type: "side-presence"; userId: string; online: boolean }
  /** Who is in the conversation changed. */
  | { type: "side-roster"; roster: SideRoster }
  /** Go no longer takes this socket's cookie: the page renews its session and reopens the socket. */
  | { type: "side-session-expired" }
  /** The conversation was purged; the socket closes next with `sideGoneCloseCode`. */
  | { type: "side-gone" }

const failures = new Set<string>([
  "empty",
  "too_long",
  "rate_limited",
  "busy",
  "too_many_requests",
  "not_allowed",
  "already_decided",
  "session_expired",
  "unavailable",
])

const requestStatuses = new Set<string>([
  "pending",
  "approved",
  "review",
  "rejected",
  "completed",
  "failed",
  "expired",
  "withheld",
])

const outcomes = new Set<string>(["rejected", "failed", "expired", "withheld"])

const reviewActions = new Set<string>(["send", "send_masked", "withhold"])

const guardStates = new Set<string>(["clear", "masked", "held"])

const agentStatuses = new Set<string>([
  "streaming",
  "waiting",
  "done",
  "failed",
])

/** A page's message to a side conversation, or nothing for anything the object does not take. */
export function parseSideClientMessage(
  data: unknown
): SideClientMessage | undefined {
  const value = parsedObject(data, maxClientFrame)
  if (!value) return undefined
  switch (value.type) {
    case "side-visibility":
      return typeof value.visible === "boolean"
        ? { type: "side-visibility", visible: value.visible }
        : undefined
    case "side-send":
      return isClientId(value.clientId) && typeof value.body === "string"
        ? { type: "side-send", clientId: value.clientId, body: value.body }
        : undefined
    case "side-ask-mine":
      return isClientId(value.clientId) && typeof value.body === "string"
        ? { type: "side-ask-mine", clientId: value.clientId, body: value.body }
        : undefined
    case "side-ask-teammate":
      return isClientId(value.clientId) &&
        isGoId(value.agentId) &&
        typeof value.body === "string"
        ? {
            type: "side-ask-teammate",
            clientId: value.clientId,
            agentId: value.agentId,
            body: value.body,
          }
        : undefined
    case "side-confirm": {
      if (!isSeq(value.seq) || value.seq === 0) return undefined
      const answers = confirmationAnswers(value.answers)
      return answers
        ? { type: "side-confirm", seq: value.seq, answers }
        : undefined
    }
    case "side-decide": {
      const allowedTools =
        value.allowedTools === undefined
          ? undefined
          : toolNames(value.allowedTools)
      if (
        !isGoId(value.requestId) ||
        (value.decision !== "approved" && value.decision !== "rejected") ||
        (value.message !== undefined && typeof value.message !== "string") ||
        (value.allowedTools !== undefined && !allowedTools)
      ) {
        return undefined
      }
      return {
        type: "side-decide",
        requestId: value.requestId,
        decision: value.decision,
        ...(typeof value.message === "string"
          ? { message: value.message }
          : {}),
        ...(allowedTools ? { allowedTools } : {}),
      }
    }
    case "side-review":
      return isGoId(value.requestId) &&
        typeof value.action === "string" &&
        reviewActions.has(value.action)
        ? {
            type: "side-review",
            requestId: value.requestId,
            action: value.action as SideReviewAction,
          }
        : undefined
    case "side-history":
      if (
        typeof value.requestId !== "string" ||
        !requestIdPattern.test(value.requestId) ||
        !isSeq(value.beforeSeq) ||
        !isSeq(value.limit)
      ) {
        return undefined
      }
      return {
        type: "side-history",
        requestId: value.requestId,
        beforeSeq: value.beforeSeq,
        limit: Math.min(Math.max(value.limit, 1), SIDE_MAX_PAGE_SIZE),
      }
    case "side-read":
      return isSeq(value.seq)
        ? { type: "side-read", seq: value.seq }
        : undefined
    case "side-typing":
      return typeof value.typing === "boolean"
        ? { type: "side-typing", typing: value.typing }
        : undefined
    default:
      return undefined
  }
}

/** The object's message on a side conversation's socket, or nothing for anything else there. */
export function parseSideServerMessage(
  data: unknown
): SideServerMessage | undefined {
  const value = parsedObject(data)
  if (!value) return undefined
  switch (value.type) {
    case "side-snapshot": {
      const items = wireItems(value.items)
      const reads = Array.isArray(value.reads)
        ? (value.reads as unknown[]).filter(isRead)
        : undefined
      const roster = parseRoster(value.roster)
      if (
        typeof value.conversation !== "string" ||
        !items ||
        typeof value.hasOlder !== "boolean" ||
        !reads ||
        !isStrings(value.online) ||
        !isStrings(value.typing) ||
        !roster
      ) {
        return undefined
      }
      return {
        type: "side-snapshot",
        conversation: value.conversation,
        items,
        hasOlder: value.hasOlder,
        reads: reads.map(({ userId, seq }) => ({ userId, seq })),
        online: value.online,
        typing: value.typing,
        roster,
      }
    }
    case "side-item": {
      const item = wireItem(value.item)
      return item ? { type: "side-item", item } : undefined
    }
    case "side-delta":
      return isSeq(value.seq) &&
        value.seq > 0 &&
        isSeq(value.at) &&
        typeof value.text === "string"
        ? { type: "side-delta", seq: value.seq, at: value.at, text: value.text }
        : undefined
    case "side-failed":
      return isClientId(value.clientId) &&
        typeof value.code === "string" &&
        failures.has(value.code) &&
        typeof value.message === "string"
        ? {
            type: "side-failed",
            clientId: value.clientId,
            code: value.code as SideFailure,
            message: value.message,
          }
        : undefined
    case "side-refused":
      return typeof value.target === "string" &&
        typeof value.code === "string" &&
        failures.has(value.code) &&
        typeof value.message === "string"
        ? {
            type: "side-refused",
            target: value.target,
            code: value.code as SideFailure,
            message: value.message,
          }
        : undefined
    case "side-history": {
      const items = wireItems(value.items)
      if (
        typeof value.requestId !== "string" ||
        !items ||
        typeof value.hasOlder !== "boolean"
      ) {
        return undefined
      }
      return {
        type: "side-history",
        requestId: value.requestId,
        items,
        hasOlder: value.hasOlder,
      }
    }
    case "side-read":
      return isRead(value)
        ? { type: "side-read", userId: value.userId, seq: value.seq }
        : undefined
    case "side-typing":
      return typeof value.userId === "string" &&
        typeof value.typing === "boolean"
        ? { type: "side-typing", userId: value.userId, typing: value.typing }
        : undefined
    case "side-presence":
      return typeof value.userId === "string" &&
        typeof value.online === "boolean"
        ? { type: "side-presence", userId: value.userId, online: value.online }
        : undefined
    case "side-roster": {
      const roster = parseRoster(value.roster)
      return roster ? { type: "side-roster", roster } : undefined
    }
    case "side-session-expired":
      return { type: "side-session-expired" }
    case "side-gone":
      return { type: "side-gone" }
    default:
      return undefined
  }
}

/** A roster as the object keeps and sends it, or nothing for anything else. Only its own fields are kept. */
export function parseRoster(value: unknown): SideRoster | undefined {
  if (!isObject(value)) return undefined
  const { teamspaceName, paused, people, contributions } = value
  if (
    typeof teamspaceName !== "string" ||
    typeof paused !== "boolean" ||
    !Array.isArray(people) ||
    !Array.isArray(contributions)
  ) {
    return undefined
  }
  const persons: SideRosterPerson[] = []
  for (const person of people as unknown[]) {
    if (!isObject(person)) return undefined
    const { userId, displayName, agentId, agentName } = person
    if (
      typeof userId !== "string" ||
      typeof agentId !== "string" ||
      typeof agentName !== "string" ||
      (displayName !== undefined && typeof displayName !== "string")
    ) {
      return undefined
    }
    persons.push({
      userId,
      ...(typeof displayName === "string" ? { displayName } : {}),
      agentId,
      agentName,
    })
  }
  const agents: SideRosterAgent[] = []
  for (const agent of contributions as unknown[]) {
    if (!isObject(agent)) return undefined
    const { agentId, ownerId, name, role, delegationMode } = agent
    if (
      typeof agentId !== "string" ||
      typeof ownerId !== "string" ||
      typeof name !== "string" ||
      (role !== undefined && typeof role !== "string") ||
      (delegationMode !== "request" && delegationMode !== "automatic")
    ) {
      return undefined
    }
    agents.push({
      agentId,
      ownerId,
      name,
      ...(typeof role === "string" ? { role } : {}),
      delegationMode,
    })
  }
  return { teamspaceName, paused, people: persons, contributions: agents }
}

/** An empty roster: what a conversation knows before any socket brought one. */
export const emptyRoster: SideRoster = {
  teamspaceName: "",
  paused: false,
  people: [],
  contributions: [],
}

const encoder = new TextEncoder()

/**
 * A roster as the Worker hands it to the object in a header: base64url of
 * its JSON, so any name survives a header's Latin-1.
 */
export function encodeRoster(roster: SideRoster): string {
  const bytes = encoder.encode(JSON.stringify(roster))
  let binary = ""
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "")
}

/** The roster `encodeRoster` made, or nothing for anything else. */
export function decodeRoster(header: string | null): SideRoster | undefined {
  if (!header || !/^[\w-]+$/.test(header)) return undefined
  try {
    const binary = atob(header.replace(/-/g, "+").replace(/_/g, "/"))
    const bytes = Uint8Array.from(binary, (character) =>
      character.charCodeAt(0)
    )
    return parseRoster(
      JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes))
    )
  } catch {
    return undefined
  }
}

function confirmationAnswers(
  value: unknown
): Array<{ id: string; confirmed: boolean }> | undefined {
  if (!Array.isArray(value) || value.length === 0 || value.length > 32) {
    return undefined
  }
  const answers: Array<{ id: string; confirmed: boolean }> = []
  for (const answer of value as unknown[]) {
    if (!isObject(answer)) return undefined
    const { id, confirmed } = answer
    if (
      typeof id !== "string" ||
      id === "" ||
      id.length > 200 ||
      typeof confirmed !== "boolean"
    ) {
      return undefined
    }
    answers.push({ id, confirmed })
  }
  return answers
}

function wireItems(value: unknown): SideWireItem[] | undefined {
  if (!Array.isArray(value)) return undefined
  return (value as unknown[]).flatMap((item) => {
    const parsed = wireItem(item)
    return parsed ? [parsed] : []
  })
}

/** An item with only its own fields, or nothing for one that is not an item. */
function wireItem(value: unknown): SideWireItem | undefined {
  if (!isObject(value)) return undefined
  const { kind, id, seq, createdAt } = value
  if (
    typeof id !== "string" ||
    !isSeq(seq) ||
    seq === 0 ||
    typeof createdAt !== "string"
  ) {
    return undefined
  }
  switch (kind) {
    case "message": {
      const { authorId, body, clientId, asksAgentId } = value
      if (
        typeof authorId !== "string" ||
        typeof body !== "string" ||
        !optionalString(clientId) ||
        !optionalString(asksAgentId)
      ) {
        return undefined
      }
      return {
        kind,
        id,
        seq,
        authorId,
        body,
        createdAt,
        ...(typeof clientId === "string" ? { clientId } : {}),
        ...(typeof asksAgentId === "string" ? { asksAgentId } : {}),
      }
    }
    case "agent-message": {
      const {
        agentId,
        agentName,
        ownerId,
        body,
        updatedAt,
        status,
        error,
        code,
        replyTo,
        via,
        confirmations,
      } = value
      if (
        typeof agentId !== "string" ||
        typeof agentName !== "string" ||
        typeof ownerId !== "string" ||
        typeof body !== "string" ||
        typeof updatedAt !== "string" ||
        typeof status !== "string" ||
        !agentStatuses.has(status) ||
        !optionalString(error) ||
        !optionalString(code) ||
        (replyTo !== undefined && !isSeq(replyTo))
      ) {
        return undefined
      }
      const parsedVia = via === undefined ? undefined : viaOf(via)
      if (via !== undefined && !parsedVia) return undefined
      const parsedConfirmations =
        confirmations === undefined ? undefined : confirmationsOf(confirmations)
      if (confirmations !== undefined && !parsedConfirmations) return undefined
      return {
        kind,
        id,
        seq,
        agentId,
        agentName,
        ownerId,
        body,
        createdAt,
        updatedAt,
        status: status as SideAgentMessageStatus,
        ...(typeof error === "string" ? { error } : {}),
        ...(typeof code === "string" ? { code } : {}),
        ...(typeof replyTo === "number" ? { replyTo } : {}),
        ...(parsedVia ? { via: parsedVia } : {}),
        ...(parsedConfirmations ? { confirmations: parsedConfirmations } : {}),
      }
    }
    case "request-card": {
      const {
        requestId,
        requesterId,
        requesterAgentId,
        requesterAgentName,
        targetId,
        targetAgentId,
        targetAgentName,
        approval,
        status,
        task,
        updatedAt,
        expiresAt,
        decision,
        clientId,
        requestedTools,
        guard,
        heldAnswer,
        review,
      } = value
      if (
        typeof requestId !== "string" ||
        typeof requesterId !== "string" ||
        typeof requesterAgentId !== "string" ||
        typeof requesterAgentName !== "string" ||
        typeof targetId !== "string" ||
        typeof targetAgentId !== "string" ||
        typeof targetAgentName !== "string" ||
        (approval !== "owner" && approval !== "agent") ||
        typeof status !== "string" ||
        !requestStatuses.has(status) ||
        typeof task !== "string" ||
        typeof updatedAt !== "string" ||
        typeof expiresAt !== "string" ||
        !optionalString(clientId) ||
        (requestedTools !== undefined && !isStrings(requestedTools))
      ) {
        return undefined
      }
      const parsedDecision =
        decision === undefined ? undefined : decisionOf(decision)
      if (decision !== undefined && !parsedDecision) return undefined
      const parsedGuard = guard === undefined ? undefined : guardOf(guard)
      if (guard !== undefined && !parsedGuard) return undefined
      const parsedHeld =
        heldAnswer === undefined ? undefined : heldAnswerOf(heldAnswer)
      if (heldAnswer !== undefined && !parsedHeld) return undefined
      const parsedReview = review === undefined ? undefined : reviewOf(review)
      if (review !== undefined && !parsedReview) return undefined
      return {
        kind,
        id,
        seq,
        requestId,
        requesterId,
        requesterAgentId,
        requesterAgentName,
        targetId,
        targetAgentId,
        targetAgentName,
        approval,
        status: status as SideRequestStatus,
        task,
        createdAt,
        updatedAt,
        expiresAt,
        ...(parsedDecision ? { decision: parsedDecision } : {}),
        ...(typeof clientId === "string" ? { clientId } : {}),
        ...(requestedTools !== undefined && requestedTools.length > 0
          ? { requestedTools }
          : {}),
        ...(parsedGuard ? { guard: parsedGuard } : {}),
        ...(parsedHeld ? { heldAnswer: parsedHeld } : {}),
        ...(parsedReview ? { review: parsedReview } : {}),
      }
    }
    case "status": {
      const { requestId, outcome, authorId, text } = value
      if (
        typeof requestId !== "string" ||
        typeof outcome !== "string" ||
        !outcomes.has(outcome) ||
        typeof authorId !== "string" ||
        typeof text !== "string"
      ) {
        return undefined
      }
      return {
        kind,
        id,
        seq,
        requestId,
        outcome: outcome as SideOutcome,
        authorId,
        text,
        createdAt,
      }
    }
    default:
      return undefined
  }
}

function viaOf(value: unknown): SideVia | undefined {
  if (!isObject(value)) return undefined
  const { requestId, onBehalfOf, approvedBy, reviewedBy } = value
  if (
    typeof requestId !== "string" ||
    typeof onBehalfOf !== "string" ||
    !optionalString(approvedBy) ||
    !optionalString(reviewedBy)
  ) {
    return undefined
  }
  return {
    requestId,
    onBehalfOf,
    ...(typeof approvedBy === "string" ? { approvedBy } : {}),
    ...(typeof reviewedBy === "string" ? { reviewedBy } : {}),
  }
}

/** Tool names as a decision allows them: at most `SIDE_MAX_TOOLS`, each 1 to `SIDE_MAX_TOOL_NAME` code points, each once. */
function toolNames(value: unknown): string[] | undefined {
  if (!isStrings(value) || value.length > SIDE_MAX_TOOLS) return undefined
  if (
    value.some((tool) => tool === "" || codePoints(tool) > SIDE_MAX_TOOL_NAME)
  ) {
    return undefined
  }
  return [...new Set(value)]
}

function guardOf(value: unknown): SideGuard | undefined {
  if (!isObject(value)) return undefined
  const { state, kinds } = value
  if (
    typeof state !== "string" ||
    !guardStates.has(state) ||
    !isStrings(kinds)
  ) {
    return undefined
  }
  return { state: state as SideGuard["state"], kinds }
}

function heldAnswerOf(value: unknown): SideHeldAnswer | undefined {
  if (!isObject(value)) return undefined
  const { stored, masked, failed } = value
  if (
    typeof stored !== "string" ||
    !optionalString(masked) ||
    (failed !== undefined && failed !== true)
  ) {
    return undefined
  }
  return {
    stored,
    ...(typeof masked === "string" ? { masked } : {}),
    ...(failed === true ? { failed } : {}),
  }
}

function reviewOf(value: unknown): SideReview | undefined {
  if (!isObject(value)) return undefined
  const { action, decidedAt, by, reason } = value
  return typeof action === "string" &&
    reviewActions.has(action) &&
    typeof decidedAt === "string" &&
    (by === "owner" || by === "system") &&
    optionalString(reason)
    ? {
        action: action as SideReviewAction,
        decidedAt,
        by,
        ...(typeof reason === "string" ? { reason } : {}),
      }
    : undefined
}

function confirmationsOf(value: unknown): SideConfirmation[] | undefined {
  if (!Array.isArray(value)) return undefined
  const confirmations: SideConfirmation[] = []
  for (const item of value as unknown[]) {
    if (!isObject(item)) return undefined
    const { callId, tool, arguments: args, hint } = item
    if (
      typeof callId !== "string" ||
      typeof tool !== "string" ||
      (args !== undefined && !isObject(args)) ||
      !optionalString(hint)
    ) {
      return undefined
    }
    confirmations.push({
      callId,
      tool,
      ...(args !== undefined ? { arguments: args } : {}),
      ...(typeof hint === "string" ? { hint } : {}),
    })
  }
  return confirmations
}

function decisionOf(value: unknown): SideDecision | undefined {
  if (!isObject(value)) return undefined
  const { decision, message, decidedAt, by } = value
  if (
    (decision !== "approved" && decision !== "rejected") ||
    typeof message !== "string" ||
    typeof decidedAt !== "string" ||
    (by !== undefined && by !== "owner" && by !== "agent" && by !== "system")
  ) {
    return undefined
  }
  return { decision, message, decidedAt, ...(by ? { by } : {}) }
}

function isGoId(value: unknown): value is string {
  return typeof value === "string" && goIdPattern.test(value)
}

function isSeq(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0
}

function isStrings(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string")
}

function isRead(value: unknown): value is SideRead {
  if (!isObject(value)) return false
  return typeof value.userId === "string" && isSeq(value.seq)
}

function optionalString(value: unknown): boolean {
  return value === undefined || typeof value === "string"
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function parsedObject(
  data: unknown,
  maxLength = Number.POSITIVE_INFINITY
): Record<string, unknown> | undefined {
  if (typeof data !== "string" || data.length > maxLength) return undefined
  let value: unknown
  try {
    value = JSON.parse(data)
  } catch {
    return undefined
  }
  return isObject(value) ? value : undefined
}
