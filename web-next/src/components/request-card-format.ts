// How a request reads on its card (DESIGN.md §3, "Requests"): who asked whom,
// where it stands, what an owner's choice lets an agent do, and what happened
// to it, in plain words. Pure, so the card, the Requests view and their tests
// share one set of rules. A request is data: nothing here repeats a code, an
// id or anything Go held back from the reader.
import {
  REPLY_MAX_CHARACTERS,
  asksForConsent,
  isOwnerWritten,
  needsOwnerReply,
  originOf,
} from "@/lib/agents/agent-requests"
import type {
  AgentRequest,
  AgentRequestDecider,
  AgentRequestDecisionConsent,
  AgentRequestGuard,
  AgentRequestWinner,
} from "@/lib/agents/agent-requests"
import { clockTime, dayLabel, firstName } from "./direct-message-format"
import { capitalized, toolsLine } from "./side-conversation-format"

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

/**
 * How far an approval let the answer go, as a short sentence, in the words of
 * the choice that gave it; for `reply`, that the owner answered in their own
 * words.
 */
export function consentWords(consent: AgentRequestDecisionConsent): string {
  switch (consent) {
    case "access":
      return "Access for 1 hour."
    case "reply":
      return "Replied in their own words."
    case "summary":
      return "Answer shared."
  }
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
  consent?: AgentRequestDecisionConsent
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
 * "Approved in Slack by Raya at 2:02 PM. Answer shared." A lease, a bypass, an
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
      // A member's own choice is worded as its button was: Deny, whichever
      // surface the request came from.
      const verb = rejected ? "Denied" : "Approved"
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
      // An owner who answered in their own words decided by doing so.
      if (facts.consent === "reply") return `Replied${where} by ${who}${at}.`
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
// Why an answer was held, and what was hidden in one that was shared
// ---------------------------------------------------------------------------
//
// The guard's findings are kinds, never values: Bedrock's entity types for
// personal details and credentials, the relevance check's OFF_TOPIC with the
// categories it found, and, for a Slack answer, the consent guard's own. Each
// is said in plain words here, and a kind this page does not know is said as
// "details Oryxa flagged": its code is never shown.

/** Personal details the guard holds an answer for. */
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

/**
 * What the consent guard holds a Slack answer for, as a clause that follows
 * "answer": the grounding checks, relevance and the topics Oryxa does not
 * share. (The summary rule's two kinds are `summaryRuleWords`.)
 */
const policyHoldWords = new Map<string, string>([
  ["NO_SOURCES", "isn't based on anything it read"],
  ["UNGROUNDED", "isn't supported by what it read"],
  ["IRRELEVANT", "doesn't answer the question"],
  [
    "DENIED_TOPIC",
    "touches a topic Oryxa doesn't share (credentials, HR or pay records, health or personal life)",
  ],
])

const COPIED_FROM_SOURCE = "COPIED_FROM_SOURCE"
const LINK = "LINK"
const UNCHECKED = "UNCHECKED"
const OFF_TOPIC = "OFF_TOPIC"

/**
 * Whether the answer was held for breaking the summary rule: at summary level
 * it may copy no long passage of what the agent read and carry no link.
 * Masking cannot mend that, so such an answer is sent as it was held or
 * withheld, and has no masked version to offer.
 */
export function isSummaryRuleHold(
  guard: AgentRequestGuard | undefined
): boolean {
  const kinds = guard?.kinds ?? []
  return kinds.includes(COPIED_FROM_SOURCE) || kinds.includes(LINK)
}

function summaryRuleWords(kinds: readonly string[]): string | undefined {
  const copied = kinds.includes(COPIED_FROM_SOURCE)
  const linked = kinds.includes(LINK)
  if (copied && linked) {
    return "copies passages from what it read and includes a link, which a summary shouldn't"
  }
  if (copied) {
    return "copies long passages from what it read, which a summary shouldn't"
  }
  if (linked) return "includes a link, which a summary shouldn't"
  return undefined
}

function listWords(words: readonly string[]): string {
  if (words.length < 2) return words.join("")
  return `${words.slice(0, -1).join(", ")} and ${words.at(-1)}`
}

/**
 * Why an answer waits for its owner, in plain words: "Krude's answer
 * includes an email address and may include something the question didn't
 * ask for (health details). Review it before it's shared."
 */
export function heldReason(
  agentName: string,
  guard: AgentRequestGuard | undefined
): string {
  const kinds = guard?.kinds ?? []
  const found: string[] = []
  const categories: string[] = []
  let offTopic = false
  let other = false
  for (const kind of kinds) {
    if (kind === UNCHECKED || kind === COPIED_FROM_SOURCE || kind === LINK) {
      continue
    }
    const category = offTopicWords.get(kind)
    const detail = heldKindWords.get(kind)
    if (kind === OFF_TOPIC) offTopic = true
    else if (policyHoldWords.has(kind)) continue
    else if (category) categories.push(category)
    else if (detail) found.push(detail)
    else other = true
  }
  const why: string[] = []
  if (found.length > 0) why.push(`includes ${listWords([...new Set(found)])}`)
  const summary = summaryRuleWords(kinds)
  if (summary) why.push(summary)
  // Always in the same order, whatever order the guard listed them in.
  for (const [kind, words] of policyHoldWords) {
    if (kinds.includes(kind)) why.push(words)
  }
  if (offTopic || categories.length > 0) {
    const named = [...new Set(categories)]
    why.push(
      `may include something the question didn't ask for${named.length > 0 ? ` (${listWords(named)})` : ""}`
    )
  }
  if (other) why.push("includes details Oryxa flagged")
  if (kinds.includes(UNCHECKED)) why.push("couldn't be checked automatically")
  return why.length > 0
    ? `${capitalized(agentName)}'s answer ${listWords(why)}. Review it before it's shared.`
    : `Review ${agentName}'s answer before it's shared.`
}

/** What the guard always masks, in plain words: credentials, keys and financial and ID numbers. */
const maskedKindWords = new Map<string, string>([
  ["PASSWORD", "a password"],
  ["AWS_ACCESS_KEY", "an access key"],
  ["AWS_SECRET_KEY", "an access key"],
  ["SLACK_TOKEN", "a Slack token"],
  ["API_KEY", "an API key"],
  ["ACCESS_TOKEN", "an access token"],
  ["PRIVATE_KEY", "a private key"],
  ["CREDIT_DEBIT_CARD_NUMBER", "card details"],
  ["CREDIT_DEBIT_CARD_CVV", "card details"],
  ["CREDIT_DEBIT_CARD_EXPIRY", "card details"],
  ["PIN", "a PIN"],
  ["INTERNATIONAL_BANK_ACCOUNT_NUMBER", "bank details"],
  ["SWIFT_CODE", "bank details"],
  ["US_BANK_ACCOUNT_NUMBER", "bank details"],
  ["US_BANK_ROUTING_NUMBER", "bank details"],
  ["US_SOCIAL_SECURITY_NUMBER", "an ID number"],
  ["US_INDIVIDUAL_TAX_IDENTIFICATION_NUMBER", "an ID number"],
  ["US_PASSPORT_NUMBER", "an ID number"],
  ["DRIVER_ID", "an ID number"],
  ["CA_HEALTH_NUMBER", "an ID number"],
  ["CA_SOCIAL_INSURANCE_NUMBER", "an ID number"],
  ["UK_NATIONAL_HEALTH_SERVICE_NUMBER", "an ID number"],
  ["UK_NATIONAL_INSURANCE_NUMBER", "an ID number"],
  ["UK_UNIQUE_TAXPAYER_REFERENCE_NUMBER", "an ID number"],
])

/**
 * What the guard hid from the asker in an answer that was shared, told to its
 * owner: "Shared with a password hidden". Nothing when it hid nothing: an
 * answer it found nothing in, and one the owner sent as it was held, which
 * hid nothing (only "send masked" hides what it was held for).
 */
export function hiddenNote(request: AgentRequest): string | undefined {
  const guard = request.guard
  if (!guard) return undefined
  const maskedByOwner = request.review?.action === "send_masked"
  if (guard.state === "clear") return undefined
  if (guard.state === "held" && !maskedByOwner) return undefined
  const hidden = [
    ...new Set(
      (guard.kinds ?? [])
        .filter((kind) => kind !== UNCHECKED)
        .flatMap((kind) => {
          const words =
            maskedKindWords.get(kind) ??
            (guard.state === "held" ? heldKindWords.get(kind) : undefined)
          // A held kind that masking cannot touch (OFF_TOPIC, the summary
          // rule, grounding) hides nothing.
          return words
            ? [words]
            : guard.state === "masked"
              ? ["other personal details"]
              : []
        })
    ),
  ]
  return hidden.length > 0
    ? `Shared with ${listWords(hidden)} hidden`
    : undefined
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

  // An ask that needs its owner's own reply waits on words, not on a choice.
  const replyAsked = needsOwnerReply(request)

  if (expired && request.status !== "review" && request.status !== "withheld") {
    return {
      tone: "ended",
      text: forOwner
        ? "Expired."
        : request.status === "approved" || decided
          ? "Expired before it was answered."
          : `Expired before ${owner} ${replyAsked ? "replied" : "decided"}.`,
      ...(decided ? { detail: decided } : {}),
    }
  }

  switch (request.status) {
    case "pending":
      return {
        tone: "waiting",
        text: forOwner
          ? replyAsked
            ? "Waiting for your reply."
            : consent
              ? "Waiting for your choice."
              : "Waiting for you to approve."
          : replyAsked
            ? `Waiting for ${owner} to reply.`
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
      const hidden = forOwner ? hiddenNote(request) : undefined
      if (isOwnerWritten(request)) {
        // Their own words: who wrote them is the status, and how and when
        // they were sent is all the detail there is to say.
        const sent = sentLine(request, reader, forOwner)
        const replyDetail = [sent, hidden && `${hidden}.`]
          .filter(Boolean)
          .join(" ")
        return {
          tone: "done",
          text: forOwner ? "You replied." : `${capitalized(owner)} replied.`,
          ...(replyDetail ? { detail: replyDetail } : {}),
        }
      }
      const detail = [decided, hidden && `${hidden}.`].filter(Boolean).join(" ")
      return {
        tone: "done",
        text: "Answered.",
        ...(detail ? { detail } : {}),
      }
    }
    case "rejected":
      return { tone: "ended", text: decided ?? "Denied." }
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

/**
 * Where and when an owner's own reply was sent, as a short sentence: "Sent in
 * Oryxa at 2:02 PM." When it took the place of an answer their agent wrote and
 * the guard held, its owner is told so ("…, in place of Krude's answer").
 */
function sentLine(
  request: AgentRequest,
  reader: Reader,
  forOwner: boolean
): string | undefined {
  const replaced =
    request.review?.action === "reply" ? request.review : undefined
  const by = replaced ? replaced.decided_by : request.decision?.decided_by
  const when = replaced ? replaced.decided_at : request.decision?.decided_at
  const where =
    by?.surface === "slack"
      ? " in Slack"
      : by?.surface === "web"
        ? " in Oryxa"
        : ""
  const place = `${where}${whenPhrase(when, reader.now)}`
  const instead =
    replaced && forOwner
      ? `${place ? "," : ""} in place of ${targetAgentName(request, reader)}'s answer`
      : ""
  return place || instead ? `Sent${place}${instead}.` : undefined
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
// The owner's choices
// ---------------------------------------------------------------------------
//
// One set of words wherever a request came from, and the same three as the
// buttons of the notice Slack's DM sends (apps/internal/slackbot/templates.go).
// A Slack ask is answered with three of them (`POST …/consent`): Share answer,
// Allow for 1 hour and Deny. A request an agent filed in Oryxa is answered
// with two (`POST …/decision`): Share answer, an approval with the tools it
// asked for, and Deny, a decline.

/** The words on the choices. */
export const SHARE_ANSWER = "Share answer"
const ALLOW_FOR_AN_HOUR = "Allow for 1 hour"
export const DENY = "Deny"

/** A first name as a possessive ("Raya's"), and "your" for the reader. */
function possessiveName(name: string): string {
  return name === "you" ? "your" : `${name}'s`
}

/**
 * What a choice's button is called to a screen reader: its words and whose
 * request they are about, given the asker's first name ("Share answer to
 * Raya's request"). A page of cards has these buttons once a card, so the
 * visible words alone would not tell them apart.
 */
export function shareAnswerName(asker: string): string {
  return `${SHARE_ANSWER} to ${possessiveName(asker)} request`
}

function allowForAnHourName(asker: string): string {
  return `${ALLOW_FOR_AN_HOUR} for ${possessiveName(asker)} request`
}

export function denyName(asker: string): string {
  return `${DENY} ${possessiveName(asker)} request`
}

export type ConsentChoiceCopy = {
  choice: "summary" | "access" | "deny"
  label: string
  /** What the button is called to a screen reader, given the asker's first name. */
  name: (asker: string) => string
  /** What it lets the agent do, as the owner is asked, given the asker's first name. */
  detail: (asker: string) => string
}

/** The three choices of a Slack ask, in the order Slack's notice offers them, with what each lets the agent do. */
export const consentChoices: readonly ConsentChoiceCopy[] = [
  {
    choice: "summary",
    label: SHARE_ANSWER,
    name: shareAnswerName,
    detail: () =>
      "Your agent answers in its own words. It won't copy passages from your documents or share links.",
  },
  {
    choice: "access",
    label: ALLOW_FOR_AN_HOUR,
    name: allowForAnHourName,
    detail: (asker) =>
      `Your agent may quote what it reads. ${asker}'s follow-ups in that thread are answered for an hour.`,
  },
  {
    choice: "deny",
    label: DENY,
    name: denyName,
    detail: (asker) => `Nothing is shared. ${asker} is told you said no.`,
  },
]

/** A choice's label as its button says it while the choice is on its way. */
export function consentChoicePending(choice: ConsentChoiceCopy["choice"]) {
  switch (choice) {
    case "summary":
      return "Sharing…"
    case "access":
      return "Allowing…"
    case "deny":
      return "Denying…"
  }
}

// ---------------------------------------------------------------------------
// Replying yourself
// ---------------------------------------------------------------------------
//
// An owner may answer in their own words instead of letting their agent: for
// a request that waits on their decision, and for an answer held for their
// review (`POST …/reply`, the agent never runs). A request that needs their
// own judgment (`answer_by: "owner"`) leads with it: the reply first, then
// "Let my agent answer" (Share answer, worded for the owner who has to choose
// to let it), then Deny.

/** The words on the owner's own reply, and on the choice that lets their agent answer instead. */
export const REPLY_YOURSELF = "Reply yourself"
export const SEND_REPLY = "Send reply"
export const LET_MY_AGENT_ANSWER = "Let my agent answer"

/** What the reply's button is called to a screen reader: its words and whose request it answers ("Reply yourself to Raya's request"). */
export function replyYourselfName(asker: string): string {
  return `${REPLY_YOURSELF} to ${possessiveName(asker)} request`
}

/** What the choice that lets their agent answer is called to a screen reader, given the asker's first name. */
export function letMyAgentAnswerName(asker: string): string {
  return `${LET_MY_AGENT_ANSWER} ${possessiveName(asker)} request`
}

/** What letting their agent answer a Slack ask lets it do, in a line: the summary choice's limits, in the owner's words. */
export const LET_MY_AGENT_ANSWER_DETAIL = `${LET_MY_AGENT_ANSWER}: it replies in its own words, without copying passages from your documents or sharing links.`

/** The reply's button while it is on its way. */
export const SENDING_REPLY = "Sending…"

/** The choice that lets their agent answer, as its button says it while it is on its way. */
export const ASKING_AGENT = "Asking your agent…"

/** From how many characters a reply shows its count: the last 500 of the limit. */
export const REPLY_COUNTER_FROM = REPLY_MAX_CHARACTERS - 500

/** How many characters a reply is, as Go counts them: what is left once trimmed, in code points. */
export function replyLength(text: string): number {
  return [...text.trim()].length
}

/**
 * Why a reply cannot be sent yet, in a sentence, or nothing when it can:
 * Go takes 1 to `REPLY_MAX_CHARACTERS` characters of trimmed text.
 */
export function replyProblem(text: string): string | undefined {
  const length = replyLength(text)
  if (length === 0) return "Write your reply first."
  if (length > REPLY_MAX_CHARACTERS) {
    return "That is too long: a reply holds at most 4,000 characters."
  }
  return undefined
}
