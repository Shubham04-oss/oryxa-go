// Cross-member agent requests ("interactions"): one member's agent asks
// another member's agent for bounded work, the target member approves or
// rejects it, then answers it. Over HTTP the caller always acts as their
// primary agent, so the inbox holds every request addressed to any of their
// agents. Handlers are in agents/primary/internal/httpserver/server.go; the
// rules are in agents/primary/internal/interactions.
import {
  ApiError,
  apiFetch,
  apiRequest,
  readApiResponse,
  requestError,
} from "@/lib/api/client"
import { organizationPath, pick, withSearch } from "./http"
import type { ReadOptions } from "./http"

export type AgentRequestBox = "inbox" | "sent"

/**
 * Where a request was made: `agent` for one an agent filed in Oryxa (every
 * request made before Slack asks, and any without an origin), `slack` for one
 * the Slack app made for the member who mentioned it. Go filters on these two
 * words (`?origin=`), and answers 400 to any other.
 */
export type AgentRequestOrigin = "agent" | "slack"

/**
 * The permissions a Slack request asks for, both at once: an owner's approval
 * grants one or both of them, and which decides what the answer may be
 * (`AgentRequestConsent`).
 */
export const CONSENT_SUMMARY_PERMISSION = "consent:summary"
export const CONSENT_ACCESS_PERMISSION = "consent:access"

/**
 * How far an owner's approval let the answer go: `summary`, an AI summary
 * alone (nothing copied from what the agent read, no links), or `access`,
 * excerpts of what it read, which also gives the asker a lease on their
 * follow-ups in that Slack thread. A rejection carries none.
 */
export type AgentRequestConsent = "summary" | "access"

/** The three choices an owner has of a request that asks for consent. */
export type AgentRequestConsentChoice = AgentRequestConsent | "deny"

/**
 * What a recorded decision's `consent` may say: how far an approval let the
 * answer go (`AgentRequestConsent`), or `reply`, the owner answered in their
 * own words and their agent never ran. `reply` is not a choice of
 * `POST …/consent`; it has a route of its own (`replyToAgentRequest`).
 */
export type AgentRequestDecisionConsent = AgentRequestConsent | "reply"

/** The longest an owner's own reply may be, in characters as Go counts them, once trimmed. */
export const REPLY_MAX_CHARACTERS = 4000

/**
 * `expired` is computed when read: a pending or approved request past
 * `expires_at`. `review` holds a finished answer the answer guard found
 * personal data in until its target member reviews it (not terminal);
 * `withheld` is one they chose not to share, or one that expired before
 * they reviewed it.
 */
export type AgentRequestStatus =
  | "pending"
  | "approved"
  | "review"
  | "rejected"
  | "completed"
  | "failed"
  | "expired"
  | "withheld"

/**
 * The toolboxes a request may ask for (`requested_tools`) and its approval
 * allow (`allowed_tools`, a subset): `apps`, the target member's connected
 * apps, read-only, and `knowledge`, the team's knowledge search. Go takes
 * other names too, but they grant nothing.
 */
export const AGENT_REQUEST_TOOLBOXES = ["apps", "knowledge"] as const

export type AgentRequestToolbox = (typeof AGENT_REQUEST_TOOLBOXES)[number]

/**
 * What the answer guard found in a result another member reads: nothing
 * (`clear`), only kinds it always masks (`masked`, delivered masked), or a
 * kind it holds for the target member's review (`held`, status `review`).
 * `kinds` are entity type names only (`EMAIL`, `PHONE`, …), never what
 * matched; `UNCHECKED` when the check itself failed.
 */
export type AgentRequestGuard = {
  state: "clear" | "masked" | "held"
  /** Absent when it found nothing; for a held answer, only the kinds it holds it for. */
  kinds?: string[]
}

/** The target member's choice for a held answer: share it, share it fully masked, or not at all. */
export type AgentRequestReviewAction = "send" | "send_masked" | "withhold"

/**
 * What a recorded review says was done: one of the choices above, or `reply`,
 * the owner wrote their own answer in place of the held one
 * (`replyToAgentRequest`, which `POST …/review` does not take).
 */
export type AgentRequestReviewed = AgentRequestReviewAction | "reply"

