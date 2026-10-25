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
import { Search, X } from "lucide-react"
import "./requests-view.css"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { AgentsNotice } from "./agents-parts"
import { agentsErrorMessage, plural } from "./agents-format"
import { RequestCard, RequestCardById } from "./request-card"
import { originLabel, waitsOnOwner } from "./request-card-format"
import type { Reader } from "./request-card-format"
import { conversationOf, conversationsOf } from "./requests-view-group"
import type { RequestConversation } from "./requests-view-group"
import { RequestThread, partyName } from "./requests-thread"
import { ThinkingOrb } from "thinking-orbs"
import { IdentityAvatar } from "./workspace-avatar"
import { personIdentity } from "./people-format"
import {
  clearStats,
  madeIn,
  matchesSearch,
  rowTag,
  rowTime,
  waitWords,
} from "./requests-view-format"
import { RequestsActivity } from "./requests-activity"
import { useStableLayout } from "./requests-view-layout"
import {
  activityQueryOptions,
  requestListQueryOptions,
  usePeopleNames,
} from "./requests-view-queries"
import type { OriginFilter, PeopleNames } from "./requests-view-queries"
import { AGENT_REQUESTS_LIMIT, agentsQueryOptions } from "@/lib/agents/queries"
import { INBOX_LIMIT, useInbox, useNeedsYou } from "@/hooks/use-needs-you"
import type { PendingChat } from "@/lib/home/confirmations"
import { primaryAgent, isUnavailableHere } from "@/lib/agents/policy"
import type {
  AgentActivityEntry,
  AgentLease,
  AgentRequest,
  AgentRequestBox,
} from "@/lib/agents/agent-requests"

type Tab = "inbox" | "sent" | "activity"

const tabs: ReadonlyArray<{ key: Tab; label: string }> = [
  { key: "inbox", label: "Inbox" },
  { key: "sent", label: "Sent" },
  { key: "activity", label: "Activity" },
]

