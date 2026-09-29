// How a request reads on its card (DESIGN.md §3, "Requests"): who asked whom,
// where it stands, what an owner's choice lets an agent do, and what happened
// to it, in plain words. Pure, so the card, the Requests view and their tests
// share one set of rules. A request is data: nothing here repeats a code, an
// id or anything Go held back from the reader.
import { asksForConsent, originOf } from "@/lib/agents/agent-requests"
import type {
  AgentRequest,
  AgentRequestConsent,
  AgentRequestDecider,
  AgentRequestGuard,
  AgentRequestWinner,
} from "@/lib/agents/agent-requests"
import { clockTime, dayLabel, firstName } from "./direct-message-format"
import { capitalized, guardNote, toolsLine } from "./side-conversation-format"

/** Which side of a request the reader is: the member it is addressed to, or the one who asked. */
export type RequestSide = "owner" | "asker" | "other"

/** What the reader is, who the members are, and what time it is. */
export type Reader = {
  viewerId: string
  /** A member's name by id, once the members list can say; nothing while it loads or for anyone it does not list. */
  nameOf: (userId: string) => string | undefined
  now: number
}

/** The icon and colour a status reads in. */
export type Tone = "waiting" | "working" | "done" | "ended" | "failed"

export type RequestStatusLine = {
  tone: Tone
  text: string
  /** A quieter second line: how it was decided, or what an answer held back. */
  detail?: string
}

export function sideOf(request: AgentRequest, viewerId: string): RequestSide {
  if (request.target.user_id === viewerId) return "owner"
  if (request.requester.user_id === viewerId) return "asker"
  return "other"
}

/**
 * Whether a request ran out before it was finished: Go says `expired` once it
 * has read it past its time, and a page that has been open a while can know
 * before that.
 */
export function isExpired(request: AgentRequest, now: number): boolean {
  if (request.status === "expired") return true
  return (
    (request.status === "pending" || request.status === "approved") &&
    Date.parse(request.expires_at) <= now
  )
}

/** Whether the owner's choice is still theirs to make: pending, and not past its time. */
export function isOpenToDecide(request: AgentRequest, now: number): boolean {
  return request.status === "pending" && !isExpired(request, now)
}

/**
 * Whether a request waits on its reader: to decide it, or to review the
 * answer that was held for them. Only its owner is ever waited on.
 */
export function waitsOnOwner(
  request: AgentRequest,
  viewerId: string,
  now: number
): boolean {
  return (
    sideOf(request, viewerId) === "owner" &&
    (isOpenToDecide(request, now) || request.status === "review")
  )
}

// ---------------------------------------------------------------------------
// People
// ---------------------------------------------------------------------------

type Named = { user_display_name?: string; user_email?: string }

/** A member as a sentence names them: "you" for the reader, else their name. */
export function personName(
  reader: Reader,
  userId: string | undefined,
  party?: Named
): string {
  if (userId && userId === reader.viewerId) return "you"
  const known = userId ? reader.nameOf(userId) : undefined
  return (
    known ??
    (party?.user_display_name?.trim() || party?.user_email?.trim() || "someone")
  )
}

/** `personName`, shortened to a first name for a sentence. */
export function shortName(
  reader: Reader,
  userId: string | undefined,
  party?: Named
): string {
  const name = personName(reader, userId, party)
  return name === "you" ? name : firstName(name)
}

export function askerName(request: AgentRequest, reader: Reader): string {
  return personName(reader, request.requester.user_id, request.requester)
}

export function ownerName(request: AgentRequest, reader: Reader): string {
  return personName(reader, request.target.user_id, request.target)
}

/** The owner's agent as its owner and the asker call it: "Krude", or "your agent". */
export function targetAgentName(request: AgentRequest, reader: Reader): string {
  const name = request.target.agent_name.trim()
  if (request.target.user_id === reader.viewerId) return name || "your agent"
  return (
    name ||
    `${shortName(reader, request.target.user_id, request.target)}'s agent`
  )
}

// ---------------------------------------------------------------------------
// Words about where a request stands
// ---------------------------------------------------------------------------

/** Where a request was made, as a label: "Slack", or "Oryxa" for one an agent filed there. */
export function originLabel(request: AgentRequest): "Slack" | "Oryxa" {
  return originOf(request) === "slack" ? "Slack" : "Oryxa"
}

