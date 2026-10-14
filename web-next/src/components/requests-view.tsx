// Agents → Requests (DESIGN.md §2, §3): what waits on the member and what
// they asked. Inbox holds the requests addressed to them, those waiting on
// them first; Sent holds theirs; Activity is what happened to their Slack
// asks. A filter narrows the two lists to where a request was made. The lists
// are re-read on focus and on a timer while open, so a request decided
// elsewhere, in Slack or on another device, settles here.
//
// One request at a time (founder's redesign, 14 Oct 2026): the lists are
// compact rows, and the request a row, a link or a note names opens beside
// them as its card. Without one, the first that waits opens; with none
// waiting, the side says every ask is answered. On a narrow canvas the card
// replaces the list, with a way back.
import { useEffect, useMemo, useRef, useState } from "react"
import type { KeyboardEvent } from "react"
import { useQuery } from "@tanstack/react-query"
import "./requests-view.css"
import { AgentsNotice } from "./agents-parts"
import { agentsErrorMessage, plural } from "./agents-format"
import { RequestCard, RequestCardById } from "./request-card"
import { originLabel, waitsOnOwner } from "./request-card-format"
import { IdentityAvatar } from "./workspace-avatar"
import { personIdentity } from "./people-format"
import { agoPhrase } from "./today-format"
import { needsOwnerReply } from "@/lib/agents/agent-requests"
import { RequestsActivity } from "./requests-activity"
import { useStableLayout } from "./requests-view-layout"
import {
  activityQueryOptions,
  requestListQueryOptions,
  usePeopleNames,
} from "./requests-view-queries"
import type { OriginFilter } from "./requests-view-queries"
import { AGENT_REQUESTS_LIMIT } from "@/lib/agents/queries"
import type {
  AgentActivityEntry,
  AgentLease,
  AgentRequest,
  AgentRequestBox,
} from "@/lib/agents/agent-requests"
import { isUnavailableHere } from "@/lib/agents/policy"

type Tab = "inbox" | "sent" | "activity"

const tabs: ReadonlyArray<{ key: Tab; label: string }> = [
  { key: "inbox", label: "Inbox" },
  { key: "sent", label: "Sent" },
  { key: "activity", label: "Activity" },
]

const filters: ReadonlyArray<{ key: OriginFilter; label: string }> = [
  { key: "all", label: "All" },
  { key: "slack", label: "Slack" },
  { key: "agent", label: "Oryxa" },
]

/** How long access lasts, at most, and a minute over for the clocks. */
const LEASE_WINDOW_MS = 61 * 60_000

/** The access the member gave, by the request it was given on, from Activity's lease entries. */
function leasesByRequest(
  entries: readonly AgentActivityEntry[] | undefined
): Map<string, AgentLease> {
  const leases = new Map<string, AgentLease>()
  for (const entry of entries ?? []) {
    if (
      entry.kind === "lease_given" &&
      entry.role === "owner" &&
      entry.lease?.request_id
    ) {
      leases.set(entry.lease.request_id, entry.lease)
    }
  }
  return leases
}

/**
 * Whether a request the member chose "Allow for 1 hour" on may still hold
 * access: the card asks Activity for the lease only then, so an inbox of old
 * requests costs nothing.
 */
function mayHoldAccess(request: AgentRequest, now: number): boolean {
  const decision = request.decision
  return (
    decision?.consent === "access" &&
    decision.decided_by?.kind === "member" &&
    now - Date.parse(decision.decided_at) < LEASE_WINDOW_MS
  )
}

const emptyWords: Record<
  AgentRequestBox,
  Record<OriginFilter, { title: string; body: string }>
> = {
  inbox: {
    all: {
      title: "Nothing is waiting for you.",
      body: "When someone asks your agent a question, in Slack or in Oryxa, it shows up here.",
    },
    slack: {
      title: "No requests from Slack.",
      body: "Questions people ask you in Slack show up here.",
    },
    agent: {
      title: "No requests from agents in Oryxa.",
      body: "Requests another member's agent files with yours show up here.",
    },
  },
  sent: {
    all: {
      title: "You haven't asked anyone yet.",
      body: "Questions you ask in Slack, and requests your agents make, show up here.",
    },
    slack: {
      title: "You haven't asked anyone in Slack.",
      body: "Mention Oryxa in Slack and name who to ask: your question shows up here.",
    },
    agent: {
      title: "Your agents haven't made a request.",
      body: "Requests your agents file with other members' agents show up here.",
    },
  },
}

