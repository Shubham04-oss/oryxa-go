// A request as its member reads it (DESIGN.md §3, "Requests"): who asked, the
// question, where it came from, where it stands and what is theirs to do:
// choose what their agent may share with the person who asked (Slack asks),
// approve or decline (any other request), review an answer the guard held,
// end access they gave, share a one-time secret, or, for the one who asked,
// open it. Requests reads the lists and hands each card its request; a card
// that is given only an id (`RequestCardById`) reads Go itself, as the member,
// and settles when the request changes elsewhere.
import { useEffect, useId, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  Ban,
  CircleAlert,
  CircleCheck,
  Clock3,
  LoaderCircle,
} from "lucide-react"
import "./request-card.css"
import { AgentMarkdown } from "./agent-timeline"
import { agentsErrorMessage } from "./agents-format"
import { clockTime } from "./direct-message-format"
import { HeldAnswerReview } from "./request-card-review"
import { OpenSecret, ShareSecret } from "./request-card-secret"
import {
  consentChoicePending,
  consentChoices,
  expiresPhrase,
  isOpenToDecide,
  originLabel,
  readsLine,
  requestTitle,
  askerName,
  ownerName,
  shortName,
  sideOf,
  statusOf,
  targetAgentName,
  winnerLine,
} from "./request-card-format"
import type { Reader, Tone } from "./request-card-format"
import {
  refreshRequests,
  requestDetailQueryOptions,
  usePeopleNames,
} from "./requests-view-queries"
import { fullTime, personIdentity } from "./teamspace-format"
import { IdentityAvatar } from "./workspace-avatar"
import { ApiError } from "@/lib/api/client"
import {
  AGENT_REQUEST_TOOLBOXES,
  AgentRequestAlreadyDecided,
  asksForConsent,
  canCarrySecret,
  consentAgentRequest,
  decideAgentRequest,
  isLeaseLive,
  originOf,
  revokeAgentLease,
  reviewAgentRequest,
} from "@/lib/agents/agent-requests"
import type {
  AgentLease,
  AgentRequest,
  AgentRequestConsentChoice,
  AgentRequestReviewAction,
  AgentRequestWinner,
} from "@/lib/agents/agent-requests"
import { isUnavailableHere } from "@/lib/agents/policy"
import { agentKeys } from "@/lib/agents/queries"
import { resultText } from "@/lib/side/asking"

/** From how many characters a card folds its question or answer away behind "Show more". */
const LONG_TEXT = 280

/** How often a card that can still run out re-reads the clock. */
const CLOCK_MS = 30_000

/** How long access lasts, at most: a follow-up approved under it is recent for this long. */
const LEASE_MS = 60 * 60_000

function isLong(text: string): boolean {
  return [...text].length > LONG_TEXT || text.split("\n").length > 6
}

/** The time now, and again every half minute while `active`: a request can run out while its card is open. */
function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const timer = window.setInterval(() => setNow(Date.now()), CLOCK_MS)
    return () => window.clearInterval(timer)
  }, [active])
  return now
}

const consentCopy = {
  request_expired: "This request expired before you chose.",
  request_closed: "Oryxa closed this request.",
  consent_unavailable: "Slack consent isn't set up on this server.",
  forbidden: "Only the person this was addressed to can choose.",
  invalid_request: "This request doesn't ask for consent.",
  not_found: "This request can't be found any more.",
} as const

const decideCopy = {
  request_expired: "This request expired before you answered.",
  request_closed: "Oryxa closed this request.",
  invalid_state: "This request was decided already, or expired.",
  forbidden: "Only the person this was addressed to can decide it.",
  not_found: "This request can't be found any more.",
} as const

const reviewCopy = {
  request_expired: "This request expired before its answer was reviewed.",
  request_closed:
    "Oryxa withheld this answer, as its request no longer stands.",
  invalid_state: "This answer was reviewed already, or has expired.",
  forbidden: "Only the person whose agent answered can review its answer.",
  not_found: "This request can't be found any more.",
} as const

const leaseCopy = {
  not_found: "That access has already ended.",
  unavailable_here: "Access isn't set up on this server.",
} as const

function isConflict(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409
}