/**
 * Who recorded a decision or a review. `kind` is `member` (the addressed
 * member, in their own session on the web or on the notice Oryxa sent them in
 * Slack), `agent`, `system` (Oryxa closing a request), `lease` (Oryxa
 * approving a follow-up under access its owner gave) or `bypass` (Oryxa
 * approving a request that needed no owner's click). A kind this file does
 * not know is still a string: read it as data.
 */
export type AgentRequestDecider = {
  kind: string
  /** The member who decided, or whose agent did; for a lease, the owner who gave it. */
  user_id?: string
  agent_id?: string
  /** Where a member decided (`web`, `slack`), or `lease` and `bypass` for those. */
  surface?: string
  /**
   * Why Oryxa decided, as a short code and never anyone's words: the lease's
   * id for a lease, why a bypass applied (`priority_channel`, `org_chart`),
   * and `answered_elsewhere` for a request closed because another owner's
   * answer settled its ask.
   */
  reason?: string
}

/**
 * The target member's review of a held answer: made by them (`decided_by`
 * a `member`), or a withholding by Oryxa (`system`), with its `reason`:
 * "expired before review" (its `decided_at` the request's expiry), or
 * "Oryxa withheld this answer because …" when the request no longer stood
 * in its teamspace as it was to be sent.
 */
export type AgentRequestReview = {
  action: AgentRequestReviewed
  decided_by: AgentRequestDecider
  decided_at: string
  reason?: string
}

export type AgentRequestParty = {
  user_id: string
  agent_id: string
  agent_name: string
  user_email?: string
  user_display_name?: string
}

export type AgentRequestContextItem = { label: string; value: string }

export type AgentRequestDecision = {
  decision: "approved" | "rejected"
  /**
   * A member's own words; an agent's decision reads in fixed words
   * ("Approved by their agent." or "Declined by their agent."), and a
   * request Oryxa closed says why ("Oryxa closed this request because …").
   */
  message: string
  allowed_tools: string[]
  granted_permissions: string[]
  decided_at: string
  /** Who decided: absent from a decision an older build recorded. */
  decided_by?: AgentRequestDecider
  /**
   * How far the approval let the answer go, or `reply` when the owner
   * answered in their own words; absent from a rejection and from an approval
   * that granted neither consent.
   */
  consent?: AgentRequestDecisionConsent
}

export type AgentRequestResult = {
  status: "completed" | "failed"
  summary: string
  content: string
  tools_used: string[]
  artifacts: string[]
  completed_at: string
  /**
   * The owner wrote this answer themselves (`replyToAgentRequest`): it is
   * their words, not their agent's. The owner and the asker both read it;
   * absent from an answer the agent wrote.
   */
  written_by_owner?: boolean
}

/**
 * An answer held for its target member's review, in the two versions they may
 * send: `result`, as the guardrail left it, and `masked_result`, with every
 * held value replaced by its kind too, absent for an answer that could not be
 * checked. Only the target member's own reads carry it, and only while the
 * request is in review; `lib/side/asking.ts` reads it the same way
 * (`HELD_ANSWER_FIELDS`).
 */
export type AgentRequestHeld = {
  result: AgentRequestResult
  masked_result?: AgentRequestResult
}

/** One request (`agents.Interaction`). */
export type AgentRequest = {
  id: string
  organization_id: string
  /** The teamspace it was made in; absent for a request made outside one. */
  teamspace_id?: string
  /**
   * Who decides it, fixed when it was made: `agent` (the addressed agent, or
   * its member's primary agent), or `owner` (only the target member, in
   * their own session: a teammate's primary agent, or a specialist
   * contributed with `request` delegation). Anything but `agent` is held for
   * the owner.
   */
  approval?: "agent" | "owner"
  /**
   * Who should answer, judged when it was made: `owner` when answering needs
   * the owner's own judgment (an approval, decision, plan, commitment or
   * opinion, rather than a fact they already wrote down), so nothing runs
   * their agent until they choose to let it; `agent` otherwise. Absent from
   * older requests, which means `agent`.
   */
  answer_by?: "owner" | "agent"
  requester: AgentRequestParty
  target: AgentRequestParty
  purpose: string
  task: string
  context: AgentRequestContextItem[]
  /** Toolbox names (`AGENT_REQUEST_TOOLBOXES`): declared, selected by Jev, or the knowledge fallback (`tools_judged_by`). */
  requested_tools: string[]
  /** Who chose `requested_tools`: `jev`, the `requester`, or `none` (knowledge fallback; older records may request no tools). */
  tools_judged_by?: "jev" | "requester" | "none"
  requested_permissions: string[]
  status: AgentRequestStatus
  decision?: AgentRequestDecision
  /**
   * Absent for the requester while the answer is in `review`, and once it
   * is `withheld`. While it is in review the target member's reads carry
   * the held answer instead, which `src/lib/side/asking.ts` reads
   * (`HELD_ANSWER_FIELDS`).
   */
  result?: AgentRequestResult
  /** The answer guard's finding, once the target's agent answered: the requester reads it only beside a result. */
  guard?: AgentRequestGuard
  /** The answer held for the target member's review; their reads carry it while the status is `review`. */
  held?: AgentRequestHeld
  /** The target member's review of a held answer, once they made it. */
  review?: AgentRequestReview
  /**
   * The Slack message the request belongs to: one message put to several
   * owners makes one request for each, and the first delivered answer settles
   * them all. Absent from a request an agent made.
   */
  ask_id?: string
  /** Where it was made; absent means `agent`. */
  origin?: AgentRequestOrigin
  /**
   * When the member who asked opened the one-time secret their owner shared
   * for this request. Only ever a time: what the secret held is not kept.
   */
  secret_viewed_at?: string
  revision: number
  expires_at: string
  created_at: string
  updated_at: string
}

