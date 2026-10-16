import { useEffect, useState } from "react"
import {
  useInfiniteQuery,
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { useHydrated } from "@tanstack/react-router"
import { ThinkingOrb } from "thinking-orbs"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { firstName } from "./people-format"
import {
  lastMessageLine,
  personIdentity,
  unreadCount,
  unreadWords,
} from "./teamspace-format"
import { originLabel } from "./request-card-format"
import { refreshRequests, usePeopleNames } from "./requests-view-queries"
import { clockTime } from "./direct-message-format"
import {
  agentFilesOf,
  agoPhrase,
  briefsOf,
  greeting,
  handledToday,
  helloLine as helloWords,
  pickupItems,
} from "./today-format"
import { HomeAsk } from "./home-ask"
import type { HomeSuggestion } from "./home-ask"
import { HomeLivePill } from "./home-live-pill"
import { HomePickup } from "./home-pickup"
import {
  HomeGettingStarted,
  startSteps,
  useCrewStep,
  useTeamspaceStep,
} from "./home-first-run"
import { ToolkitLogo } from "./workspace-tool-logo"
import { HomeGlance } from "./home-glance"
import type { DmPeople } from "./direct-message-people"
import { toolkitIcon } from "./workspace-tools-data"
import { IdentityAvatar, TeamspaceAvatar } from "./workspace-avatar"
import { ApiError } from "@/lib/api/client"
import {
  AGENT_REQUESTS_POLL_MS,
  agentKeys,
  agentRequestsQueryOptions,
  agentWorkflowsQueryOptions,
} from "@/lib/agents/queries"
import { isUnavailableHere } from "@/lib/agents/policy"
import { needsOwnerReply, revokeAgentLease } from "@/lib/agents/agent-requests"
import { connectionsQueryOptions, toolkitName } from "@/lib/apps/client"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import { getAgentGrant } from "@/lib/auth/settings"
import { slackKeys } from "@/lib/apps/slack"
import { corporaQueryOptions } from "@/lib/rag/client"
import {
  retryUnlessRefused,
  threadApprovalQueryOptions,
  threadDetailQueryOptions,
  threadListQueryOptions,
} from "@/lib/threads/client"
import { pendingConfirmationIds } from "@/lib/threads/timeline"
import { approvalCandidates, chatHistory } from "@/lib/threads/chat-choice"
import type { PrimaryChatActivity } from "@/lib/threads/chat-choice"
import { getMemberSlackStatus, listActiveAgentLeases } from "@/lib/home/client"
import { authorizedData, homeRead } from "@/lib/home/queries"
import { teamspacesQueryOptions } from "@/lib/teamspaces/queries"
import "./home-overview.css"

const requestLimit = 100

/** An app as its brand writes it ("GitHub"), else as the catalog does. */
function appName(toolkit: string): string {
  return toolkitIcon(toolkit)?.title || toolkitName(toolkit)
}

/** Only owner decisions and held-answer reviews actually wait on this member. */
function waitsOnMember(request: AgentRequest, viewerId: string, now: number) {
  return (
    request.target.user_id === viewerId &&
    Date.parse(request.expires_at) > now &&
    (request.status === "review" ||
      (request.status === "pending" && request.approval !== "agent"))
  )
}

type ReadState = {
  isPending: boolean
  isFetching: boolean
  isError: boolean
  error: unknown
  refetch: () => unknown
}

/**
 * One read's loading or error line, in its own words: each area names what
 * it lists ("Requests can’t be loaded right now."), never a resource name
 * dropped into a template. A read this server doesn't offer (or a 404) has
 * nothing to retry, so only a failure gets Try again.
 */
function ReadNotice({
  failed,
  loading,
  query,
}: {
  /** A whole sentence: "Recent chats can’t be loaded right now." */
  failed: string
  /** A whole line: "Loading recent chats…" */
  loading: string
  query: ReadState
}) {
  if (query.isError) {
    const absent =
      isUnavailableHere(query.error) ||
      (query.error instanceof ApiError && query.error.status === 404)
    return (
      <p className="home-quiet" role="status">
        {failed}
        {!absent && (
          <Button variant="link" size="sm" onClick={() => void query.refetch()}>
            Try again
          </Button>
        )}
      </p>
    )
  }
  if (query.isPending)
    return (
      <p className="home-quiet" role="status">
        {loading}
      </p>
    )
  return null
}

const notices = {
  requests: {
    failed: "Requests can’t be loaded right now.",
    loading: "Loading requests…",
  },
  chats: {
    failed: "Recent chats can’t be loaded right now.",
    loading: "Loading recent chats…",
  },
  updates: {
    failed: "Updates from your apps can’t be loaded right now.",
    loading: "Loading updates from your apps…",
  },
  access: {
    failed: "Open access windows can’t be loaded right now.",
    loading: "Loading open access windows…",
  },
  confirmations: {
    failed: "Tool confirmations can’t be loaded right now.",
    loading: "Loading tool confirmations…",
  },
  collections: {
    failed: "Library collections can’t be loaded right now.",
    loading: "Loading Library collections…",
  },
  workflows: {
    failed: "Recurring work can’t be loaded right now.",
    loading: "Loading recurring work…",
  },
} as const

export { greeting }

function conversationTitle(title?: string) {
  return title?.trim() ?? ""
}

type HomeProps = {
  organizationId?: string
  viewerId?: string
  primaryChatActivity?: PrimaryChatActivity | null
  onNewChat?: (message?: string) => void
  onOpenRequests: (requestId?: string) => void
  onOpenAgent: () => void
  onOpenConversation?: (threadId: string) => void
  onOpenTools: () => void
  onOpenKnowledge: () => void
  onOpenSlack?: () => void
  onOpenAccount?: () => void
  /** Opens a teamspace, where its live parley is joined. */
  onOpenTeamspace?: (teamspaceId: string) => void
  /** Opens Teamspaces, where a first one is made. */
  onOpenTeamspaces?: () => void
  /** Opens Settings → People, where the crew is invited. */
  onOpenPeople?: () => void
  /**
   * The first run, right after setup (OnbFirstRunV3): Getting started takes
   * While you were away's place until the member hides it.
   */
  firstRun?: boolean
  onFirstRunDone?: () => void
  /**
   * "glance" is the agent panel's At a glance tab: the same widgets in one
   * compact column, without the page's composer (the agent's chat is a tab
   * away).
   */
  variant?: "page" | "glance"
  /** The member's name, for the page's greeting. */
  viewerName?: string
  /** At a glance: the member's teammates, whom a tap messages. */
  teammates?: DmPeople
  onMessage?: (peerId: string) => void
  /** At a glance: Recurring work's All, to the agent's workflows. */
  onOpenWorkflows?: () => void
  /** The member's primary agent's name, for "Files Krude made". */
  agentName?: string
}

/** A different signed-in member must never inherit drafts or local decisions. */
export function HomeOverview(props: HomeProps) {
  return (
    <HomeDesk
      key={`${props.organizationId ?? ""}:${props.viewerId ?? ""}`}
      {...props}
    />
  )
}

function HomeDesk({
  organizationId,
  viewerId,
  primaryChatActivity,
  onNewChat,
  onOpenRequests,
  onOpenAgent,
  onOpenConversation,
  onOpenTools,
  onOpenKnowledge,
  onOpenSlack,
  onOpenAccount,
  onOpenTeamspace,
  onOpenTeamspaces,
  onOpenPeople,
  firstRun = false,
  onFirstRunDone,
  variant = "page",
  viewerName,
  teammates,
  onMessage,
  onOpenWorkflows,
  agentName,
}: HomeProps) {
  const hydrated = useHydrated()
  const enabled = hydrated && Boolean(organizationId && viewerId)
  const org = organizationId ?? ""
  const viewer = viewerId ?? ""
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState("")
  const [showAccess, setShowAccess] = useState(false)
  const [leaseProblem, setLeaseProblem] = useState("")
  const [now, setNow] = useState(() => Date.now())
  const poll = { enabled, refetchInterval: AGENT_REQUESTS_POLL_MS }
  const inbox = useQuery({
    ...homeRead(agentRequestsQueryOptions(org, "inbox", requestLimit), viewer),
    ...poll,
  })
  const threads = useQuery({
    ...homeRead(threadListQueryOptions(org, viewer), viewer),
    ...poll,
  })
  // The thread Krude writes in when the member's apps report something: the
  // briefs. The list already holds it (Go answers every origin).
  const updatesThreadId = (authorizedData(threads) ?? []).find(
    (thread) => thread.origin === "wakeup"
  )?.id
  const updates = useQuery({
    ...homeRead(threadDetailQueryOptions(org, updatesThreadId ?? ""), viewer),
    enabled: enabled && Boolean(updatesThreadId),
    refetchInterval: 30_000,
  })
  const connections = useQuery({
    ...homeRead(connectionsQueryOptions(org), viewer),
    enabled,
    refetchInterval: 60_000,
  })
  const workflows = useQuery({
    ...homeRead(agentWorkflowsQueryOptions(org), viewer),
    enabled,
    refetchInterval: 60_000,
  })
  const corpora = useQuery({
    ...homeRead(corporaQueryOptions(org), viewer),
    enabled,
    refetchInterval: 30_000,
  })
  const slack = useQuery({
    ...homeRead(
      {
        queryKey: [...slackKeys.all(org), "status"] as const,
        queryFn: ({ signal }) => getMemberSlackStatus(org, { signal }),
      },
      viewer
    ),
    enabled,
    refetchInterval: 30_000,
  })
  const grant = useQuery({
    ...homeRead(
      {
        queryKey: ["auth", "agent-grant", org] as const,
        queryFn: getAgentGrant,
      },
      viewer
    ),
    enabled,
    refetchInterval: 30_000,
  })
  const leaseKey = [...agentKeys.requests(org), "active-leases", "home", viewer]
  const leases = useInfiniteQuery({
    queryKey: leaseKey,
    queryFn: ({ pageParam, signal }) =>
      listActiveAgentLeases(org, { cursor: pageParam, signal }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => (page.has_more ? page.next_cursor : undefined),
    ...poll,
    retry: retryUnlessRefused,
    staleTime: 0,
    refetchOnMount: "always",
    refetchOnWindowFocus: "always",
    refetchIntervalInBackground: false,
  })
  // Use the server's observation plus elapsed time, rather than trusting a
  // member's clock, to remove an access window that expires on this page.
  const leasePages = authorizedData(leases)?.pages ?? []
  const latestObservation = Math.max(
    0,
    ...leasePages.map((page) => Date.parse(page.observed_at) || 0)
  )
  const leaseNow = latestObservation + Math.max(0, now - leases.dataUpdatedAt)
  const activeLeases = leasePages
    .flatMap((page) => page.leases)
    .filter(
      (lease) =>
        !lease.revoked_at &&
        Date.parse(lease.expires_at) > leaseNow &&
        (lease.owner_user_id === viewer || lease.asker_user_id === viewer)
    )
  const clockInterval = activeLeases.length > 0 ? 1000 : 30_000
  useEffect(() => {
    if (!enabled) return
    const timer = window.setInterval(() => setNow(Date.now()), clockInterval)
    return () => window.clearInterval(timer)
  }, [enabled, clockInterval])
  const endAccess = useMutation({
    mutationFn: (leaseId: string) => revokeAgentLease(org, leaseId),
    onSuccess: async (winner) => {
      setLeaseProblem("")
      queryClient.setQueryData<typeof leases.data>(
        leaseKey,
        (data) =>
          data
            ? {
                ...data,
                pages: data.pages.map((page) => ({
                  ...page,
                  leases: page.leases.map((lease) =>
                    lease.id === winner.id ? { ...lease, ...winner } : lease
                  ),
                })),
              }
            : data,
        { updatedAt: leases.dataUpdatedAt }
      )
      await refreshRequests(queryClient, org)
    },
    onError: async () => {
      setLeaseProblem("Couldn’t end that access. Try again.")
      await refreshRequests(queryClient, org)
    },
  })
  const inboxRows = authorizedData(inbox) ?? []
  const waitingRequests = inboxRows.filter((request) =>
    waitsOnMember(request, viewer, now)
  )
  const conversations = chatHistory(authorizedData(threads) ?? [])
  // ADK stores confirmations in events, not reliable session metadata. Never
  // walk all histories: check the newest five primary chats plus the open one.
  const checkedChats = approvalCandidates(conversations)
  const openThreadId = primaryChatActivity?.threadId
  const checkedIds = checkedChats.map((thread) => thread.id)
  if (openThreadId && !checkedIds.includes(openThreadId))
    checkedIds.push(openThreadId)
  const confirmations = useQueries({
    queries: checkedIds.map((threadId) => ({
      ...threadApprovalQueryOptions(org, threadId, viewer),
      enabled,
    })),
  })
  const waitingChats = checkedIds.flatMap((threadId, index) => {
    const detail = authorizedData(confirmations[index])
    const pending = detail ? pendingConfirmationIds(detail).length : 0
    const liveWaiting =
      primaryChatActivity?.state === "waiting" && openThreadId === threadId
    if (!pending && !liveWaiting) return []
    return [
      {
        id: threadId,
        title:
          conversationTitle(
            detail?.title ||
              conversations.find((chat) => chat.id === threadId)?.title
          ) || "Current conversation",
        pending,
      },
    ]
  })
  const waitingWithoutId =
    primaryChatActivity?.state === "waiting" && !openThreadId
  const running =
    primaryChatActivity?.state === "running" ? primaryChatActivity : null
  const activityTitle =
    conversationTitle(
      conversations.find(
        (thread) => thread.id === primaryChatActivity?.threadId
      )?.title
    ) || "Current conversation"
  const agentWork = [
    ...(running
      ? [
          {
            threadId: running.threadId,
            title: running.threadId ? activityTitle : "Starting conversation",
            detail: "Working",
            running: true,
          },
        ]
      : []),
    ...conversations
      .filter(
        (thread) =>
          conversationTitle(thread.title).length > 0 &&
          thread.id !== running?.threadId
      )
      .slice(0, running ? 2 : 3)
      .map((thread) => ({
        threadId: thread.id,
        title: conversationTitle(thread.title),
        detail: agoPhrase(thread.updated_at, now),
        running: false,
      })),
  ].slice(0, 3)
  const people = usePeopleNames(org, viewer, enabled)
  const starting = enabled && variant === "page" && firstRun
  const crewStep = useCrewStep(org, viewer, starting)
  const teamspaceStep = useTeamspaceStep(org, starting)
  const nameOf = (userId: string, fallback?: string) =>
    people.nameOf(userId) || fallback?.trim() || "A teammate"

  // Teamspaces with room messages the member hasn't read, from the list's
  // own unread counts (#389): none at all from a server without them.
  const spaces = useQuery({ ...teamspacesQueryOptions(org), enabled })
  const unreadSpaces = (spaces.data ?? [])
    .filter((space) => unreadCount(space) > 0 && space.last_message)
    .sort((left, right) =>
      (right.last_message?.created_at ?? "").localeCompare(
        left.last_message?.created_at ?? ""
      )
    )
    .slice(0, 3)

  const updatesDetail = authorizedData(updates)
  const briefs = updatesDetail ? briefsOf(updatesDetail) : []
  const agentFiles = agentFilesOf(
    confirmations.map((query) => authorizedData(query))
  )
  const tools = [
    ...new Set(
      (authorizedData(connections) ?? [])
        .filter((connection) => connection.status === "active")
        .map((connection) => connection.toolkit)
    ),
  ]
  const noApps = connections.isSuccess && tools.length === 0
  const ownWorkflows = (authorizedData(workflows) ?? []).filter(
    (workflow) => workflow.owner_user_id === viewer
  )
  const corpusRows = authorizedData(corpora)
  const knowledgeRows = [...(corpusRows ?? [])]
    .sort((left, right) => right.updated_at.localeCompare(left.updated_at))
    .slice(0, 3)

  const slackStatus = authorizedData(slack)
  const agentGrant = authorizedData(grant)
  const setup = [
    ...(slackStatus && slackStatus.status !== "active" && slackStatus.can_manage
      ? [{ id: "slack", title: "Connect Slack", open: onOpenSlack }]
      : []),
    ...(corpusRows && !corpusRows.some((base) => base.document_count > 0)
      ? [{ id: "knowledge", title: "Add knowledge", open: onOpenKnowledge }]
      : []),
    ...(agentGrant && !agentGrant.granted
      ? [
          {
            id: "grant",
            title: "Turn on background access",
            open: onOpenAccount,
          },
        ]
      : []),
  ]
  const needsMember =
    waitingRequests.length > 0 || waitingChats.length > 0 || waitingWithoutId
  const outsideConfirmationCoverage = conversations.some(
    (thread) => !checkedIds.includes(thread.id)
  )
  const freshConfirmations = confirmations.every(
    (query) => query.isSuccess && !query.isFetching
  )
  const completeConfirmationCoverage =
    threads.isSuccess && !outsideConfirmationCoverage && freshConfirmations
  const settled = [inbox, threads, leases].every(
    (query) => query.isSuccess && !query.isFetching
  )
  const canSayQuiet =
    settled &&
    inboxRows.length < requestLimit &&
    completeConfirmationCoverage &&
    !needsMember
  const needsCount = waitingRequests.length + waitingChats.length
  const handled = handledToday(inboxRows, viewer, now)
  const helloLine = helloWords(needsCount, handled)
  const page = variant === "page"

  const appsSection = (
    <section className="home-column" aria-labelledby="home-briefs">
      <header className="home-column-head">
        <h2 id="home-briefs">
          {!page
            ? "From your apps"
            : firstRun
              ? "What Krude found"
              : "While you were away"}
        </h2>
        {updatesThreadId && (
          <Button
            variant="ghost"
            size="sm"
            disabled={!onOpenConversation}
            onClick={() => onOpenConversation?.(updatesThreadId)}
          >
            All updates
          </Button>
        )}
      </header>
      {page && !firstRun && unreadSpaces.length > 0 && (
        <ul className="home-list" aria-label="Unread in your teamspaces">
          {unreadSpaces.map((space) => {
            const last = space.last_message
            if (!last) return null
            const unread = unreadCount(space)
            return (
              <li key={space.id}>
                <button
                  type="button"
                  className="home-item"
                  disabled={!onOpenTeamspace}
                  aria-label={`Open ${space.name}, ${unreadWords(unread)}`}
                  onClick={() => onOpenTeamspace?.(space.id)}
                >
                  <span className="home-item-mark">
                    <TeamspaceAvatar name={space.name} />
                  </span>
                  <span className="home-item-copy">
                    <span>
                      <strong>{space.name}</strong>{" "}
                      {lastMessageLine(last, viewer)}
                    </span>
                    <small>
                      {[unreadWords(unread), agoPhrase(last.created_at, now)]
                        .filter(Boolean)
                        .join(" · ")}
                    </small>
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
      )}
      {/* The briefs live in the wake-up thread, found in the chat list: a
          list that can't be read leaves them unknown, and says so. With no
          app connected and no wake-up thread yet there is nothing to report
          either way. */}
      {!(noApps && !updatesThreadId) && (
        <ReadNotice
          {...notices.updates}
          query={updatesThreadId ? updates : threads}
        />
      )}
      {briefs.length > 0 ? (
        <ul className="home-list">
          {briefs.slice(0, page ? 3 : undefined).map((brief) => (
            <li key={brief.key}>
              <button
                type="button"
                className="home-item"
                disabled={!onOpenConversation}
                onClick={() =>
                  updatesThreadId && onOpenConversation?.(updatesThreadId)
                }
              >
                <span className="home-item-mark">
                  {brief.toolkits[0] ? (
                    <ToolkitLogo
                      toolkit={{
                        toolkit: brief.toolkits[0],
                        name: appName(brief.toolkits[0]),
                      }}
                    />
                  ) : null}
                </span>
                <span className="home-item-copy">
                  <span>{brief.text}</span>
                  <small>
                    {[
                      brief.toolkits.map(appName).join(", "),
                      agoPhrase(brief.at, now),
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </small>
                </span>
              </button>
            </li>
          ))}
        </ul>
      ) : (
        ((noApps && !updatesThreadId) ||
          (threads.isSuccess && (!updatesThreadId || updates.isSuccess))) && (
          <p className="home-quiet">
            {noApps
              ? "Connect an app to see what happened while you were away."
              : "Nothing new from your apps."}
            {noApps && (
              <Button variant="link" size="sm" onClick={onOpenTools}>
                Connect your tools
              </Button>
            )}
          </p>
        )
      )}
    </section>
  )

  const needsSection = (
    <section className="home-column" aria-labelledby="home-needs">
      <header className="home-column-head">
        <h2 id="home-needs">Needs you</h2>
        {page && needsCount > 0 && (
          <Badge
            className="home-count"
            variant="secondary"
            aria-label={`${needsCount} waiting`}
          >
            {needsCount}
          </Badge>
        )}
        {waitingRequests.length > 0 && (
          <Button variant="ghost" size="sm" onClick={() => onOpenRequests()}>
            All requests
          </Button>
        )}
      </header>
      <ReadNotice {...notices.requests} query={inbox} />
      {(needsMember || activeLeases.length > 0) && (
        <ul className="home-list" aria-label="Needs you">
          {waitingRequests.slice(0, 4).map((request) => {
            const asker = nameOf(
              request.requester.user_id,
              request.requester.user_display_name ||
                request.requester.user_email
            )
            const review = request.status === "review"
            const reply = !review && needsOwnerReply(request)
            return (
              <li key={request.id}>
                <div className="home-item">
                  <span className="home-item-mark">
                    <IdentityAvatar
                      identity={personIdentity(
                        request.requester.user_id,
                        asker
                      )}
                    />
                  </span>
                  <span className="home-item-copy">
                    <span>
                      <strong>{firstName(asker)}</strong>{" "}
                      {request.task.trim() || request.purpose.trim()}
                    </span>
                    <small>
                      {review
                        ? "Your agent’s answer is held for you"
                        : reply
                          ? "Needs your own reply"
                          : "Waiting for your OK"}{" "}
                      · {originLabel(request)} ·{" "}
                      {agoPhrase(request.created_at, now)}
                    </small>
                  </span>
                  <Button
                    className="home-item-action"
                    variant="outline"
                    size="sm"
                    aria-label={`${review ? "Review" : reply ? "Reply to" : "Decide on"} ${firstName(asker)}’s request`}
                    onClick={() => onOpenRequests(request.id)}
                  >
                    {review ? "Review" : reply ? "Reply" : "Decide"}
                  </Button>
                </div>
              </li>
            )
          })}
          {waitingChats.map((chat) => (
            <li key={chat.id}>
              <div className="home-item">
                <span className="home-item-mark">
                  <ThinkingOrb size={20} state="listening" aria-hidden="true" />
                </span>
                <span className="home-item-copy">
                  <span>
                    <strong>Your agent</strong> is waiting in {chat.title}
                  </span>
                  <small>
                    {chat.pending > 0
                      ? `${chat.pending} tool confirmation${chat.pending === 1 ? "" : "s"}`
                      : "Needs confirmation"}
                  </small>
                </span>
                <Button
                  className="home-item-action"
                  variant="outline"
                  size="sm"
                  disabled={!onOpenConversation}
                  onClick={() => onOpenConversation?.(chat.id)}
                >
                  Open
                </Button>
              </div>
            </li>
          ))}
          {waitingWithoutId && (
            <li>
              <p className="home-quiet" role="status">
                Your current conversation needs confirmation. It will open when
                it is ready.
              </p>
            </li>
          )}
          {activeLeases.slice(0, showAccess ? undefined : 3).map((lease) => {
            const owner =
              lease.role === "owner" && lease.owner_user_id === viewer
            const otherId = owner ? lease.asker_user_id : lease.owner_user_id
            const name = nameOf(otherId)
            return (
              <li key={lease.id}>
                <div className="home-item">
                  <span className="home-item-mark">
                    <IdentityAvatar identity={personIdentity(otherId, name)} />
                  </span>
                  <span className="home-item-copy">
                    <span>
                      {owner ? (
                        <>
                          <strong>{firstName(name)}</strong> can ask your agent
                        </>
                      ) : (
                        <>
                          You can ask <strong>{firstName(name)}</strong>’s agent
                        </>
                      )}
                    </span>
                    <small>
                      In one Slack thread · Until {clockTime(lease.expires_at)}
                    </small>
                  </span>
                  {owner && lease.can_revoke && (
                    <Button
                      className="home-item-action"
                      variant="outline"
                      size="sm"
                      disabled={endAccess.isPending}
                      onClick={() => endAccess.mutate(lease.id)}
                    >
                      End access
                    </Button>
                  )}
                </div>
              </li>
            )
          })}
        </ul>
      )}
      {(activeLeases.length > 3 || leases.hasNextPage) && (
        <Button
          variant="ghost"
          size="sm"
          className="home-more"
          disabled={leases.isFetchingNextPage}
          onClick={() => {
            if (showAccess && leases.hasNextPage) void leases.fetchNextPage()
            else setShowAccess(!showAccess)
          }}
        >
          {!showAccess
            ? "All access"
            : leases.hasNextPage
              ? leases.isFetchingNextPage
                ? "Loading access…"
                : "Load more access"
              : "Show less"}
        </Button>
      )}
      <ReadNotice {...notices.access} query={leases} />
      {leaseProblem && (
        <p className="home-quiet" role="alert">
          {leaseProblem}
        </p>
      )}
      {waitingRequests.length > 4 && (
        <p className="home-quiet">
          {waitingRequests.length - 4} more waiting in Requests.
        </p>
      )}
      {checkedIds.map((id, index) => (
        <ReadNotice
          key={id}
          {...notices.confirmations}
          query={confirmations[index]}
        />
      ))}
      {threads.isSuccess &&
        (outsideConfirmationCoverage || !freshConfirmations) && (
          <p className="home-quiet">
            Tool confirmations are limited to your{" "}
            {checkedChats.length === 5 ? "five" : checkedChats.length} newest
            chat{checkedChats.length === 1 ? "" : "s"}
            {openThreadId ? " and the open chat" : ""}.{" "}
            {outsideConfirmationCoverage
              ? "Older chats may still need you."
              : "The check is not complete."}
            <Button variant="link" size="sm" onClick={onOpenAgent}>
              All conversations
            </Button>
          </p>
        )}
      {inboxRows.length >= requestLimit && (
        <p className="home-quiet">
          Only the latest {requestLimit} requests were checked.
        </p>
      )}
      {canSayQuiet && activeLeases.length === 0 && (
        <p className="home-quiet home-empty">Nothing needs you right now.</p>
      )}
    </section>
  )

  if (page) {
    const chatTitle = (threadId: string) =>
      conversationTitle(
        conversations.find((chat) => chat.id === threadId)?.title
      )
    const pickup = pickupItems(
      {
        files: agentFiles.map((file) => ({
          ...file,
          chatTitle: chatTitle(file.threadId),
        })),
        chats: conversations,
        knowledge: corpusRows ?? [],
        runningThreadId: running?.threadId,
        starting: Boolean(running && !running.threadId),
      },
      now
    )
    const pickupReady =
      threads.isSuccess &&
      corpora.isSuccess &&
      confirmations.every((query) => query.isSuccess)
    // Suggestions come from what the member really has: never a made-up
    // teammate, file or channel.
    const firstTool = tools[0]
    const suggestions: HomeSuggestion[] = enabled
      ? [
          {
            key: "today",
            label: "What needs me today?",
            run: () => onNewChat?.("What needs me today?"),
          },
          ...(firstTool
            ? [
                {
                  key: `catch-up:${firstTool}`,
                  label: `Catch me up on ${appName(firstTool)}`,
                  run: () =>
                    onNewChat?.(`Catch me up on ${appName(firstTool)}`),
                },
              ]
            : []),
        ].filter(() => Boolean(onNewChat))
      : []
    const steps = startSteps(
      {
        granted: agentGrant?.granted,
        tools: connections.isSuccess ? tools.length > 0 : undefined,
        crew: crewStep,
        teamspace: teamspaceStep,
        asked: threads.isSuccess ? conversations.length > 0 : undefined,
      },
      {
        grant: onOpenAccount,
        tools: onOpenTools,
        crew: onOpenPeople,
        teamspace: onOpenTeamspaces,
      }
    )
    const firstLine =
      helloLine ||
      (briefs.length > 0
        ? `${briefs.length === 1 ? "One thing" : `${briefs.length} things`} stood out in your apps. Ask Krude about any of it, or anything else.`
        : "Ask Krude anything, or finish getting started below.")
    const showNeeds = needsMember || activeLeases.length > 0
    return (
      <div
        className="home-overview home-today home-centred"
        data-first-run={firstRun || undefined}
      >
        <div className="home-scene" aria-hidden="true" />
        {enabled && (
          <HomeLivePill
            organizationId={org}
            enabled={enabled}
            onOpenTeamspace={onOpenTeamspace}
          />
        )}
        {enabled && firstRun && (
          <p className="home-krude">
            <ThinkingOrb size={20} state="listening" aria-hidden="true" />
            <span>
              <b>Krude</b> is setting up with you · only you see this page
            </span>
          </p>
        )}
        <header className="home-hello">
          <h2>{greeting(now, viewerName)}</h2>
          {enabled &&
            (firstRun ? <p>{firstLine}</p> : helloLine && <p>{helloLine}</p>)}
        </header>
        <HomeAsk
          draft={draft}
          onDraftChange={setDraft}
          enabled={enabled}
          onNewChat={onNewChat}
          suggestions={suggestions}
        />
        {enabled && !firstRun && (
          <div className="home-pair">
            {needsSection}
            {appsSection}
          </div>
        )}
        {enabled && firstRun && (
          <div className="home-pair">
            {appsSection}
            <HomeGettingStarted steps={steps} onHide={onFirstRunDone} />
          </div>
        )}
        {enabled && firstRun && showNeeds && (
          <div className="home-pair home-pair-one">{needsSection}</div>
        )}
        {enabled && (!firstRun || pickup.length > 0) && (
          <HomePickup
            items={pickup}
            ready={pickupReady}
            onOpenConversation={onOpenConversation}
            onOpenKnowledge={onOpenKnowledge}
            onOpenAgent={onOpenAgent}
            onNewChat={onNewChat ? () => onNewChat() : undefined}
            notices={
              <>
                <ReadNotice {...notices.chats} query={threads} />
                <ReadNotice {...notices.collections} query={corpora} />
              </>
            }
          />
        )}
      </div>
    )
  }

  // At a glance (AgentPanelGlance board): the widgets as compact library
  // cards, then the Finish setup line.
  if (!enabled)
    return (
      <p className="workspace-agent-empty">
        Your apps, teammates and conversations will appear here.
      </p>
    )
  return (
    <div className="home-glance">
      <HomeGlance
        now={now}
        agentName={agentName}
        updatesThreadId={updatesThreadId}
        updates={updates}
        briefs={briefs}
        tools={tools}
        connections={connections}
        people={teammates}
        conversations={agentWork}
        threads={threads}
        knowledge={knowledgeRows}
        corpora={corpora}
        files={agentFiles}
        filesRead={confirmations.every((query) => query.isSuccess)}
        conversationTitle={(threadId) =>
          conversationTitle(
            conversations.find((chat) => chat.id === threadId)?.title
          )
        }
        workflows={ownWorkflows}
        workflowsRead={workflows}
        onOpenConversation={onOpenConversation}
        onNewChat={onNewChat ? () => onNewChat() : undefined}
        onOpenTools={onOpenTools}
        onOpenKnowledge={onOpenKnowledge}
        onOpenPeople={onOpenPeople}
        onMessage={onMessage}
        onOpenWorkflows={onOpenWorkflows}
      />

      {/* Slack and background access only feed the setup line: a read that
          failed or is not served here leaves its step out, quietly. */}
      {setup.length > 0 && (
        <p className="home-setup-line" aria-label="Finish setup">
          <span>Finish setup:</span>
          {setup.map((item, index) => (
            <span key={item.id}>
              {index > 0 && " · "}
              <Button
                variant="link"
                size="sm"
                disabled={!item.open}
                onClick={item.open}
              >
                {item.title}
              </Button>
            </span>
          ))}
        </p>
      )}
    </div>
  )
}