/** The card's title: who asked whom. Slack asks name the owner, who is asked in person; the rest name the agent. */
export function requestTitle(request: AgentRequest, reader: Reader): string {
  const asker = capitalized(askerName(request, reader))
  if (asksForConsent(request)) {
    return `${asker} asked ${ownerName(request, reader)}`
  }
  return `${asker} asked ${targetAgentName(request, reader)}`
}

/** How far an approval let the answer go, as a short sentence. */
export function consentWords(consent: AgentRequestConsent): string {
  return consent === "access" ? "Access for 1 hour." : "Summary only."
}

/** When, to follow "at": the clock today, and the day and clock otherwise. */
export function whenPhrase(iso: string | undefined, now: number): string {
  if (!iso) return ""
  const clock = clockTime(iso)
  if (!clock) return ""
  const day = dayLabel(iso, now)
  if (day === "Today") return ` at ${clock}`
  if (day === "Yesterday") return ` yesterday at ${clock}`
  return ` on ${day} at ${clock}`
}

/** How long a request that is still open has left, or nothing once it is not. */
export function expiresPhrase(
  request: AgentRequest,
  now: number
): string | undefined {
  if (request.status !== "pending" && request.status !== "approved") {
    return undefined
  }
  const left = Date.parse(request.expires_at) - now
  if (Number.isNaN(left) || left <= 0) return undefined
  if (left < 60_000) return "Expires in under a minute."
  const minutes = Math.round(left / 60_000)
  if (minutes < 60) return `Expires in ${minutes} min.`
  const hours = Math.round(minutes / 60)
  if (hours < 48) return `Expires in ${hours} h.`
  return `Expires in ${Math.round(hours / 24)} days.`
}

/** What a decision says, whoever recorded it: read from the request's own, or from the winner of a lost race. */
type DecisionFacts = {
  decision: "approved" | "rejected"
  consent?: AgentRequestConsent
  decidedBy?: AgentRequestDecider
  decidedAt?: string
}

function factsOf(request: AgentRequest): DecisionFacts | undefined {
  const { decision } = request
  if (!decision) return undefined
  return {
    decision: decision.decision,
    consent: decision.consent,
    decidedBy: decision.decided_by,
    decidedAt: decision.decided_at,
  }
}

/** Why a bypass applied, as a clause, given the asker and the owner as a sentence names them ("you" for the reader). */
function bypassWhy(
  reason: string | undefined,
  asker: string,
  owner: string
): string | undefined {
  switch (reason) {
    case "priority_channel":
      return "it was asked in a high-priority channel"
    case "org_chart":
      return `${asker} ${asker === "you" ? "are" : "is"} above ${owner} in your organization's chart`
    default:
      return undefined
  }
}

/**
 * How a request was decided, as a sentence, where and by whom and when:
 * "Approved in Slack by Raya at 2:02 PM. Summary only." A lease, a bypass, an
 * agent and Oryxa each say so in their own words, and never a code.
 */
function decisionSentence(
  facts: DecisionFacts,
  request: AgentRequest,
  reader: Reader
): string {
  const asker = shortName(reader, request.requester.user_id, request.requester)
  const owner = shortName(reader, request.target.user_id, request.target)
  const at = whenPhrase(facts.decidedAt, reader.now)
  const by = facts.decidedBy
  const rejected = facts.decision === "rejected"
  switch (by?.kind) {
    case "lease":
      return `Approved${at} under the access ${owner} gave ${asker}.`
    case "bypass": {
      const why = bypassWhy(by.reason, asker, owner)
      return `Approved${at} without asking ${owner}${why ? `: ${why}` : ""}.`
    }
    case "system":
      return by.reason === "answered_elsewhere"
        ? "Another owner answered first, so Oryxa closed this one."
        : `Closed by Oryxa${at}.`
    case "agent":
      return `${rejected ? "Declined" : "Approved"} by ${owner === "you" ? "your" : `${owner}'s`} agent${at}.`
    default: {
      const verb = rejected
        ? asksForConsent(request)
          ? "Denied"
          : "Declined"
        : "Approved"
      const where =
        by?.surface === "slack"
          ? " in Slack"
          : by?.surface === "web"
            ? " in Oryxa"
            : ""
      const whoId = by?.user_id ?? request.target.user_id
      const who = shortName(
        reader,
        whoId,
        whoId === request.target.user_id ? request.target : undefined
      )
      const consent = facts.consent ? ` ${consentWords(facts.consent)}` : ""
      return `${verb}${where} by ${who}${at}.${consent}`
    }
  }
}