/** Only the target member decides, and only while the request is pending. */
export type DecideAgentRequestInput = {
  decision: "approved" | "rejected"
  /** Required; up to 4,000 characters. */
  message: string
  /** A subset of `requested_tools`; nothing when rejecting. */
  allowed_tools?: string[]
  /** A subset of `requested_permissions`; nothing when rejecting. */
  granted_permissions?: string[]
}

/** Only the target member answers, and only once the request is approved. */
export type RespondToAgentRequestInput = {
  status: "completed" | "failed"
  /** Required; up to 4,000 characters. */
  summary: string
  /** Up to 20,000 characters. */
  content?: string
  /** Within the decision's `allowed_tools`; up to 32 entries of 1–120 characters. */
  tools_used?: string[]
  /** Up to 32 entries of 1–120 characters. */
  artifacts?: string[]
}

function requestsPath(organizationId: string, ...segments: string[]) {
  return organizationPath(organizationId, "agent-requests", ...segments)
}

/** What a listing can be narrowed by, besides which box it is. */
export type ListAgentRequestsOptions = ReadOptions & {
  limit?: number
  /** Only the requests made there (`?origin=`). */
  origin?: AgentRequestOrigin
  /** Only the requests of one Slack message (`?ask_id=`). */
  askId?: string
}

/**
 * `GET …/agent-requests/{inbox|sent}[?limit=&origin=&ask_id=]`
 * (server.interactionInbox, server.interactionSent): latest activity first;
 * 20 by default, at most 100. A filter left out lists everything, and one
 * sent blank is a 400, so only a filter that is set is sent.
 */
export async function listAgentRequests(
  organizationId: string,
  box: AgentRequestBox,
  { limit, origin, askId, signal }: ListAgentRequestsOptions = {}
): Promise<AgentRequest[]> {
  const result = await apiRequest<{ requests: AgentRequest[] }>(
    withSearch(requestsPath(organizationId, box), {
      limit,
      origin,
      ask_id: askId,
    }),
    { signal }
  )
  // Older Go builds ignore origin, so narrow their mixed lists here too.
  return origin
    ? result.requests.filter((request) => originOf(request) === origin)
    : result.requests
}

/** What a member asks someone's agent directly (agents.InteractionCreateInput). */
export type CreateAgentRequestInput = {
  target_agent_id: string
  /**
   * The shared teamspace the request belongs to: who approves it then follows
   * the owner's choice there. Left out, the agent's owner always approves it
   * first, and the request is a line in the DM between the two of them.
   */
  teamspace_id?: string
  /** Why this agent: at most 500 characters. */
  purpose: string
  /** The question: at most 10,000 characters. */
  task: string
}

/** The most a request's task may hold (interactions.maxTaskRunes). */
export const AGENT_REQUEST_TASK_MAX = 10_000

/**
 * `POST …/agent-requests` (server.createInteraction), in the member's own
 * session: files a request to another member's agent. It waits for that
 * member's approval (see `teamspace_id`), and its answer lands in Requests →
 * Sent.
 */