// The channel is a chip (N12): on, the lists hold that channel's requests;
// pressed again, every channel's.
const filters: ReadonlyArray<{
  key: Exclude<OriginFilter, "all">
  label: string
}> = [
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

/**
 * One conversation's row: who it is with, where, the newest ask, and where
 * it stands; with more than one ask, how many. It opens the ask that waits
 * on the member, else the newest.
 */
function ConversationRow({
  conversation,
  box,
  waiting,
  selected,
  people,
  reader,
  agentName,
  onSelect,
}: {
  conversation: RequestConversation
  box: AgentRequestBox
  /** The ask in it that waits on the member, if one does. */
  waiting?: AgentRequest
  selected: boolean
  people: PeopleNames
  reader: Reader
  /** The member's agent, for "Answered by Krude". */
  agentName?: string
  onSelect: (requestId: string) => void
}) {
  const request = conversation.latest
  // Inbox rows name who asked; Sent rows name who was asked.
  const party = box === "inbox" ? request.requester : request.target
  const { name, hint } = partyName(party, reader.viewerId, people)
  const what = request.task.trim() || request.purpose.trim()
  const count = conversation.requests.length
  const shown = waiting ?? request
  return (
    <button
      type="button"
      className={selected ? "requests-row is-active" : "requests-row"}
      aria-current={selected ? "true" : undefined}
      data-waiting={waiting ? "true" : undefined}
      onClick={() => onSelect(shown.id)}
    >
      <IdentityAvatar identity={personIdentity(party.user_id, name)} />
      <span className="requests-row-copy">
        <span className="requests-row-head">
          <strong>{name}</strong>
          {hint && <small className="requests-row-hint">{hint}</small>}
          <small>· {originLabel(request)}</small>
          <time dateTime={request.created_at}>
            {rowTime(request.created_at, reader.now)}
          </time>
        </span>
        <span className="requests-row-what">{what}</span>
        <span className="requests-row-tag">
          {rowTag(
            shown,
            Boolean(waiting),
            reader,
            box === "inbox" ? agentName : undefined
          )}
          {count > 1 && (
            <span className="requests-row-count"> · {count} messages</span>
          )}
        </span>
      </span>
    </button>
  )
}

/** A chat in which a tool waits for the member's OK: it opens the chat. */
function ChatRow({
  chat,
  onOpen,
}: {
  chat: PendingChat
  onOpen?: (threadId: string) => void
}) {
  const title = chat.title.trim() || "A conversation"
  const count = chat.pending_confirmations
  return (
    <button
      type="button"
      className="requests-row"
      data-waiting="true"
      disabled={!onOpen}
      aria-label={`Open ${title}: ${count} ${count === 1 ? "tool waits" : "tools wait"} for your OK`}
      onClick={() => onOpen?.(chat.id)}
    >
      <ThinkingOrb size={20} state="listening" aria-hidden="true" />
      <span className="requests-row-copy">
        <span className="requests-row-head">
          <strong>Your agent</strong>
          <small>· Chat</small>
        </span>
        <span className="requests-row-what">{title}</span>
        <span className="requests-row-tag">
          {count === 1
            ? "A tool waits for your OK"
            : `${count} tools wait for your OK`}
        </span>
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
  search,
  agentName,
  chats = [],
  onOpenConversation,
}: {
  organizationId: string
  viewerId: string
  box: AgentRequestBox
  origin: OriginFilter
  /** Chats with a tool waiting for the member's OK: they wait on them too. */
  chats?: readonly PendingChat[]
  onOpenConversation?: (threadId: string) => void
  /** What the member typed in the list's search: only the requests it matches show. */
  search: string
  agentName?: string
  /** The request open beside the list. */
  selectedId: string | null
  onSelect: (requestId: string) => void
}) {
  const people = usePeopleNames(organizationId, viewerId)
  // One read per box, whatever the filter: the inbox's is the one every
  // page shares.
  const inbox = useInbox(organizationId, viewerId, box === "inbox")
  const sent = useQuery({
    ...requestListQueryOptions(organizationId, "sent"),
    enabled: box === "sent",
  })
  const query = box === "inbox" ? inbox : sent
  const limit = box === "inbox" ? INBOX_LIMIT : AGENT_REQUESTS_LIMIT
  const now = Date.now()
  const requests = madeIn(query.data ?? [], origin).filter((request) =>
    matchesSearch(request, search, people.nameOf)
  )
  // Chats are no channel's: they show with every request, not a filter's.
  const waitingChats =
    box === "inbox" && origin === "all" && !search.trim() ? chats : []
  const waitsNow = (request: AgentRequest) =>
    box === "inbox" && waitsOnOwner(request, viewerId, now)
  const reader: Reader = { viewerId, nameOf: people.nameOf, now }
  // Asks from one person, in one place, close together, are one row.
  const conversations = conversationsOf(requests, box)
  const waitingIn = (conversation: RequestConversation) =>
    conversation.requests.find(waitsNow)
  // Each conversation keeps the place it first had while the list is open:
  // the one the member has just answered stays under their pointer.
  const layout = useStableLayout(conversations, (conversation) =>
    waitingIn(conversation) ? "waiting" : "earlier"
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
            <Button variant="outline" onClick={() => void query.refetch()}>
              Try again
            </Button>
          )
        }
      />
    )
  }
  if (requests.length === 0 && search.trim()) {
    return (
      <p className="workspace-agents-muted" role="status">
        Nothing matches “{search.trim()}”.
      </p>
    )
  }
  if (requests.length === 0 && box === "sent") {
    const words = emptyWords[box][origin]
    return <AgentsNotice title={words.title}>{words.body}</AgentsNotice>
  }

  // The inbox is headed "Waiting for you N" even at none (the board's
  // all-clear), then "Earlier"; the headings are items of the one list, so
  // one going or coming never makes a card again.
  const headed = box === "inbox"
  // The count is of asks, as every page counts them, not of rows.
  const waitingNow = requests.filter(waitsNow).length + waitingChats.length
  const rows: Array<
    | { heading: string; count?: number }
    | { conversation: RequestConversation }
    | { chat: PendingChat }
  > = [
    ...(headed ? [{ heading: "Waiting for you", count: waitingNow }] : []),
    ...waitingChats.map((chat) => ({ chat })),
    ...layout.waiting.map((conversation) => ({ conversation })),
    ...(headed && layout.earlier.length > 0 ? [{ heading: "Earlier" }] : []),
    ...layout.earlier.map((conversation) => ({ conversation })),
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
              <p className="workspace-eyebrow">
                {row.heading}
                {row.count !== undefined && " "}
                {row.count !== undefined && (
                  <b className="workspace-requests-heading-count">
                    {row.count}
                  </b>
                )}
              </p>
            </li>
          ) : "chat" in row ? (
            <li key={`chat:${row.chat.id}`}>
              <ChatRow chat={row.chat} onOpen={onOpenConversation} />
            </li>
          ) : (
            <li key={row.conversation.id}>
              <ConversationRow
                conversation={row.conversation}
                box={box}
                waiting={waitingIn(row.conversation)}
                selected={row.conversation.requests.some(
                  (request) => request.id === selectedId
                )}
                people={people}
                reader={reader}
                agentName={agentName}
                onSelect={onSelect}
              />
            </li>
          )
        )}
      </ul>
      {requests.length === 0 && waitingChats.length === 0 && (
        <AgentsNotice title={emptyWords[box][origin].title}>
          {emptyWords[box][origin].body}
        </AgentsNotice>
      )}
      {query.data.length >= limit && (
        <p className="workspace-agents-note">
          Showing the latest {plural(limit, "request")}.
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
  /** Opens one of the member's chats: one a tool waits in. */
  onOpenConversation?: (threadId: string) => void
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
  onOpenConversation,
}: RequestsViewProps) {
  const [tab, setTab] = useState<Tab>("inbox")
  const [origin, setOrigin] = useState<OriginFilter>("all")
  // The list's search, filtered here over what the lists hold.
  const [searching, setSearching] = useState(false)
  const [search, setSearch] = useState("")
  const searchRef = useRef<HTMLInputElement>(null)
  const agents = useQuery(agentsQueryOptions(organizationId))
  const agentName = agents.data ? primaryAgent(agents.data)?.name : undefined
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

  // The whole inbox, whatever the filter, and the chats a tool waits in: what
  // waits on the member, counted as every page counts it, and whether an
  // access they gave may still be running.
  const needs = useNeedsYou(organizationId, viewerId)
  const inbox = needs.inbox
  const now = Date.now()
  const inboxAll = inbox.data
  const waiting = needs.count ?? needs.requests?.length ?? 0
  const accessMayRun = (inboxAll ?? []).some((request) =>
    mayHoldAccess(request, now)
  )
  const activity = useQuery({
    ...activityQueryOptions(organizationId),
    enabled: tab === "activity" || accessMayRun,
  })
  const leases = useMemo(() => leasesByRequest(activity.data), [activity.data])
  // The list on show, from the same cache its rows read.
  const sent = useQuery({
    ...requestListQueryOptions(organizationId, "sent"),
    enabled: tab === "sent",
  })
  const shown = madeIn((tab === "sent" ? sent.data : inboxAll) ?? [], origin)
  // Without a choice, the first request on show that waits opens, and stays
  // open once answered, so the member sees what their choice did.
  const firstWaiting =
    tab === "inbox"
      ? shown.find((request) => waitsOnOwner(request, viewerId, now))?.id
      : undefined
  const [lastOpened, setLastOpened] = useState<string | null>(null)
  useEffect(() => {
    if (firstWaiting) setLastOpened(firstWaiting)
  }, [firstWaiting])
  const stillListed =
    lastOpened && shown.some((request) => request.id === lastOpened)
      ? lastOpened
      : null
  const openId =
    chosen ?? (tab === "inbox" ? (firstWaiting ?? stillListed) : null)
  const openRequest = openId
    ? [...shown, ...(inboxAll ?? [])].find((request) => request.id === openId)
    : undefined
  // The conversation the open request is in, read as a thread above its card.
  const people = usePeopleNames(organizationId, viewerId)
  const box: AgentRequestBox = tab === "sent" ? "sent" : "inbox"
  const openConversation =
    openId && tab !== "activity"
      ? conversationOf(conversationsOf(shown, box), openId)
      : undefined
  const reader: Reader = { viewerId, nameOf: people.nameOf, now }
  const allClear =
    !openId && tab === "inbox" && inbox.isSuccess && waiting === 0
  const stats = inboxAll ? clearStats(inboxAll, viewerId, now) : undefined

  useEffect(() => {
    if (searching) searchRef.current?.focus()
  }, [searching])

  function closeSearch() {
    setSearch("")
    setSearching(false)
  }

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
              <Button
                variant="ghost"
                size="icon-sm"
                className="workspace-requests-search-toggle"
                aria-label={searching ? "Close search" : "Search requests"}
                aria-expanded={searching}
                onClick={() => (searching ? closeSearch() : setSearching(true))}
              >
                {searching ? <X /> : <Search />}
              </Button>
            )}
            {tab !== "activity" && searching && (
              <Input
                ref={searchRef}
                type="search"
                className="workspace-requests-search"
                aria-label="Search requests"
                placeholder="Search who asked, or what"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Escape") closeSearch()
                }}
              />
            )}
            {tab !== "activity" && (
              <div
                className="workspace-requests-filter"
                role="group"
                aria-label="Channel"
              >
                {filters.map((item) => (
                  <Button
                    variant={origin === item.key ? "secondary" : "ghost"}
                    size="sm"
                    className="workspace-requests-filter-item"
                    key={item.key}
                    aria-pressed={origin === item.key}
                    onClick={() =>
                      setOrigin(origin === item.key ? "all" : item.key)
                    }
                  >
                    {item.label}
                  </Button>
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
                search={search}
                agentName={agentName}
                chats={needs.chats}
                onOpenConversation={onOpenConversation}
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
            {openConversation && (
              <RequestThread
                conversation={openConversation}
                box={box}
                openId={openId}
                reader={reader}
                people={people}
                agentName={agentName}
                onSelect={choose}
              />
            )}
            {openRequest && !(chosen && chosenAlone) ? (
              <RequestCard
                key={openRequest.id}
                organizationId={organizationId}
                viewerId={viewerId}
                request={openRequest}
                lease={leases.get(openRequest.id)}
                autoFocus={chosen === openRequest.id}
                shortcuts
              />
            ) : (
              <RequestCardById
                key={openId}
                organizationId={organizationId}
                viewerId={viewerId}
                requestId={openId}
                lease={leases.get(openId)}
                autoFocus
                shortcuts
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
            <p>
              Nothing is waiting for you.{" "}
              {agentName
                ? `${agentName} will flag the next one.`
                : "The next one opens here."}
            </p>
            {stats && (inboxAll?.length ?? 0) < INBOX_LIMIT && (
              <dl className="requests-clear-stats" aria-label="Today">
                <div>
                  <dt>asks today</dt>
                  <dd>{stats.asks}</dd>
                </div>
                <div>
                  <dt>by {agentName ?? "your agent"}</dt>
                  <dd>{stats.byAgent}</dd>
                </div>
                <div>
                  <dt>by you</dt>
                  <dd>{stats.byYou}</dd>
                </div>
                {stats.averageWaitMs !== undefined && (
                  <div>
                    <dt>average wait</dt>
                    <dd>{waitWords(stats.averageWaitMs)}</dd>
                  </div>
                )}
              </dl>
            )}
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
  onOpenConversation,
}: {
  organizationId?: string
  viewerId?: string
  focusRequestId?: string | null
  onFocusDone?: () => void
  onOpenConversation?: (threadId: string) => void
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
      onOpenConversation={onOpenConversation}
    />
  )
}
