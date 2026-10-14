// A request as its member reads it (DESIGN.md §3, "Requests"): who asked, the
// question, where it came from, where it stands and what is theirs to do:
// choose what their agent may share with the person who asked (a Slack ask:
// Share answer, Allow for 1 hour or Deny), share the answer or deny (any other
// request), review an answer the guard held, reply themselves instead (under
// any of those, or first, for an ask that needs their own judgment), end
// access they gave, share a one-time secret, or, for the one who asked, open
// it. Requests reads the lists and hands each card its request; a card that
// is given only an id (`RequestCardById`) reads Go itself, as the member, and
// settles when the request changes elsewhere.
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
import { ReplyFirst, ReplyOption } from "./request-card-reply"
import type { FirstChoice } from "./request-card-reply"
import { OpenSecret, ShareSecret } from "./request-card-secret"
import {
  DENY,
  LET_MY_AGENT_ANSWER_DETAIL,
  SHARE_ANSWER,
  consentChoicePending,
  consentChoices,
  denyName,
  expiresPhrase,
  isOpenToDecide,
  originLabel,
  readsLine,
  replyProblem,
  requestTitle,
  askerName,
  ownerName,
  shareAnswerName,
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
  isOwnerWritten,
  needsOwnerReply,
  originOf,
  replyToAgentRequest,
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

const replyCopy = {
  request_expired: "This request expired before you replied.",
  request_closed: "Oryxa closed this request.",
  invalid_state: "This request was decided already, or expired.",
  forbidden: "Only the person this was addressed to can reply.",
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
  const people = usePeopleNames(organizationId, viewerId)
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
  // What the owner has written to reply with, kept until it is sent or
  // dropped, and whether the last send was refused for it.
  const [draft, setDraft] = useState("")
  const [replyInvalid, setReplyInvalid] = useState(false)
  const cardRef = useRef<HTMLElement>(null)
  const noteRef = useRef<HTMLInputElement>(null)
  const replyRef = useRef<HTMLTextAreaElement>(null)
  const noteId = useId()
  const choicesId = useId()

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
  // Reply yourself is there wherever the owner may decide or review. One that
  // needs their own judgment leads with it, in place of the usual choices.
  const canReply = canChoose || canDecide || canReview
  const replyFirst = needsOwnerReply(request) && (canChoose || canDecide)
  const ownerWrote = isOwnerWritten(request)
  // Words that can no longer be sent (the request was decided or ran out
  // meanwhile, here or elsewhere) stay readable, to copy.
  const unsent = side === "owner" && !canReply && draft.trim() !== ""

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
    // Whatever was written has been sent, or given up for another choice.
    setDraft("")
    setReplyInvalid(false)
    queryClient.setQueryData(
      [...agentKeys.request(organizationId, updated.id), viewerId],
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

  // Share answer approves, with the tools the request asked for: the card
  // offers no approval without them (Go still takes one). Deny declines.
  const decide = useMutation({
    mutationFn: (decision: "approved" | "rejected") =>
      decideAgentRequest(organizationId, request.id, {
        decision,
        // Go requires a note: without one, the decision says where it was made.
        message:
          note.trim() ||
          (decision === "approved" ? "Approved in Oryxa." : "Denied in Oryxa."),
        ...(decision === "approved"
          ? { allowed_tools: allowedTools(request) }
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

  // The owner's own words instead of their agent's. Their agent never runs
  // for it, and the request settles as answered. A reply that lost the race
  // (decided or reviewed first, here or in Slack) says who won, as a choice
  // does; any other failure keeps what was written to try again.
  const reply = useMutation({
    mutationFn: (replyText: string) =>
      replyToAgentRequest(organizationId, request.id, replyText),
    onSuccess: async (answered) => {
      await settle(answered)
      cardRef.current?.focus()
    },
    onError: (error) => {
      if (error instanceof AgentRequestAlreadyDecided) {
        void refreshRequests(queryClient, organizationId)
        if (request.status === "review") {
          // The winner's record is of the decision, not of the review.
          setProblem(reviewCopy.invalid_state)
          return
        }
        setProblem("")
        setLost({ winner: error.winner, revision: request.revision })
        return
      }
      failed(error, "Couldn't send your reply. Try again.", replyCopy)
      replyRef.current?.focus()
    },
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
    reply.isPending ||
    endAccess.isPending

  function editReply(value: string) {
    setDraft(value)
    // What was refused was the old text: the field stops saying so.
    if (replyInvalid) {
      setReplyInvalid(false)
      setProblem("")
    }
  }

  function sendReply() {
    if (busy) return
    const issue = replyProblem(draft)
    if (issue) {
      setProblem(issue)
      setReplyInvalid(true)
      replyRef.current?.focus()
      return
    }
    setProblem("")
    setReplyInvalid(false)
    reply.mutate(draft.trim())
  }

  function dropReply() {
    setDraft("")
    setProblem("")
    setReplyInvalid(false)
  }

  // "Let my agent answer" is Share answer, and Deny is Deny, whichever way
  // the request asks: the Slack ask's consent choices, or a decision.
  function letAgentAnswer() {
    setProblem("")
    if (consentAsk) consent.mutate("summary")
    else decide.mutate("approved")
  }

  function denyInstead() {
    setProblem("")
    if (consentAsk) consent.mutate("deny")
    else decide.mutate("rejected")
  }

  const firstPressed: FirstChoice | undefined = reply.isPending
    ? "reply"
    : (consent.isPending && consent.variables === "summary") ||
        (decide.isPending && decide.variables === "approved")
      ? "agent"
      : consent.isPending || decide.isPending
        ? "deny"
        : undefined

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

      {canChoose && !replyFirst && (
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
                aria-label={choice.name(askerShort)}
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

      {canDecide && !replyFirst && (
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
          <div className="workspace-request-actions">
            <button
              className="workspace-agent-pill"
              type="button"
              aria-label={denyName(askerShort)}
              disabled={busy}
              onClick={() => decide.mutate("rejected")}
            >
              {decide.isPending && decide.variables === "rejected"
                ? "Denying…"
                : DENY}
            </button>
            <button
              className="workspace-agent-pill"
              data-primary="true"
              type="button"
              aria-label={shareAnswerName(askerShort)}
              disabled={busy}
              onClick={() => decide.mutate("approved")}
            >
              {decide.isPending && decide.variables === "approved"
                ? "Sharing…"
                : SHARE_ANSWER}
            </button>
          </div>
        </div>
      )}

      {replyFirst && (
        <ReplyFirst
          askerName={askerShort}
          value={draft}
          onChange={editReply}
          busy={busy}
          invalid={replyInvalid}
          textareaRef={replyRef}
          onSend={sendReply}
          pressed={firstPressed}
          agentDetail={consentAsk ? LET_MY_AGENT_ANSWER_DETAIL : undefined}
          onAgent={letAgentAnswer}
          onDeny={denyInstead}
        />
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

      {canReply && !replyFirst && (
        <ReplyOption
          askerName={askerShort}
          value={draft}
          onChange={editReply}
          busy={busy}
          sending={reply.isPending}
          invalid={replyInvalid}
          textareaRef={replyRef}
          // A reply to a held answer takes its place: its owner is told so.
          hint={
            canReview
              ? `${askerShort} gets your words instead of ${agent}'s answer.`
              : undefined
          }
          onSend={sendReply}
          onCancel={dropReply}
        />
      )}

      {unsent && (
        <div className="workspace-request-shared">
          <p className="workspace-request-held-label">
            Your reply wasn't sent. It's here if you want to copy it.
          </p>
          <div className="workspace-request-answer" data-by="owner">
            {draft.trim()}
          </div>
        </div>
      )}

      {shared !== undefined && (
        <div className="workspace-request-shared">
          {!ownerWrote && (
            <p className="workspace-request-held-label">
              {side === "owner" ? "What was shared" : "The answer"}
            </p>
          )}
          {ownerWrote ? (
            // Their own words, as written: never read as Markdown.
            <div className="workspace-request-answer" data-by="owner">
              {shared}
            </div>
          ) : (
            <div className="workspace-request-answer">
              <AgentMarkdown text={shared} />
            </div>
          )}
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
  const people = usePeopleNames(organizationId, viewerId)
  const query = useQuery(
    requestDetailQueryOptions(organizationId, requestId, viewerId)
  )

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