function CardIcon({ tone }: { tone: Tone }) {
  const props = { size: 14, strokeWidth: 1.8, "aria-hidden": true } as const
  switch (tone) {
    case "waiting":
      return <Clock3 {...props} />
    case "working":
      return <LoaderCircle {...props} className="workspace-agent-spin" />
    case "done":
      return <CircleCheck {...props} />
    case "ended":
      return <Ban {...props} />
    case "failed":
      return <CircleAlert {...props} />
  }
}

/** The toolboxes the request asked for that Go knows, which an approval may allow. */
function allowedTools(request: AgentRequest): string[] {
  return request.requested_tools.filter((tool) =>
    (AGENT_REQUEST_TOOLBOXES as readonly string[]).includes(tool)
  )
}

export type RequestCardProps = {
  organizationId: string
  /** The signed-in member: what is theirs to do depends on which side of the request they are. */
  viewerId: string
  request: AgentRequest
  /** Access the member gave on this request, when the page knows of it (Activity lists them). */
  lease?: AgentLease
  /** Takes focus as it opens, for the card a link named. */
  autoFocus?: boolean
}

/**
 * A request on a card. What it offers is the member's to do and nobody
 * else's: Go refuses anyone else, and this only leaves the buttons out. An
 * owner whose choice loses to another click, in Slack or here, sees who won.
 */