/** What a row says a request waits on, or where it stands. */
function rowTag(request: AgentRequest, waits: boolean): string {
  if (waits) {
    if (request.status === "review") return "Answer held for you"
    return needsOwnerReply(request) ? "Needs your reply" : "Waiting for your OK"
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

function RequestRow({
  request,
  box,
  waits,
  selected,
  nameOf,
  now,
  onSelect,
}: {
  request: AgentRequest
  box: AgentRequestBox
  waits: boolean
  selected: boolean
  nameOf: (userId: string) => string | undefined
  now: number
  onSelect: (requestId: string) => void
}) {
  // Inbox rows name who asked; Sent rows name who was asked.
  const party = box === "inbox" ? request.requester : request.target
  const name =
    nameOf(party.user_id) ||
    party.user_display_name?.trim() ||
    party.user_email?.trim() ||
    "Someone"
  const what = request.task.trim() || request.purpose.trim()
  return (
    <button
      type="button"
      className={selected ? "requests-row is-active" : "requests-row"}
      aria-current={selected ? "true" : undefined}
      data-waiting={waits ? "true" : undefined}
      onClick={() => onSelect(request.id)}
    >
      <IdentityAvatar identity={personIdentity(party.user_id, name)} />
      <span className="requests-row-copy">
        <span className="requests-row-head">
          <strong>{name}</strong>
          <small>· {originLabel(request)}</small>
          <time dateTime={request.created_at}>
            {agoPhrase(request.created_at, now)}
          </time>
        </span>
        <span className="requests-row-what">{what}</span>
        <span className="requests-row-tag">{rowTag(request, waits)}</span>
      </span>
    </button>
  )
}

function RequestList({
  organizationId,
  viewerId,
  box,
  origin,
  selectedId,
  onSelect,
}: {
  organizationId: string
  viewerId: string
  box: AgentRequestBox
  origin: OriginFilter
  /** The request open beside the list. */
  selectedId: string | null
  onSelect: (requestId: string) => void
}) {
  const people = usePeopleNames(organizationId, viewerId)
  const query = useQuery(requestListQueryOptions(organizationId, box, origin))
  const requests = query.data ?? []
  const now = Date.now()
  const waitsNow = (request: AgentRequest) =>
    box === "inbox" && waitsOnOwner(request, viewerId, now)
  // Each request keeps the place it first had while the list is open: the one
  // the member has just answered stays under their pointer.
  const layout = useStableLayout(requests, (request) =>
    waitsNow(request) ? "waiting" : "earlier"
  )

  if (query.isPending || people.status === "loading") {
    return (
      <p className="workspace-agents-muted" role="status">
        Loading requests…
      </p>
    )
  }
  if (query.isError) {
    const unavailable = isUnavailableHere(query.error)
    return (
      <AgentsNotice
        title={
          unavailable
            ? "Requests aren't set up on this server."
            : agentsErrorMessage(query.error, "Could not load requests.")
        }
        alert={!unavailable}
        action={
          !unavailable && (
            <button
              className="workspace-agents-button"
              type="button"
              onClick={() => void query.refetch()}
            >
              Try again
            </button>
          )
        }
      />
    )
  }
  if (requests.length === 0) {
    const words = emptyWords[box][origin]
    return <AgentsNotice title={words.title}>{words.body}</AgentsNotice>
  }

  // Headings only while something waits: they are items of the one list, so
  // one going or coming never makes a card again.
  const headed = layout.waiting.some(waitsNow)
  const rows: Array<{ heading: string } | { request: AgentRequest }> = [
    ...(headed ? [{ heading: "Waiting for you" }] : []),
    ...layout.waiting.map((request) => ({ request })),
    ...(headed && layout.earlier.length > 0 ? [{ heading: "Earlier" }] : []),
    ...layout.earlier.map((request) => ({ request })),
  ]

  return (
    <div className="workspace-requests-groups">
      <ul
        className="workspace-requests-list"
        aria-label={box === "inbox" ? "Inbox" : "Sent"}
      >
        {rows.map((row) =>
          "heading" in row ? (
            <li className="workspace-requests-heading" key={row.heading}>
              <p className="workspace-eyebrow">{row.heading}</p>
            </li>
          ) : (
            <li key={row.request.id}>
              <RequestRow
                request={row.request}
                box={box}
                waits={waitsNow(row.request)}
                selected={row.request.id === selectedId}
                nameOf={people.nameOf}
                now={now}
                onSelect={onSelect}
              />
            </li>
          )
        )}
      </ul>
      {query.data.length >= AGENT_REQUESTS_LIMIT && (
        <p className="workspace-agents-note">
          Showing the latest {plural(AGENT_REQUESTS_LIMIT, "request")}.
        </p>
      )}
    </div>
  )
}

export type RequestsViewProps = {
  organizationId: string
  viewerId: string
  /** A request a link or a note named: it opens first, above the lists. */
  focusRequestId?: string | null
  /** The named request was put away. */
  onFocusDone?: () => void
}

/**
 * Requests for a signed-in member of an organization. Tabs Inbox, Sent and
 * Activity; Inbox says how many wait on the member; a filter narrows the two
 * lists to Slack or to agents in Oryxa.
 */
export function RequestsView({
  organizationId,
  viewerId,
  focusRequestId,
  onFocusDone,
}: RequestsViewProps) {
  const [tab, setTab] = useState<Tab>("inbox")
  const [origin, setOrigin] = useState<OriginFilter>("all")
  // The request the member opened: from a row, a link, a note or Activity.
  const [chosen, setChosen] = useState<string | null>(focusRequestId ?? null)
  // Opened by a link, a note or Activity, a request reads itself, as it may
  // not be in the list on show; opened from a row, it is that row's request.
  const [chosenAlone, setChosenAlone] = useState(Boolean(focusRequestId))
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([])

  useEffect(() => {
    if (focusRequestId) {
      setChosen(focusRequestId)
      setChosenAlone(true)
    }
  }, [focusRequestId])

  // The whole inbox, whatever the filter: it says how many wait on the
  // member, and whether an access they gave may still be running.
  const inbox = useQuery(requestListQueryOptions(organizationId, "inbox"))
  const now = Date.now()
  const inboxAll = inbox.data
  const waitingRequests = (inboxAll ?? []).filter((request) =>
    waitsOnOwner(request, viewerId, now)
  )
  const waiting = waitingRequests.length
  const accessMayRun = (inboxAll ?? []).some((request) =>
    mayHoldAccess(request, now)
  )
  const activity = useQuery({
    ...activityQueryOptions(organizationId),
    enabled: tab === "activity" || accessMayRun,
  })
  const leases = useMemo(() => leasesByRequest(activity.data), [activity.data])
  // The list on show, from the same cache its rows read.
  const shown = useQuery({
    ...requestListQueryOptions(
      organizationId,
      tab === "sent" ? "sent" : "inbox",
      origin
    ),
    enabled: tab !== "activity",
  })
  // Without a choice, the first request on show that waits opens, and stays
  // open once answered, so the member sees what their choice did.
  const firstWaiting =
    tab === "inbox"
      ? (shown.data ?? []).find((request) =>
          waitsOnOwner(request, viewerId, now)
        )?.id
      : undefined
  const [lastOpened, setLastOpened] = useState<string | null>(null)
  useEffect(() => {
    if (firstWaiting) setLastOpened(firstWaiting)
  }, [firstWaiting])
  const stillListed =
    lastOpened &&
    (shown.data ?? []).some((request) => request.id === lastOpened)
      ? lastOpened
      : null
  const openId =
    chosen ?? (tab === "inbox" ? (firstWaiting ?? stillListed) : null)
  const openRequest = openId
    ? [...(shown.data ?? []), ...(inboxAll ?? [])].find(
        (request) => request.id === openId
      )
    : undefined
  const allClear =
    !openId && tab === "inbox" && inbox.isSuccess && waiting === 0

  function selectTab(index: number) {
    setTab(tabs[index].key)
    tabRefs.current[index]?.focus()
  }

  function onTabKey(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const last = tabs.length - 1
    const target =
      event.key === "ArrowRight"
        ? index === last
          ? 0
          : index + 1
        : event.key === "ArrowLeft"
          ? index === 0
            ? last
            : index - 1
          : event.key === "Home"
            ? 0
            : event.key === "End"
              ? last
              : undefined
    if (target === undefined) return
    event.preventDefault()
    selectTab(target)
  }

  function choose(requestId: string) {
    setChosen(requestId)
    setChosenAlone(false)
  }

  function chooseFromActivity(requestId: string) {
    setChosen(requestId)
    setChosenAlone(true)
  }

  function putAway() {
    setChosen(null)
    onFocusDone?.()
  }

  return (
    <div
      className="workspace-agents-page workspace-requests-page"
      data-open={chosen ? "true" : undefined}
    >
      <div className="requests-split">
        <div className="workspace-requests-column requests-side">
          <div className="workspace-requests-bar">
            <div
              className="workspace-requests-tabs"
              role="tablist"
              aria-label="Requests"
            >
              {tabs.map((item, index) => (
                <button
                  className={
                    tab === item.key
                      ? "workspace-work-tab is-active"
                      : "workspace-work-tab"
                  }
                  type="button"
                  role="tab"
                  key={item.key}
                  id={`requests-tab-${item.key}`}
                  aria-selected={tab === item.key}
                  aria-controls={`requests-panel-${item.key}`}
                  aria-label={
                    item.key === "inbox" && waiting > 0
                      ? `Inbox, ${waiting} waiting`
                      : undefined
                  }
                  tabIndex={tab === item.key ? 0 : -1}
                  ref={(element) => {
                    tabRefs.current[index] = element
                  }}
                  onClick={() => setTab(item.key)}
                  onKeyDown={(event) => onTabKey(event, index)}
                >
                  {item.label}
                  {item.key === "inbox" && waiting > 0 && (
                    <span
                      className="workspace-requests-count"
                      aria-hidden="true"
                    >
                      {waiting}
                    </span>
                  )}
                </button>
              ))}
            </div>
            {tab !== "activity" && (
              <div
                className="workspace-requests-filter"
                role="group"
                aria-label="Where requests were made"
              >
                {filters.map((item) => (
                  <button
                    className="workspace-agent-pill"
                    type="button"
                    key={item.key}
                    aria-pressed={origin === item.key}
                    onClick={() => setOrigin(item.key)}
                  >
                    {item.label}
                  </button>
                ))}
              </div>
            )}
          </div>

          <div
            role="tabpanel"
            id={`requests-panel-${tab}`}
            aria-labelledby={`requests-tab-${tab}`}
          >
            {tab === "activity" ? (
              <RequestsActivity
                organizationId={organizationId}
                viewerId={viewerId}
                onOpenRequest={chooseFromActivity}
              />
            ) : (
              <RequestList
                key={tab}
                organizationId={organizationId}
                viewerId={viewerId}
                box={tab}
                origin={origin}
                selectedId={openId}
                onSelect={choose}
              />
            )}
          </div>
        </div>

        {openId ? (
          <section
            className="requests-detail"
            aria-label={chosen ? "Opened request" : "Current request"}
          >
            {chosen && (
              <button
                className="workspace-agent-text-button requests-back"
                type="button"
                onClick={putAway}
              >
                Back to all requests
              </button>
            )}
            {openRequest && !(chosen && chosenAlone) ? (
              <RequestCard
                key={openRequest.id}
                organizationId={organizationId}
                viewerId={viewerId}
                request={openRequest}
                lease={leases.get(openRequest.id)}
                autoFocus={chosen === openRequest.id}
              />
            ) : (
              <RequestCardById
                key={openId}
                organizationId={organizationId}
                viewerId={viewerId}
                requestId={openId}
                lease={leases.get(openId)}
                autoFocus
              />
            )}
          </section>
        ) : allClear ? (
          <section
            className="requests-detail requests-clear"
            aria-label="All clear"
          >
            <div className="requests-clear-scene" aria-hidden="true" />
            <h2>Every ask answered.</h2>
            <p>The next one will open here.</p>
          </section>
        ) : (
          <div
            className="requests-detail requests-detail-empty"
            aria-hidden="true"
          />
        )}
      </div>
    </div>
  )
}

/**
 * The Requests page as the workspace shell shows it: for a signed-in member
 * of an organization, and, without one (the dev-only layout preview), a
 * notice like the other Agents pages give.
 */
export function RequestsCanvas({
  organizationId,
  viewerId,
  focusRequestId,
  onFocusDone,
}: {
  organizationId?: string
  viewerId?: string
  focusRequestId?: string | null
  onFocusDone?: () => void
}) {
  if (!organizationId || !viewerId) {
    return (
      <div className="workspace-agents-page">
        <AgentsNotice title="Your requests appear here once you're signed in.">
          This preview isn't connected to a workspace, so it has no requests to
          show.
        </AgentsNotice>
      </div>
    )
  }
  return (
    <RequestsView
      organizationId={organizationId}
      viewerId={viewerId}
      focusRequestId={focusRequestId}
      onFocusDone={onFocusDone}
    />
  )
}
