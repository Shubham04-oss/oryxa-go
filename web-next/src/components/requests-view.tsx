// Agents → Requests (DESIGN.md §2, §3): what waits on the member and what
// they asked. Inbox holds the requests addressed to them, those waiting on
// them first; Sent holds theirs; Activity is what happened to their Slack
// asks. A filter narrows the two lists to where a request was made. The lists
// are re-read on focus and on a timer while open, so a request decided
// elsewhere, in Slack or on another device, settles here. A request a link or
// a note named opens first, above the lists.
import { useEffect, useMemo, useRef, useState } from "react"
import type { KeyboardEvent } from "react"
import { useQuery } from "@tanstack/react-query"
import "./requests-view.css"
import { AgentsNotice } from "./agents-parts"
import { agentsErrorMessage, plural } from "./agents-format"
import { RequestCard, RequestCardById } from "./request-card"
import { waitsOnOwner } from "./request-card-format"
import { RequestsActivity } from "./requests-activity"
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

function RequestList({
  organizationId,
  viewerId,
  box,
  origin,
  hideId,
  leases,
}: {
  organizationId: string
  viewerId: string
  box: AgentRequestBox
  origin: OriginFilter
  /** A request shown above the lists instead. */
  hideId: string | null
  leases: ReadonlyMap<string, AgentLease>
}) {
  const people = usePeopleNames(organizationId)
  const query = useQuery(requestListQueryOptions(organizationId, box, origin))

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

  const requests = query.data.filter((request) => request.id !== hideId)
  if (requests.length === 0) {
    const words = emptyWords[box][origin]
    return <AgentsNotice title={words.title}>{words.body}</AgentsNotice>
  }

  const now = Date.now()
  const waiting =
    box === "inbox"
      ? requests.filter((request) => waitsOnOwner(request, viewerId, now))
      : []
  const groups: Array<{ label?: string; requests: AgentRequest[] }> =
    waiting.length > 0
      ? [
          { label: "Waiting for you", requests: waiting },
          {
            label: "Earlier",
            requests: requests.filter((request) => !waiting.includes(request)),
          },
        ]
      : [{ requests }]

  return (
    <div className="workspace-requests-groups">
      {groups
        .filter((group) => group.requests.length > 0)
        .map((group) => (
          <section
            className="workspace-requests-group"
            key={group.label ?? "all"}
            aria-label={group.label ?? (box === "inbox" ? "Inbox" : "Sent")}
          >
            {group.label && <p className="workspace-eyebrow">{group.label}</p>}
            <ul className="workspace-requests-list">
              {group.requests.map((request) => (
                <li key={request.id}>
                  <RequestCard
                    organizationId={organizationId}
                    viewerId={viewerId}
                    request={request}
                    lease={leases.get(request.id)}
                  />
                </li>
              ))}
            </ul>
          </section>
        ))}
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
  const [pinned, setPinned] = useState<string | null>(focusRequestId ?? null)
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([])

  useEffect(() => {
    if (focusRequestId) setPinned(focusRequestId)
  }, [focusRequestId])

  // The whole inbox, whatever the filter: it says how many wait on the
  // member, and whether an access they gave may still be running.
  const inbox = useQuery(requestListQueryOptions(organizationId, "inbox"))
  const now = Date.now()
  const inboxAll = inbox.data
  const waiting = (inboxAll ?? []).filter((request) =>
    waitsOnOwner(request, viewerId, now)
  ).length
  const accessMayRun = (inboxAll ?? []).some((request) =>
    mayHoldAccess(request, now)
  )
  const activity = useQuery({
    ...activityQueryOptions(organizationId),
    enabled: tab === "activity" || accessMayRun,
  })
  const leases = useMemo(() => leasesByRequest(activity.data), [activity.data])

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

  function openRequest(requestId: string) {
    setPinned(requestId)
  }

  return (
    <div className="workspace-agents-page workspace-requests-page">
      <div className="workspace-requests-column">
        {pinned && (
          <section
            className="workspace-requests-pinned"
            aria-label="Opened request"
          >
            <RequestCardById
              organizationId={organizationId}
              viewerId={viewerId}
              requestId={pinned}
              lease={leases.get(pinned)}
              autoFocus
            />
            <button
              className="workspace-agent-text-button"
              type="button"
              onClick={() => {
                setPinned(null)
                onFocusDone?.()
              }}
            >
              Back to all requests
            </button>
          </section>
        )}

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
                  <span className="workspace-requests-count" aria-hidden="true">
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
              onOpenRequest={openRequest}
            />
          ) : (
            <RequestList
              key={tab}
              organizationId={organizationId}
              viewerId={viewerId}
              box={tab}
              origin={origin}
              hideId={pinned}
              leases={leases}
            />
          )}
        </div>
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
