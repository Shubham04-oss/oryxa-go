// Request notes (REALTIME.md, "Request notes"): what Go tells the Worker when
// an agent request between two members moves, and what the direct message and
// the two inboxes keep of it. A note is identities and a state: who asked
// whose agent, where the request was made, which revision of it this is, and
// the state that revision is in. It carries nothing of what was asked or
// answered, so nothing in it is shown as content: the words a page says
// around it are all in this file, and the request itself is read from Go, as
// the member.
//
// Shared by the Worker, the two objects and the page, so it imports nothing
// of the Worker's.
import { isNameId } from "./names"

/**
 * Where a request stands, in the words Go's realtime service says it (its
 * ledger status, less the ones nobody says): `waiting` (pending), `approved`,
 * `declined` (rejected, by its owner, their agent or Oryxa), `answered`
 * (completed), `held` (its answer waits for its owner's review), `failed`,
 * and `withheld` (its owner, or its expiry, kept the answer back).
 */
export const REQUEST_NOTE_STATES = [
  "waiting",
  "approved",
  "declined",
  "answered",
  "held",
  "failed",
  "withheld",
] as const

export type RequestNoteState = (typeof REQUEST_NOTE_STATES)[number]

/**
 * A state as a line may say it: a note's, or `expired`, which Go says when
 * it reads a pending or approved request past its expiry and no event ever
 * carries.
 */
export type RequestLineState = RequestNoteState | "expired"

/** Whether `value` is one of a note's states. */
export function isRequestNoteState(value: unknown): value is RequestNoteState {
  return (
    typeof value === "string" &&
    (REQUEST_NOTE_STATES as readonly string[]).includes(value)
  )
}

// An origin is a short lowercase word: `agent` (an agent filed the request)
// or `slack` (the Slack app did, for the member who mentioned it). Another
// word a later build publishes is kept as it is, and a page says no more of
// it than that the request was made.
const originPattern = /^[a-z][a-z0-9_-]{0,31}$/

/** Whether `value` is an origin a note may carry. */
export function isRequestOrigin(value: unknown): value is string {
  return typeof value === "string" && originPattern.test(value)
}

/** The origin of a request the Slack app made. */
export const REQUEST_ORIGIN_SLACK = "slack"

/** The origin of a request an agent filed. */
export const REQUEST_ORIGIN_AGENT = "agent"

/**
 * One move of a request, as Go says it and the objects keep it. `revision`
 * is the request's own: it goes up by one each time the request changes and
 * never down, so an object keeps the highest it was told of and ignores any
 * note that is not newer (SQS and EventBridge keep no order, and Go sends
 * again whatever it is unsure of). `changedAt` is when the request reached
 * this state, an ISO 8601 time in UTC to the millisecond.
 */
export type RequestNote = {
  requestId: string
  revision: number
  /** The member who asked. */
  askerId: string
  /** The member whose agent was asked. */
  ownerId: string
  origin: string
  state: RequestNoteState
  changedAt: string
}

/** What an object did with a note: kept it, or found it no newer than what it holds. */
export type RequestNoteOutcome = "noted" | "repeated"

/**
 * A note as the DM keeps and sends it: with `at`, when the conversation first
 * noted the request, which is where its line stands in the log (the line's
 * state changes in place, its place does not).
 */
export type DmRequestNote = RequestNote & { at: string }

/** The most notes a DM keeps and sends a socket as it opens: the latest by `at`. */
export const DM_REQUEST_LIMIT = 30

/** Whether `value` is an integer revision: 1 or more. */
export function isRevision(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 1
}

// An ISO time as the objects write it: UTC, to the millisecond.
const instant = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/

/**
 * Whether `value` is a time as a note carries it: one that writes itself
 * back as it is, which a 30th of February, or 24:00, does not (`Date.parse`
 * rolls them over to the next day).
 */
export function isNoteInstant(value: unknown): value is string {
  if (typeof value !== "string" || !instant.test(value)) return false
  const at = Date.parse(value)
  return Number.isFinite(at) && new Date(at).toISOString() === value
}

/**
 * A note read from something that only claims to be one (an RPC argument, or
 * a frame on a socket), or nothing when it is not: every field the right kind
 * and shape, and the two people different. Only the note's own fields are
 * kept.
 */
export function readRequestNote(value: unknown): RequestNote | undefined {
  if (typeof value !== "object" || value === null) return undefined
  const note = value as { [Key in keyof RequestNote]?: unknown }
  if (
    !isNameId(note.requestId) ||
    !isRevision(note.revision) ||
    !isNameId(note.askerId) ||
    !isNameId(note.ownerId) ||
    note.askerId === note.ownerId ||
    !isRequestOrigin(note.origin) ||
    !isRequestNoteState(note.state) ||
    !isNoteInstant(note.changedAt)
  ) {
    return undefined
  }
  return {
    requestId: note.requestId,
    revision: note.revision,
    askerId: note.askerId,
    ownerId: note.ownerId,
    origin: note.origin,
    state: note.state,
    changedAt: note.changedAt,
  }
}

// The state as a line says it.
const stateWords: Record<RequestLineState, string> = {
  waiting: "waiting",
  approved: "approved",
  declined: "declined",
  answered: "answered",
  held: "held",
  failed: "failed",
  withheld: "not shared",
  expired: "expired",
}

/** A state in the word a line says it in. */
export function requestStateWord(state: RequestLineState): string {
  return stateWords[state]
}

/** Whether a request in `state` can still move: the others are final. */
export function isOpenRequestState(state: RequestLineState): boolean {
  return state === "waiting" || state === "approved" || state === "held"
}

function where(origin: string): string {
  return origin === REQUEST_ORIGIN_SLACK ? " in Slack" : ""
}

/**
 * A request in a DM's log, without its state: who asked whose agent, from the
 * viewer's side. `peerName` is what the viewer calls the other person.
 */
export function requestLine(
  viewerAsked: boolean,
  peerName: string,
  origin: string
): string {
  return viewerAsked
    ? `You asked ${peerName}'s agent${where(origin)}`
    : `${peerName} asked your agent${where(origin)}`
}

/**
 * What an inbox row says of its latest request, from the viewer's side, in
 * fixed words: nobody's names, since the inbox keeps none, and nothing of what
 * was asked.
 */
export function requestPreview(
  viewerAsked: boolean,
  origin: string,
  state: RequestLineState
): string {
  const asked = viewerAsked ? "You asked their agent" : "Asked your agent"
  return `${asked}${where(origin)} · ${stateWords[state]}`
}