export function RequestCard({
  organizationId,
  viewerId,
  request: given,
  lease: knownLease,
  autoFocus,
}: RequestCardProps) {
  const queryClient = useQueryClient()
  const people = usePeopleNames(organizationId)
  // What a change made on this card answered, until the lists read it too.
  const [latest, setLatest] = useState<AgentRequest>()
  const [ownLease, setOwnLease] = useState<AgentLease>()
  // A choice that lost, and the revision it lost at: the card says so until
  // the request it lost to is read.
  const [lost, setLost] = useState<{
    winner?: AgentRequestWinner
    revision: number
  }>()
  const [problem, setProblem] = useState("")
  const [note, setNote] = useState("")
  const [noting, setNoting] = useState(false)
  const [folded, setFolded] = useState(true)
  const cardRef = useRef<HTMLElement>(null)
  const noteRef = useRef<HTMLInputElement>(null)
  const noteId = useId()
  const choicesId = useId()
  const noToolsId = useId()

  const request = latest && latest.revision > given.revision ? latest : given
  const stillOpen =
    request.status === "pending" ||
    request.status === "approved" ||
    request.status === "review"
  const now = useNow(stillOpen || Boolean(knownLease ?? ownLease))
  const reader: Reader = { viewerId, nameOf: people.nameOf, now }

  const side = sideOf(request, viewerId)
  const consentAsk = asksForConsent(request)
  const open = isOpenToDecide(request, now)
  const lostNow =
    lost !== undefined &&
    request.status === "pending" &&
    request.revision <= lost.revision
  const canChoose = side === "owner" && consentAsk && open && !lostNow
  const canDecide = side === "owner" && !consentAsk && open && !lostNow
  const canReview = side === "owner" && request.status === "review"

  const asker = askerName(request, reader)
  const owner = ownerName(request, reader)
  const askerShort = shortName(
    reader,
    request.requester.user_id,
    request.requester
  )
  const ownerShort = shortName(reader, request.target.user_id, request.target)
  const agent = targetAgentName(request, reader)
  const title = requestTitle(request, reader)
  const status = statusOf(request, reader)
  const origin = originLabel(request)
  const reads = open && !lostNow ? readsLine(request, reader) : undefined
  const asksForTools = allowedTools(request).length > 0
  const expires = expiresPhrase(request, now)
  const long = isLong(request.task)
  const shared =
    request.status === "completed" && request.result
      ? resultText({ result: request.result })
      : undefined

  // The person on the other side of the request is who the card shows.
  const otherId =
    side === "owner" ? request.requester.user_id : request.target.user_id
  const otherName = side === "owner" ? asker : owner

  useEffect(() => {
    if (autoFocus) cardRef.current?.focus()
  }, [autoFocus])

  useEffect(() => {
    if (noting) noteRef.current?.focus()
  }, [noting])

  function settle(updated: AgentRequest): Promise<void> {
    setLatest(updated)
    setProblem("")
    queryClient.setQueryData(
      agentKeys.request(organizationId, updated.id),
      updated
    )
    return refreshRequests(queryClient, organizationId)
  }

  function failed(
    error: unknown,
    fallback: string,
    copy: Record<string, string>
  ) {
    setProblem(agentsErrorMessage(error, fallback, copy))
    // Decided, closed or expired meanwhile: read it again to say which.
    if (isConflict(error)) void refreshRequests(queryClient, organizationId)
  }

  const consent = useMutation({
    mutationFn: (choice: AgentRequestConsentChoice) =>
      consentAgentRequest(organizationId, request.id, choice),
    onSuccess: async ({ request: decided, lease }) => {
      if (lease) setOwnLease(lease)
      await settle(decided)
      cardRef.current?.focus()
    },
    onError: (error) => {
      if (error instanceof AgentRequestAlreadyDecided) {
        setProblem("")
        setLost({ winner: error.winner, revision: request.revision })
        void refreshRequests(queryClient, organizationId)
        return
      }
      failed(error, "Couldn't record your choice. Try again.", consentCopy)
    },
  })

  const decide = useMutation({
    mutationFn: (input: {
      decision: "approved" | "rejected"
      withTools: boolean
    }) =>
      decideAgentRequest(organizationId, request.id, {
        decision: input.decision,
        // Go requires a note: without one, the decision says where it was made.
        message:
          note.trim() ||
          (input.decision === "approved"
            ? "Approved in Oryxa."
            : "Declined in Oryxa."),
        ...(input.decision === "approved"
          ? { allowed_tools: input.withTools ? allowedTools(request) : [] }
          : {}),
      }),
    onSuccess: async (updated) => {
      await settle(updated)
      cardRef.current?.focus()
    },
    onError: (error) =>
      failed(error, "Couldn't record your decision. Try again.", decideCopy),
  })

  const review = useMutation({
    mutationFn: (action: AgentRequestReviewAction) =>
      reviewAgentRequest(organizationId, request.id, action),
    onSuccess: async (updated) => {
      await settle(updated)
      cardRef.current?.focus()
    },
    onError: (error) =>
      failed(error, "Couldn't record your review. Try again.", reviewCopy),
  })

  const endAccess = useMutation({
    mutationFn: (leaseId: string) => revokeAgentLease(organizationId, leaseId),
    onSuccess: async (ended) => {
      setOwnLease(ended)
      setProblem("")
      await refreshRequests(queryClient, organizationId)
    },
    onError: (error) =>
      failed(error, "Couldn't end that access. Try again.", leaseCopy),
  })

  const busy =
    consent.isPending ||
    decide.isPending ||
    review.isPending ||
    endAccess.isPending

  // Access the owner gave the asker in the request's Slack thread: this
  // card's own, or the one a follow-up was approved under.
  const lease = ownLease ?? knownLease
  const decidedBy = request.decision?.decided_by
  const followUpLeaseId =
    decidedBy?.kind === "lease" &&
    request.decision &&
    now - Date.parse(request.decision.decided_at) < LEASE_MS
      ? decidedBy.reason
      : undefined
  const leaseId = lease?.id ?? followUpLeaseId
  const accessEnded = lease?.revoked_at !== undefined
  const accessLive = lease
    ? isLeaseLive(lease, now)
    : followUpLeaseId !== undefined
  const showAccess =
    side === "owner" && leaseId !== undefined && (accessLive || accessEnded)

  // Who asked and who was asked come by id, from the members list: nobody
  // is called "someone" for the moment it takes.
  if (people.status === "loading") {
    return (
      <p className="workspace-agents-muted" role="status">
        Loading the request…
      </p>
    )
  }

  return (
    <article
      className="workspace-request-card"
      data-tone={status.tone}
      data-side={side}
      data-origin={originOf(request)}
      data-status={request.status}
      aria-label={`${title}, in ${origin}`}
      aria-busy={busy ? "true" : undefined}
      tabIndex={-1}
      ref={cardRef}
    >
      <header className="workspace-request-head">
        <IdentityAvatar identity={personIdentity(otherId, otherName)} />
        <div className="workspace-request-title">
          <strong>{title}</strong>
          <span>
            <span
              className="workspace-request-origin"
              data-origin={originOf(request)}
            >
              {origin}
            </span>
            {!consentAsk && request.purpose ? ` · ${request.purpose}` : ""}
          </span>
        </div>
        <time
          dateTime={request.created_at}
          title={fullTime(request.created_at)}
        >
          {clockTime(request.created_at)}
        </time>
      </header>

      <p
        className="workspace-request-task"
        data-folded={long && folded ? "true" : undefined}
      >
        {request.task}
      </p>
      {long && (
        <button
          className="workspace-agent-text-button"
          type="button"
          aria-expanded={!folded}
          onClick={() => setFolded((was) => !was)}
        >
          {folded ? "Show more" : "Show less"}
        </button>
      )}
      {reads && <p className="workspace-request-reads">{reads}</p>}

      <p className="workspace-request-status" role="status">
        <CardIcon tone={status.tone} />
        <span>{status.text}</span>
      </p>
      {status.detail && (
        <p className="workspace-request-detail">{status.detail}</p>
      )}
      {open && expires && side === "owner" && (
        <p className="workspace-request-detail">{expires}</p>
      )}
      {lostNow && (
        <p className="workspace-request-lost" role="status">
          <strong>Already decided.</strong>{" "}
          {lost.winner
            ? winnerLine(lost.winner, request, reader)
            : "Another click got there first."}
        </p>
      )}

      {canChoose && (
        <div
          className="workspace-request-choices"
          role="group"
          aria-label={`Choose what ${agent} may share with ${askerShort}`}
        >
          {consentChoices.map((choice) => {
            const pressed =
              consent.isPending && consent.variables === choice.choice
            return (
              <button
                className="workspace-request-choice"
                data-choice={choice.choice}
                type="button"
                key={choice.choice}
                aria-label={choice.label}
                aria-describedby={`${choicesId}-${choice.choice}`}
                disabled={busy}
                onClick={() => {
                  setProblem("")
                  consent.mutate(choice.choice)
                }}
              >
                <strong>
                  {pressed ? consentChoicePending(choice.choice) : choice.label}
                </strong>
                <span id={`${choicesId}-${choice.choice}`}>
                  {choice.detail(askerShort)}
                </span>
              </button>
            )
          })}
        </div>
      )}

      {canDecide && (
        <div className="workspace-request-decide">
          {noting ? (
            <label className="workspace-request-note" htmlFor={noteId}>
              <span className="sr-only">
                A note for {askerShort} (optional)
              </span>
              <input
                id={noteId}
                ref={noteRef}
                type="text"
                value={note}
                maxLength={4000}
                placeholder={`A note for ${askerShort} (optional)`}
                disabled={busy}
                onChange={(event) => setNote(event.target.value)}
              />
            </label>
          ) : (
            <button
              className="workspace-agent-text-button"
              type="button"
              onClick={() => setNoting(true)}
            >
              Add a note
            </button>
          )}
          {asksForTools && (
            <p id={noToolsId} className="workspace-request-reads">
              Approve without tools means {agent} can’t look anything up.
            </p>
          )}
          <div className="workspace-request-actions">
            <button
              className="workspace-agent-pill"
              type="button"
              aria-label={`Decline ${agent}'s request from ${asker}`}
              disabled={busy}
              onClick={() =>
                decide.mutate({ decision: "rejected", withTools: false })
              }
            >
              {decide.isPending && decide.variables.decision === "rejected"
                ? "Declining…"
                : "Decline"}
            </button>
            {asksForTools && (
              <button
                className="workspace-agent-pill"
                type="button"
                aria-label={`Approve ${agent}'s request from ${asker} without tools`}
                aria-describedby={noToolsId}
                disabled={busy}
                onClick={() =>
                  decide.mutate({ decision: "approved", withTools: false })
                }
              >
                {decide.isPending &&
                decide.variables.decision === "approved" &&
                !decide.variables.withTools
                  ? "Approving…"
                  : "Approve without tools"}
              </button>
            )}
            <button
              className="workspace-agent-pill"
              data-primary="true"
              type="button"
              aria-label={
                asksForTools
                  ? `Approve with tools for ${agent}'s request from ${asker}`
                  : `Approve ${agent}'s request from ${asker}`
              }
              disabled={busy}
              onClick={() =>
                decide.mutate({ decision: "approved", withTools: true })
              }
            >
              {decide.isPending &&
              decide.variables.decision === "approved" &&
              decide.variables.withTools
                ? "Approving…"
                : asksForTools
                  ? "Approve with tools"
                  : "Approve"}
            </button>
          </div>
        </div>
      )}

      {canReview && (
        <HeldAnswerReview
          request={request}
          agentName={agent}
          askerName={askerShort}
          busy={review.isPending}
          pressed={review.isPending ? review.variables : undefined}
          onReview={(action) => {
            setProblem("")
            review.mutate(action)
          }}
          onReload={() => void refreshRequests(queryClient, organizationId)}
        />
      )}

      {shared !== undefined && (
        <div className="workspace-request-shared">
          <p className="workspace-request-held-label">
            {side === "owner" ? "What was shared" : "The answer"}
          </p>
          <div className="workspace-request-answer">
            <AgentMarkdown text={shared} />
          </div>
        </div>
      )}

      {showAccess && (
        <div className="workspace-request-access">
          {accessEnded ? (
            <p role="status">
              You ended {askerShort}'s access. Their next question in that
              thread asks you again.
            </p>
          ) : (
            <>
              <p>
                {lease
                  ? `${askerShort}'s follow-ups in that Slack thread are answered without asking you until ${clockTime(lease.expires_at)}.`
                  : `${askerShort}'s follow-ups in that Slack thread are answered without asking you while this access lasts.`}
              </p>
              <button
                className="workspace-agent-pill"
                type="button"
                aria-label={`End ${askerShort}'s access`}
                disabled={busy}
                onClick={() => {
                  setProblem("")
                  endAccess.mutate(leaseId)
                }}
              >
                {endAccess.isPending ? "Ending…" : "End access"}
              </button>
            </>
          )}
        </div>
      )}

      {canCarrySecret(request) &&
        side === "owner" &&
        Date.parse(request.expires_at) > now && (
          <ShareSecret
            organizationId={organizationId}
            request={request}
            askerName={askerShort}
            now={now}
          />
        )}
      {canCarrySecret(request) && side === "asker" && (
        <OpenSecret
          organizationId={organizationId}
          request={request}
          ownerName={ownerShort}
          now={now}
        />
      )}

      {problem && (
        <p className="workspace-agent-error" role="alert">
          {problem}
        </p>
      )}
    </article>
  )
}

