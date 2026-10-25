// What the Requests page says about its rows and its all-clear (RequestsNew
// and RequestsClear boards, 15 Oct 2026), worked out from the request lists
// alone: nothing here asks Go for more than the lists already hold.
import { clockTime, dayLabel } from "./time-format"
import { agoPhrase } from "./today-format"
import { expiresPhrase } from "./request-card-format"
import {
  isOwnerWritten,
  needsOwnerReply,
  originOf,
} from "@/lib/agents/agent-requests"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import type { OriginFilter } from "./requests-view-queries"

/**
 * The requests made where the filter says: in Slack, by an agent in Oryxa,
 * or anywhere. The lists are read whole, once, and narrowed here, so the
 * filter never makes another read of its own.
 */
export function madeIn(
  requests: readonly AgentRequest[],
  origin: OriginFilter
): AgentRequest[] {
  return origin === "all"
    ? [...requests]
    : requests.filter((request) => originOf(request) === origin)
}

/**
 * A row's tag: for a request that waits on the member, what it waits on
 * ("Wants your reply", "Answer held for you", or how long is left to decide
 * it); for the rest, where it stands, and who answered an answered one.
 * `agentName` is the member's agent, as an inbox row names it.
 */
export function rowTag(
  request: AgentRequest,
  waits: boolean,
  now: number,
  agentName?: string
): string {
  if (waits) {
    if (request.status === "review") return "Answer held for you"
    if (needsOwnerReply(request)) return "Wants your reply"
    return (
      expiresPhrase(request, now)?.replace(/\.$/, "") ?? "Waiting for your OK"
    )
  }
  if (request.status === "completed") {
    if (isOwnerWritten(request)) return "Answered by you"
    const agent = agentName?.trim() || request.target.agent_name.trim()
    return agent ? `Answered by ${agent}` : "Answered"
  }
  const words: Record<AgentRequest["status"], string> = {
    pending: "Waiting",
    approved: "Approved",
    review: "In review",
    rejected: "Declined",
    completed: "Answered",
    failed: "Failed",
    expired: "Expired",
    withheld: "Withheld",
  }
  return words[request.status]
}

/** A row's time: the clock today ("5:30 PM"), how long ago before that. */
export function rowTime(iso: string, now: number): string {
  if (dayLabel(iso, now) === "Today") return clockTime(iso)
  return agoPhrase(iso, now)
}

/** Whether a request matches what the member typed in the list's search. */
export function matchesSearch(
  request: AgentRequest,
  query: string,
  nameOf: (userId: string) => string | undefined
): boolean {
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
  if (words.length === 0) return true
  const parties = [request.requester, request.target]
  const haystack = [
    request.task,
    request.purpose,
    ...parties.flatMap((party) => [
      nameOf(party.user_id),
      party.user_display_name,
      party.user_email,
      party.agent_name,
    ]),
  ]
    .filter((part): part is string => typeof part === "string")
    .join(" ")
    .toLowerCase()
  return words.every((word) => haystack.includes(word))
}

/** The all-clear's numbers for today, from the member's inbox. */
export type ClearStats = {
  /** Requests made to the member today. */
  asks: number
  /** Answered today by their agent. */
  byAgent: number
  /** Answered today in their own words. */
  byYou: number
  /** How long, on average, an ask decided today waited for its decision, in ms; undefined with none. */
  averageWaitMs?: number
}

/**
 * Today's numbers, by the member's own clock: what was asked of them, who
 * answered, and how long the asks decided today waited. Read from the inbox
 * the page already holds, so only as far as it reaches.
 */
export function clearStats(
  inbox: readonly AgentRequest[],
  viewerId: string,
  now: number
): ClearStats {
  const today = (iso: string | undefined) =>
    Boolean(iso) && dayLabel(iso ?? "", now) === "Today"
  const mine = inbox.filter((request) => request.target.user_id === viewerId)
  const asks = mine.filter((request) => today(request.created_at)).length
  const answeredToday = mine.filter(
    (request) =>
      request.status === "completed" &&
      today(request.decision?.decided_at ?? request.updated_at)
  )
  const byYou = answeredToday.filter((request) =>
    isOwnerWritten(request)
  ).length
  const waits = mine.flatMap((request) => {
    const decidedAt = request.decision?.decided_at
    if (!decidedAt || !today(decidedAt)) return []
    const wait = Date.parse(decidedAt) - Date.parse(request.created_at)
    return Number.isFinite(wait) && wait >= 0 ? [wait] : []
  })
  return {
    asks,
    byAgent: answeredToday.length - byYou,
    byYou,
    averageWaitMs:
      waits.length > 0
        ? waits.reduce((sum, wait) => sum + wait, 0) / waits.length
        : undefined,
  }
}

/** A wait as the all-clear says it: "under a minute", "4 min", "2 h". */
export function waitWords(ms: number): string {
  const minutes = Math.round(ms / 60_000)
  if (minutes < 1) return "under a minute"
  if (minutes < 60) return `${minutes} min`
  const hours = Math.round(minutes / 60)
  return `${hours} h`
}