export function createAgentRequest(
  organizationId: string,
  input: CreateAgentRequestInput
): Promise<AgentRequest> {
  return apiRequest<AgentRequest>(requestsPath(organizationId), {
    method: "POST",
    body: pick(input, ["target_agent_id", "teamspace_id", "purpose", "task"]),
  })
}

/** `GET …/agent-requests/{requestID}` (server.getInteraction); 404 unless the caller sent or received it. */
export function getAgentRequest(
  organizationId: string,
  requestId: string,
  { signal }: ReadOptions = {}
): Promise<AgentRequest> {
  return apiRequest<AgentRequest>(requestsPath(organizationId, requestId), {
    signal,
  })
}

/** `POST …/agent-requests/{requestID}/decision` (server.decideInteraction). */
export function decideAgentRequest(
  organizationId: string,
  requestId: string,
  input: DecideAgentRequestInput
): Promise<AgentRequest> {
  return apiRequest<AgentRequest>(
    requestsPath(organizationId, requestId, "decision"),
    {
      method: "POST",
      body: pick(input, [
        "decision",
        "message",
        "allowed_tools",
        "granted_permissions",
      ]),
    }
  )
}

/** `POST …/agent-requests/{requestID}/response` (server.respondInteraction). */
export function respondToAgentRequest(
  organizationId: string,
  requestId: string,
  input: RespondToAgentRequestInput
): Promise<AgentRequest> {
  return apiRequest<AgentRequest>(
    requestsPath(organizationId, requestId, "response"),
    {
      method: "POST",
      body: pick(input, [
        "status",
        "summary",
        "content",
        "tools_used",
        "artifacts",
      ]),
    }
  )
}

/**
 * `POST …/agent-requests/{requestID}/review` (server.reviewInteraction): the
 * target member, in their own session, sends an answer the guard held for
 * them, sends it masked (only when the held answer has a masked version), or
 * withholds it. Answers the request as it now stands. A 409 says it was
 * reviewed already, was closed, or has expired; `request_expired` and
 * `request_closed` name which.
 */
export function reviewAgentRequest(
  organizationId: string,
  requestId: string,
  action: AgentRequestReviewAction
): Promise<AgentRequest> {
  return apiRequest<AgentRequest>(
    requestsPath(organizationId, requestId, "review"),
    { method: "POST", body: { action } }
  )
}

// ---------------------------------------------------------------------------
// What a request that asks for consent is
// ---------------------------------------------------------------------------

/**
 * Whether a request asks its owner for the three consent choices: the ones
 * the Slack app makes ask for both consent permissions at once, and no other
 * request does (`interactions.asksConsent`). Any other request is decided
 * with a decision, as ever.
 */
export function asksForConsent(
  request: Pick<AgentRequest, "requested_permissions">
): boolean {
  return (
    request.requested_permissions.includes(CONSENT_SUMMARY_PERMISSION) &&
    request.requested_permissions.includes(CONSENT_ACCESS_PERMISSION)
  )
}

/**
 * Whether the request needs its owner's own reply (`answer_by: "owner"`):
 * nothing runs their agent until they choose to let it. A request that says
 * nothing, as every older one does, is for the agent.
 */
export function needsOwnerReply(
  request: Pick<AgentRequest, "answer_by">
): boolean {
  return request.answer_by === "owner"
}

/**
 * Whether the answer is the owner's own words, not their agent's. Go marks
 * the answer (`result.written_by_owner`); the two records of how it came to be
 * (`decision.consent: "reply"`, `review.action: "reply"`) say the same, so any
 * one of them is enough.
 */
export function isOwnerWritten(
  request: Pick<AgentRequest, "result" | "decision" | "review">
): boolean {
  return (
    request.result?.written_by_owner === true ||
    request.decision?.consent === "reply" ||
    request.review?.action === "reply"
  )
}

/** Where a request was made: a request that names none was an agent's. */
export function originOf(
  request: Pick<AgentRequest, "origin">
): AgentRequestOrigin {
  return request.origin === "slack" ? "slack" : "agent"
}

/**
 * Whether a one-time secret can be shared for the request, or opened: it was
 * approved with access (a summary alone may say only where a secret lives),
 * and is approved, in review or answered, not closed, rejected, withheld or
 * expired (`interactions.secretable`). Go decides each call again; this is
 * only what to offer.
 */
export function canCarrySecret(
  request: Pick<AgentRequest, "status" | "decision">
): boolean {
  return (
    request.decision?.consent === "access" &&
    (request.status === "approved" ||
      request.status === "review" ||
      request.status === "completed")
  )
}