/**
 * A request read by its id, as the member: for wherever a request is named
 * without being listed (a link, a note in a conversation). It settles as the
 * request changes, on focus and on a timer, and says plainly when it cannot
 * be read.
 */
export function RequestCardById({
  organizationId,
  viewerId,
  requestId,
  lease,
  autoFocus,
}: {
  organizationId: string
  viewerId: string
  requestId: string
  lease?: AgentLease
  autoFocus?: boolean
}) {
  const people = usePeopleNames(organizationId)
  const query = useQuery(requestDetailQueryOptions(organizationId, requestId))

  if (query.isPending || people.status === "loading") {
    return (
      <p className="workspace-agents-muted" role="status">
        Loading the request…
      </p>
    )
  }
  if (query.isError) {
    const notFound =
      query.error instanceof ApiError && query.error.status === 404
    return (
      <div
        className="workspace-request-missing"
        role={notFound ? undefined : "alert"}
      >
        <p>
          {notFound
            ? "This request can't be found. It may not be yours, or it may be gone."
            : isUnavailableHere(query.error)
              ? "Requests aren't set up on this server."
              : agentsErrorMessage(query.error, "Couldn't load this request.")}
        </p>
        {!notFound && !isUnavailableHere(query.error) && (
          <button
            className="workspace-agent-pill"
            type="button"
            onClick={() => void query.refetch()}
          >
            Try again
          </button>
        )}
      </div>
    )
  }
  return (
    <RequestCard
      organizationId={organizationId}
      viewerId={viewerId}
      request={query.data}
      lease={lease}
      autoFocus={autoFocus}
    />
  )
}