/** How the request was decided, or nothing while it is not. */
export function decisionLine(
  request: AgentRequest,
  reader: Reader
): string | undefined {
  const facts = factsOf(request)
  return facts ? decisionSentence(facts, request, reader) : undefined
}

/**
 * What a choice that lost says: who won and how, in the words the request's
 * own decision would use. A request that ended some other way says that.
 */
export function winnerLine(
  winner: AgentRequestWinner,
  request: AgentRequest,
  reader: Reader
): string {
  if (winner.decision) {
    return decisionSentence(
      {
        decision: winner.decision,
        consent: winner.consent,
        decidedBy: winner.decided_by,
        decidedAt: winner.decided_at,
      },
      request,
      reader
    )
  }
  return winner.status === "expired"
    ? "This request expired before it was decided."
    : "This request was closed before it was decided."
}

// ---------------------------------------------------------------------------
// Why an answer was held
// ---------------------------------------------------------------------------

/** What the answer guard holds an answer for, in plain words: personal details it holds for review. */
const heldKindWords = new Map<string, string>([
  ["EMAIL", "an email address"],
  ["PHONE", "a phone number"],
  ["ADDRESS", "a street address"],
])

/** What the relevance check names an answer carried that its question did not ask for. */
const offTopicWords = new Map<string, string>([
  ["personal_life", "personal-life details"],
  ["health", "health details"],
  ["hr", "HR details"],
  ["compensation", "pay details"],
  ["others_private", "someone else's private messages or data"],
  ["credentials", "secret values"],
])

const UNCHECKED = "UNCHECKED"
const OFF_TOPIC = "OFF_TOPIC"

function listWords(words: readonly string[]): string {
  if (words.length < 2) return words.join("")
  return `${words.slice(0, -1).join(", ")} and ${words.at(-1)}`
}

/**
 * Why an answer waits for its owner, in plain words: "Krude's answer
 * includes an email address and may include something the question didn't
 * ask for (health details). Review it before it's shared." A kind this page
 * does not know reads as "details Oryxa flagged": its code is never shown.
 */
export function heldReason(
  agentName: string,
  guard: AgentRequestGuard | undefined
): string {
  const kinds = guard?.kinds ?? []
  const unchecked = kinds.includes(UNCHECKED)
  const found: string[] = []
  const categories: string[] = []
  let offTopic = false
  let other = false
  for (const kind of kinds) {
    if (kind === UNCHECKED) continue
    if (kind === OFF_TOPIC) offTopic = true
    else if (offTopicWords.has(kind)) categories.push(offTopicWords.get(kind)!)
    else if (heldKindWords.has(kind)) found.push(heldKindWords.get(kind)!)
    else other = true
  }
  const why: string[] = []
  if (found.length > 0) why.push(`includes ${listWords([...new Set(found)])}`)
  if (offTopic || categories.length > 0) {
    const named = [...new Set(categories)]
    why.push(
      `may include something the question didn't ask for${named.length > 0 ? ` (${listWords(named)})` : ""}`
    )
  }
  if (other) why.push("includes details Oryxa flagged")
  if (unchecked) why.push("couldn't be checked automatically")
  return why.length > 0
    ? `${capitalized(agentName)}'s answer ${listWords(why)}. Review it before it's shared.`
    : `Review ${agentName}'s answer before it's shared.`
}

/** What the guard hid from the asker in an answer that was shared, or nothing when it hid nothing. */
export function hiddenNote(
  guard: AgentRequestGuard | undefined
): string | undefined {
  if (!guard || guard.state === "clear") return undefined
  return guardNote({ state: guard.state, kinds: guard.kinds ?? [] })
}

// ---------------------------------------------------------------------------
// The card's status
// ---------------------------------------------------------------------------