// ---------------------------------------------------------------------------
// The owner's choice
// ---------------------------------------------------------------------------

/**
 * Access an owner gave an asker in one Slack thread: for its length, the
 * asker's follow-ups there are approved with no click. It ends when it runs
 * out or the owner revokes it (`interactions.Lease`).
 */
export type AgentLease = {
  id: string
  organization_id: string
  asker_user_id: string
  owner_user_id: string
  channel_id: string
  thread_ts: string
  /** The request the owner gave it on; absent from one Oryxa made no request for. */
  request_id?: string
  created_at: string
  expires_at: string
  revoked_at?: string
}

/** Whether a lease still approves follow-ups: neither run out nor revoked. */
export function isLeaseLive(lease: AgentLease, now = Date.now()): boolean {
  return !lease.revoked_at && Date.parse(lease.expires_at) > now
}

/** What an owner's choice came to: the request as it now stands, and the lease `access` gave. */
export type ConsentAgentRequestResult = {
  request: AgentRequest
  lease?: AgentLease
}

/**
 * Who won a choice that lost, as Go reports it in a 409 `already_decided`:
 * what the request's decision says. A request that is not decided, such as
 * one that expired, has none of it.
 */
export type AgentRequestWinner = {
  request_id: string
  status: AgentRequestStatus
  revision: number
  decision?: "approved" | "rejected"
  consent?: AgentRequestDecisionConsent
  decided_by?: AgentRequestDecider
  decided_at?: string
}

/**
 * The owner's choice lost: another click, in Slack or on the web, decided the
 * request first. `winner` says how, when Go could.
 */
export class AgentRequestAlreadyDecided extends ApiError {
  constructor(
    message: string,
    readonly winner: AgentRequestWinner | undefined
  ) {
    super(message, 409, "already_decided")
    this.name = "AgentRequestAlreadyDecided"
  }
}

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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function text(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined
}

/** Go's `winner`, read as data: what is not what it should be is left out. */
export function winnerOf(value: unknown): AgentRequestWinner | undefined {
  if (!isRecord(value)) return undefined
  const { request_id, status, revision, decision, consent, decided_at } = value
  if (
    typeof request_id !== "string" ||
    typeof status !== "string" ||
    !requestStatuses.has(status) ||
    typeof revision !== "number"
  ) {
    return undefined
  }
  const by = value.decided_by
  return {
    request_id,
    status: status as AgentRequestStatus,
    revision,
    ...(decision === "approved" || decision === "rejected" ? { decision } : {}),
    ...(consent === "summary" || consent === "access" || consent === "reply"
      ? { consent }
      : {}),
    ...(isRecord(by) && typeof by.kind === "string"
      ? {
          decided_by: {
            kind: by.kind,
            ...(text(by.user_id) ? { user_id: text(by.user_id) } : {}),
            ...(text(by.agent_id) ? { agent_id: text(by.agent_id) } : {}),
            ...(text(by.surface) ? { surface: text(by.surface) } : {}),
            ...(text(by.reason) ? { reason: text(by.reason) } : {}),
          },
        }
      : {}),
    ...(text(decided_at) ? { decided_at: text(decided_at) } : {}),
  }
}

async function jsonBody(response: Response): Promise<unknown> {
  try {
    return await response.json()
  } catch {
    return undefined
  }
}

/**
 * `POST …/agent-requests/{requestID}/consent` (server.consentInteraction): the
 * owner of a request that asks for consent chooses `summary`, `access` or
 * `deny`, in their own session. Whoever decides first wins, here or on the
 * button of the notice in Slack, and a choice that lost throws
 * `AgentRequestAlreadyDecided` carrying the winner. Other refusals are an
 * `ApiError`: 409 `request_expired`, 400 for a request that does not ask for
 * consent, 403 for one that is not the caller's, 503 `consent_unavailable`
 * where Slack consent is not set up.
 */
export async function consentAgentRequest(
  organizationId: string,
  requestId: string,
  choice: AgentRequestConsentChoice
): Promise<ConsentAgentRequestResult> {
  const response = await apiFetch(
    requestsPath(organizationId, requestId, "consent"),
    { method: "POST", body: { choice } }
  )
  return readApiResponse(response, async (current) => {
    if (current.ok) return current.json()
    return refuseOwnerChoice(current)
  })
}