/** Where a request stands, as its card says it to the reader. */
export function statusOf(
  request: AgentRequest,
  reader: Reader
): RequestStatusLine {
  const side = sideOf(request, reader.viewerId)
  const forOwner = side === "owner"
  const owner = shortName(reader, request.target.user_id, request.target)
  const agent = targetAgentName(request, reader)
  const decided = decisionLine(request, reader)
  const consent = asksForConsent(request)
  const expired = isExpired(request, reader.now)

  if (expired && request.status !== "review" && request.status !== "withheld") {
    return {
      tone: "ended",
      text: forOwner
        ? "Expired."
        : request.status === "approved" || decided
          ? "Expired before it was answered."
          : `Expired before ${owner} decided.`,
      ...(decided ? { detail: decided } : {}),
    }
  }

  switch (request.status) {
    case "pending":
      return {
        tone: "waiting",
        text: forOwner
          ? consent
            ? "Waiting for your choice."
            : "Waiting for you to approve."
          : `Waiting for ${owner} to decide.`,
      }
    case "approved":
      return {
        tone: "working",
        text: forOwner
          ? `${capitalized(agent)} is working on it…`
          : `${capitalized(possessive(owner))} agent is working on it…`,
        ...(decided ? { detail: decided } : {}),
      }
    case "review":
      return {
        tone: "waiting",
        text: forOwner
          ? heldReason(agent, request.guard)
          : `Waiting for ${owner} to review the answer.`,
        ...(decided ? { detail: decided } : {}),
      }
    case "completed": {
      const hidden = forOwner ? hiddenNote(request.guard) : undefined
      const detail = [decided, hidden && `${hidden}.`].filter(Boolean).join(" ")
      return {
        tone: "done",
        text: "Answered.",
        ...(detail ? { detail } : {}),
      }
    }
    case "rejected":
      return { tone: "ended", text: decided ?? "Declined." }
    case "failed":
      return {
        tone: "failed",
        text: forOwner
          ? `${capitalized(agent)} couldn't finish the request.`
          : "It couldn't be answered.",
        ...(decided ? { detail: decided } : {}),
      }
    case "withheld":
      return {
        tone: "ended",
        text: withheldWords(request, forOwner, owner, agent),
        ...(decided ? { detail: decided } : {}),
      }
    case "expired":
      // Read past its time, with no state a page should say more of.
      return { tone: "ended", text: "Expired." }
  }
}

function possessive(name: string): string {
  return name.endsWith("s") ? `${name}'` : `${name}'s`
}

/** Go's reason for an answer withheld when its request expired in review. */
const EXPIRED_BEFORE_REVIEW = "expired before review"

function withheldWords(
  request: AgentRequest,
  forOwner: boolean,
  owner: string,
  agent: string
): string {
  const review = request.review
  if (!review) return "Not shared."
  if (review.decided_by.kind === "member") {
    return forOwner
      ? `You chose not to share ${agent}'s answer.`
      : `Not shared by ${owner}.`
  }
  return review.reason === EXPIRED_BEFORE_REVIEW
    ? forOwner
      ? "Expired before you reviewed it."
      : "Expired."
    : "Withheld by Oryxa."
}

/** The line that says what approving lets the owner's agent read, for the owner while it waits on them. */
export function readsLine(
  request: AgentRequest,
  reader: Reader
): string | undefined {
  if (sideOf(request, reader.viewerId) !== "owner") return undefined
  return toolsLine(targetAgentName(request, reader), request.requested_tools)
}

// ---------------------------------------------------------------------------
// The owner's three choices
// ---------------------------------------------------------------------------

export type ConsentChoiceCopy = {
  choice: "summary" | "access" | "deny"
  label: string
  /** What it lets the agent do, as the owner is asked, given the asker's first name. */
  detail: (asker: string) => string
}

/** The three choices, in the order Slack's notice offers them, with what each lets the agent do. */
export const consentChoices: readonly ConsentChoiceCopy[] = [
  {
    choice: "summary",
    label: "Summary only",
    detail: () =>
      "Your agent answers in its own words. It won't copy passages from your documents or share links.",
  },
  {
    choice: "access",
    label: "Allow for 1 hour",
    detail: (asker) =>
      `Your agent may quote what it reads. ${asker}'s follow-up questions in the same Slack thread are answered without asking you again for an hour, and you can end that any time.`,
  },
  {
    choice: "deny",
    label: "Deny",
    detail: (asker) => `Nothing is shared. ${asker} is told you said no.`,
  },
]

/** A choice's label as its button says it while the choice is on its way. */
export function consentChoicePending(choice: ConsentChoiceCopy["choice"]) {
  return choice === "deny" ? "Denying…" : "Approving…"
}