/**
 * What an owner's choice that Go refused throws: `AgentRequestAlreadyDecided`,
 * carrying the winner, for a 409 `already_decided` (the same shape for
 * `…/consent` and `…/reply`), and an `ApiError` for every other refusal.
 */
async function refuseOwnerChoice(response: Response): Promise<never> {
  if (response.status === 409) {
    const body = await jsonBody(response.clone())
    const failure = isRecord(body) && isRecord(body.error) ? body.error : {}
    if (failure.code === "already_decided") {
      throw new AgentRequestAlreadyDecided(
        text(failure.message) ?? "agent request was already decided",
        isRecord(body) ? winnerOf(body.winner) : undefined
      )
    }
  }
  throw await requestError(response)
}

/**
 * `POST …/agent-requests/{requestID}/reply` (server.replyInteraction): the
 * owner answers in their own words, in their own session, and their agent
 * never runs. It is for a request that waits on their decision (it becomes
 * approved and completed at once, `decision.consent: "reply"`) and for one
 * whose answer is held for their review (their text replaces it,
 * `review.action: "reply"`). Either way the answer is `result` with
 * `written_by_owner` set. `text` is trimmed by Go and is 1 to
 * `REPLY_MAX_CHARACTERS` characters (a 400 says otherwise). Whoever decides
 * first wins, here or on the notice in Slack, and a reply that lost throws
 * `AgentRequestAlreadyDecided` carrying the winner. Other refusals are an
 * `ApiError`: 403 for one that is not the caller's (or an agent's token),
 * 404 for one that cannot be read, 409 `request_expired` or `request_closed`.
 * Answers the request as the owner now reads it.
 */
export async function replyToAgentRequest(
  organizationId: string,
  requestId: string,
  replyText: string
): Promise<AgentRequest> {
  const response = await apiFetch(
    requestsPath(organizationId, requestId, "reply"),
    { method: "POST", body: { text: replyText } }
  )
  return readApiResponse(response, async (current) => {
    if (current.ok) {
      const answered: { request: AgentRequest } = await current.json()
      return answered.request
    }
    return refuseOwnerChoice(current)
  })
}

// ---------------------------------------------------------------------------
// Leases and Activity
// ---------------------------------------------------------------------------

/**
 * `DELETE …/agent-leases/{leaseID}` (server.revokeLease): the owner ends,
 * before it runs out, the access they gave an asker in one Slack thread. From
 * then on the asker's follow-ups there ask again. Answers the lease as it now
 * stands; revoking one that already ended changes nothing, and one that is
 * not the caller's is a 404.
 */
export async function revokeAgentLease(
  organizationId: string,
  leaseId: string
): Promise<AgentLease> {
  const result = await apiRequest<{ lease: AgentLease }>(
    organizationPath(organizationId, "agent-leases", leaseId),
    { method: "DELETE" }
  )
  return result.lease
}

/** What an entry of the member's activity says happened. */
export type AgentActivityKind =
  | "asked"
  | "decided"
  | "answered"
  | "secret_viewed"
  | "lease_given"
  | "lease_revoked"
  | "accessed"

/**
 * One allow from the access decision log (internal/access.Decision): someone
 * else, or one of the member's own agents, reaching a file, a collection, a
 * document or a source, and the rule that let it.
 */
export type AgentAccessDecision = {
  at: string
  service: string
  action: "read" | "search" | "write" | "delete"
  resource_kind: "file" | "corpus" | "document" | "source"
  resource_id: string
  /** The member whose it is; left out for a team's or the organization's. */
  owner_id?: string
  actor_user_id: string
  /** Set when an agent reached it, rather than its member in their own session. */
  actor_agent_id?: string
  /** The agent's kind: `primary` or `specialist`. */
  actor_agent_role?: string
  /** The rule that granted it. */
  reason:
    | "owner"
    | "org_admin"
    | "team_member"
    | "team_admin"
    | "organization"
    | "grant"
}

/**
 * One thing that happened to the member's Slack asks, as the ledger holds it:
 * names, states and times, never a question or an answer (the request it
 * points to is read, as the member, for those).
 */
export type AgentActivityEntry = {
  kind: AgentActivityKind
  at: string
  /** Which side of it the member is: the one asked (`owner`) or the one who asked (`asker`). */
  role: "owner" | "asker"
  /** The other member. */
  with_user_id?: string
  request_id?: string
  ask_id?: string
  /** The request's status now. */
  status?: AgentRequestStatus
  /** On a `decided` entry: approved or rejected, how far the answer may go, who decided, where and why. */
  decision?: "approved" | "rejected"
  consent?: AgentRequestDecisionConsent
  decided_by?: AgentRequestDecider
  /** On the two lease entries. */
  lease?: AgentLease
  /**
   * On an `accessed` entry: who reached what, and why. `role` is `owner` when
   * someone, or the member's own agent, reached the member's thing, and
   * `asker` when one of the member's agents reached someone else's.
   */
  access?: AgentAccessDecision
}

/**
 * `GET …/agent-requests/activity[?limit=]` (server.interactionActivity): what
 * happened to the member's Slack asks, newest first: asks made and received,
 * how each was decided and where, bypasses and leases among the decisions,
 * answers, leases given and revoked, and secrets opened; and, from the access
 * decision log, who reached the member's things and what their agents
 * reached. It lists what the ledger still has.
 */
export async function listAgentActivity(
  organizationId: string,
  { limit, signal }: ReadOptions & { limit?: number } = {}
): Promise<AgentActivityEntry[]> {
  const result = await apiRequest<{ activity: AgentActivityEntry[] }>(
    withSearch(requestsPath(organizationId, "activity"), { limit }),
    { signal }
  )
  return result.activity
}

// ---------------------------------------------------------------------------
// One-time secrets
// ---------------------------------------------------------------------------
//
// A secret is never in Slack, in an event, in the ledger or in a model's
// context. The owner of a request approved with access pastes it in
// themselves; the member who asked opens it once, in their own session; the
// owner can take it back before either. What a secret holds is a value only
// these calls carry: a caller passes it straight through, and never through a
// query cache or a mutation's variables and data, which TanStack Query keeps
// after they settle (as `lib/apps/client.ts` does for its own secrets).

/** The longest a secret waits to be opened, and the shortest, in seconds. */
export const SECRET_MAX_TTL_SECONDS = 3600
export const SECRET_MIN_TTL_SECONDS = 60

/** What a shared secret says of itself: when it stops being there to open. */
export type SharedAgentSecret = { request_id: string; expires_at: string }

/** A secret as the member who asked opens it, once. */
export type OpenedAgentSecret = { value: string; expires_at: string }

/**
 * `PUT …/agent-requests/{requestID}/secret` (server.putSecret): the owner
 * shares a secret for a request they approved with access. It waits to be
 * opened for `ttlSeconds` (60 to 3600, and an hour when left out; Go may
 * shorten it to the organization's consent expiry), replaces any secret
 * already shared for the request, and is never answered back.
 */
export function shareAgentRequestSecret(
  organizationId: string,
  requestId: string,
  { value, ttlSeconds }: { value: string; ttlSeconds?: number }
): Promise<SharedAgentSecret> {
  return apiRequest<SharedAgentSecret>(
    requestsPath(organizationId, requestId, "secret"),
    {
      method: "PUT",
      body: {
        value,
        ...(ttlSeconds === undefined ? {} : { ttl_seconds: ttlSeconds }),
      },
    }
  )
}

/**
 * `GET …/agent-requests/{requestID}/secret` (server.viewSecret): the member
 * who asked opens the secret their owner shared, once. Opening deletes it,
 * so this is not a read to repeat, prefetch or put in a query: a second call
 * is 410 `secret_gone`, as it is for one that was taken back, expired or
 * never shared, and Go tells no more of which.
 */
export function openAgentRequestSecret(
  organizationId: string,
  requestId: string
): Promise<OpenedAgentSecret> {
  return apiRequest<OpenedAgentSecret>(
    requestsPath(organizationId, requestId, "secret")
  )
}

/**
 * `DELETE …/agent-requests/{requestID}/secret` (server.revokeSecret): the
 * owner takes back a secret they shared, before it is opened or expires.
 * Answers 204 whether or not one was still there.
 */
export function revokeAgentRequestSecret(
  organizationId: string,
  requestId: string
): Promise<void> {
  return apiRequest(requestsPath(organizationId, requestId, "secret"), {
    method: "DELETE",
  })
}

/** Whether an error says a secret is not there to open: opened, taken back, expired or never shared. */
export function isSecretGone(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    (error.status === 410 || error.code === "secret_gone")
  )
}
