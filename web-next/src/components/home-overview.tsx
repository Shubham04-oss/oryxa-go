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
import { AgentComposer } from "./agent-composer"
import { RequestCard } from "./request-card"
import { activitySentence } from "./requests-activity-format"
import {
  activityQueryOptions,
  refreshRequests,
  usePeopleNames,
} from "./requests-view-queries"
import { clockTime } from "./time-format"
import { ApiError } from "@/lib/api/client"
import {
  AGENT_REQUESTS_POLL_MS,
  agentKeys,
  agentRequestsQueryOptions,
} from "@/lib/agents/queries"
import { isUnavailableHere } from "@/lib/agents/policy"
import { revokeAgentLease } from "@/lib/agents/agent-requests"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import { getAgentGrant } from "@/lib/auth/settings"
import { slackKeys } from "@/lib/apps/slack"
import { corporaQueryOptions } from "@/lib/rag/client"
import {
  retryUnlessRefused,
  threadApprovalQueryOptions,
  threadListQueryOptions,
} from "@/lib/threads/client"
import { pendingConfirmationIds } from "@/lib/threads/timeline"
import { approvalCandidates, chatHistory } from "@/lib/threads/chat-choice"
import type { PrimaryChatActivity } from "@/lib/threads/chat-choice"
import { getMemberSlackStatus, listActiveAgentLeases } from "@/lib/home/client"
import { authorizedData, homeRead } from "@/lib/home/queries"
import "./home-overview.css"

const requestLimit = 100

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

function ReadNotice({ name, query }: { name: string; query: ReadState }) {
  if (query.isError) {
    const absent =
      isUnavailableHere(query.error) ||
      (query.error instanceof ApiError && query.error.status === 404)
    return (
      <p className="home-quiet" role="status">
        {absent ? `${name} is not available here.` : `Couldn’t load ${name}.`}
        {!absent && (
          <button type="button" onClick={() => void query.refetch()}>
            Try again
          </button>
        )}
      </p>
    )
  }
  if (query.isPending)
    return (
      <p className="home-quiet" role="status">
        Loading {name}…
      </p>
    )
  return null
}

function relativeTime(timestamp: string) {
  const time = Date.parse(timestamp)
  if (!Number.isFinite(time)) return "Recent conversation"
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
  }).format(time)
}

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
  onOpenKnowledge,
  onOpenSlack,
  onOpenAccount,
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
  const sent = useQuery({
    ...homeRead(agentRequestsQueryOptions(org, "sent", requestLimit), viewer),
    ...poll,
  })
  const activity = useQuery({
    ...homeRead(activityQueryOptions(org, requestLimit), viewer),
    ...poll,
  })
  const threads = useQuery({
    ...homeRead(threadListQueryOptions(org, viewer), viewer),
    ...poll,
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
  const sentRows = (authorizedData(sent) ?? []).filter(
    (request) => request.requester.user_id === viewer
  )
  const waitingRequests = inboxRows.filter((request) =>
    waitsOnMember(request, viewer, now)
  )
  const sharedAnswers = inboxRows.filter(
    (request) =>
      request.target.user_id === viewer &&
      request.status === "completed" &&
      Boolean(request.result)
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
        detail: relativeTime(thread.updated_at),
        running: false,
      })),
  ].slice(0, 3)
  const people = usePeopleNames(org, viewer, enabled)
  const sharedIds = new Set(
    sharedAnswers.slice(0, 3).map((request) => request.id)
  )
  const sharedActivity = (authorizedData(activity) ?? [])
    .filter(
      (entry) =>
        entry.role === "owner" &&
        entry.kind === "answered" &&
        entry.status === "completed" &&
        entry.request_id &&
        !sharedIds.has(entry.request_id)
    )
    .filter(
      (entry, index, rows) =>
        rows.findIndex((row) => row.request_id === entry.request_id) === index
    )
    .slice(0, 3)
  const slackStatus = authorizedData(slack)
  const corpusRows = authorizedData(corpora)
  const agentGrant = authorizedData(grant)
  const setup = [
    ...(slackStatus && slackStatus.status !== "active"
      ? [
          {
            id: "slack",
            title: "Connect Slack",
            detail: slackStatus.can_manage
              ? "Bring requests from your workspace into Oryxa."
              : "Ask a workspace admin to connect Slack.",
            open: slackStatus.can_manage ? onOpenSlack : undefined,
            action: "Connect",
          },
        ]
      : []),
    ...(corpusRows && !corpusRows.some((base) => base.document_count > 0)
      ? [
          {
            id: "knowledge",
            title: "Add knowledge",
            detail: "Give your agent something to look up.",
            open: onOpenKnowledge,
            action: "Add",
          },
        ]
      : []),
    ...(agentGrant && !agentGrant.granted
      ? [
          {
            id: "grant",
            title: "Enable background access",
            detail: "Let your agent handle approved work while you’re away.",
            open: onOpenAccount,
            action: "Open Account",
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
  const settled = [
    inbox,
    sent,
    activity,
    threads,
    leases,
    corpora,
    slack,
    grant,
  ].every((query) => query.isSuccess && !query.isFetching)
  const canSayQuiet =
    settled &&
    inboxRows.length < requestLimit &&
    completeConfirmationCoverage &&
    !needsMember
  const hasDeskWork =
    needsMember ||
    activeLeases.length > 0 ||
    sentRows.length > 0 ||
    sharedAnswers.length > 0 ||
    sharedActivity.length > 0
  const requestCard = (request: AgentRequest) => (
    <RequestCard
      key={request.id}
      organizationId={org}
      viewerId={viewer}
      request={request}
    />
  )

  return (
    <div className="home-overview">
      <section className="home-card home-agent" aria-label="Your agent">
        <header>
          <h2>Your agent</h2>
          <div>
            <button
              type="button"
              disabled={!onNewChat}
              onClick={() => onNewChat?.()}
            >
              New chat
            </button>
            <button
              type="button"
              onClick={onOpenAgent}
              aria-label="All conversations"
            >
              All
            </button>
          </div>
        </header>
        <AgentComposer
          label="Start a new conversation"
          placeholder="Ask your agent…"
          value={draft}
          onChange={setDraft}
          sendDisabled={!enabled || !onNewChat}
          status={
            !enabled
              ? "Sign in to start a conversation."
              : !onNewChat
                ? "Conversation shortcuts are unavailable."
                : undefined
          }
          onSubmit={(message) => {
            onNewChat?.(message)
            setDraft("")
          }}
        />
        {enabled && <ReadNotice name="conversations" query={threads} />}
        {agentWork.length > 0 && (
          <ul className="home-recents" aria-label="Recent conversations">
            {agentWork.map((work) => (
              <li key={work.threadId ?? "starting"} className="home-row">
                {work.threadId === null ? (
                  <div role="status">
                    <span>{work.title}</span>
                    <small className="home-work-status">
                      <ThinkingOrb
                        size={20}
                        theme="dark"
                        state="working"
                        aria-hidden="true"
                      />
                      {work.detail}
                    </small>
                  </div>
                ) : (
                  <button
                    type="button"
                    disabled={!onOpenConversation}
                    onClick={() => {
                      if (work.threadId) onOpenConversation?.(work.threadId)
                    }}
                  >
                    <span>{work.title}</span>
                    <small
                      className={work.running ? "home-work-status" : undefined}
                    >
                      {work.running && (
                        <ThinkingOrb
                          size={20}
                          theme="dark"
                          state="working"
                          aria-hidden="true"
                        />
                      )}
                      {work.detail}
                    </small>
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
      {enabled && (
        <>
          <ReadNotice name="requests" query={inbox} />
          {needsMember && (
            <section className="home-card home-needs" aria-label="Needs you">
              <header>
                <h2>Needs you</h2>
                {waitingRequests.length > 0 && (
                  <button type="button" onClick={() => onOpenRequests()}>
                    All requests
                  </button>
                )}
              </header>
              {waitingRequests.slice(0, 3).map(requestCard)}
              {waitingChats.length > 0 && (
                <ul aria-label="Tool confirmations">
                  {waitingChats.map((chat) => (
                    <li key={chat.id} className="home-row">
                      <button
                        type="button"
                        disabled={!onOpenConversation}
                        onClick={() => onOpenConversation?.(chat.id)}
                      >
                        <span>{chat.title}</span>
                        <small>
                          {chat.pending > 0
                            ? `${chat.pending} tool confirmation${chat.pending === 1 ? "" : "s"}`
                            : "Needs confirmation"}{" "}
                          · Open chat to decide
                        </small>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
              {waitingWithoutId && (
                <p className="home-quiet" role="status">
                  Your current conversation needs confirmation. It will open
                  when it is ready.
                </p>
              )}
              {waitingRequests.length > 3 && (
                <p className="home-quiet">
                  {waitingRequests.length - 3} more request
                  {waitingRequests.length === 4 ? "" : "s"} need
                  {waitingRequests.length === 4 ? "s" : ""} you. Open All
                  requests to decide.
                </p>
              )}
            </section>
          )}
          {checkedIds.map((id, index) => (
            <ReadNotice
              key={id}
              name="tool confirmations"
              query={confirmations[index]}
            />
          ))}
          {threads.isSuccess &&
            (outsideConfirmationCoverage || !freshConfirmations) && (
              <p className="home-quiet">
                Tool confirmations are limited to your{" "}
                {checkedChats.length === 5 ? "five" : checkedChats.length}{" "}
                newest chat{checkedChats.length === 1 ? "" : "s"}
                {openThreadId ? " and the open chat" : ""}.{" "}
                {outsideConfirmationCoverage
                  ? "Older chats may still need you."
                  : "The check is not complete."}
                <button type="button" onClick={onOpenAgent}>
                  All conversations
                </button>
              </p>
            )}
          {inboxRows.length >= requestLimit && (
            <p className="home-quiet">
              Only the latest {requestLimit} requests were checked.
            </p>
          )}
          <ReadNotice name="open access" query={leases} />
          {(activeLeases.length > 0 ||
            (leases.isSuccess && leases.hasNextPage)) && (
            <section className="home-card" aria-label="Open access">
              <header>
                <h2>Open access</h2>
                {(activeLeases.length > 3 || leases.hasNextPage) && (
                  <button
                    type="button"
                    onClick={() => setShowAccess(!showAccess)}
                  >
                    {showAccess ? "Show less" : "All access"}
                  </button>
                )}
              </header>
              <ul>
                {(showAccess ? activeLeases : activeLeases.slice(0, 3)).map(
                  (lease) => {
                    const owner =
                      lease.role === "owner" && lease.owner_user_id === viewer
                    const name =
                      people.nameOf(
                        owner ? lease.asker_user_id : lease.owner_user_id
                      ) || "A teammate"
                    return (
                      <li key={lease.id} className="home-row home-access-row">
                        <div>
                          <span>
                            {owner
                              ? `${name} can ask your agent`
                              : `You can ask ${name}’s agent`}
                          </span>
                          <small>
                            In one Slack thread · Until{" "}
                            {clockTime(lease.expires_at)}
                          </small>
                        </div>
                        {owner && lease.can_revoke && (
                          <button
                            className="workspace-agent-pill"
                            type="button"
                            disabled={endAccess.isPending}
                            onClick={() => endAccess.mutate(lease.id)}
                          >
                            End access
                          </button>
                        )}
                      </li>
                    )
                  }
                )}
              </ul>
              {showAccess && leases.hasNextPage && (
                <button
                  className="workspace-agent-text-button"
                  type="button"
                  disabled={leases.isFetchingNextPage}
                  onClick={() => void leases.fetchNextPage()}
                >
                  {leases.isFetchingNextPage
                    ? "Loading access…"
                    : "Load more access"}
                </button>
              )}
            </section>
          )}
          {leaseProblem && (
            <p className="home-quiet" role="alert">
              {leaseProblem}
            </p>
          )}
          <ReadNotice name="your requests" query={sent} />
          <ReadNotice name="shared activity" query={activity} />
          {(sentRows.length > 0 ||
            sharedAnswers.length > 0 ||
            sharedActivity.length > 0) && (
            <section
              className="home-card home-shared"
              aria-label="Your requests and shared answers"
            >
              <header>
                <h2>Your requests &amp; shared answers</h2>
                <button type="button" onClick={() => onOpenRequests()}>
                  All requests
                </button>
              </header>
              {sentRows.length > 0 && (
                <div className="home-request-group">
                  <h3>You asked</h3>
                  {sentRows.slice(0, 3).map(requestCard)}
                </div>
              )}
              {(sharedAnswers.length > 0 || sharedActivity.length > 0) && (
                <div className="home-request-group">
                  <h3>Your agent shared</h3>
                  {sharedAnswers.slice(0, 3).map(requestCard)}
                  {sharedActivity.length > 0 && (
                    <ul>
                      {sharedActivity.map((entry) => (
                        <li className="home-row" key={entry.request_id}>
                          <button
                            type="button"
                            onClick={() => onOpenRequests(entry.request_id)}
                          >
                            <span>
                              {activitySentence(entry, {
                                viewerId: viewer,
                                nameOf: people.nameOf,
                                now,
                              })}
                            </span>
                            <small>
                              {relativeTime(entry.at)} · Open request
                            </small>
                          </button>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              )}
              {(sentRows.length >= requestLimit ||
                (authorizedData(activity)?.length ?? 0) >= requestLimit) && (
                <p className="home-quiet">
                  Showing the latest requests and Slack activity. All requests
                  has the full request view.
                </p>
              )}
            </section>
          )}
          {canSayQuiet && (
            <p className="home-quiet home-empty">
              Nothing needs you right now.
            </p>
          )}
          <ReadNotice name="Slack status" query={slack} />
          <ReadNotice name="knowledge bases" query={corpora} />
          <ReadNotice name="background access" query={grant} />
          {setup.length > 0 && (
            <section
              className="home-card"
              aria-label={
                hasDeskWork ? "Finish setup" : "Set up your workspace"
              }
            >
              <header>
                <h2>
                  {hasDeskWork ? "Finish setup" : "Set up your workspace"}
                </h2>
              </header>
              <ul className="home-setup">
                {setup.map((item) => (
                  <li key={item.id} className="home-row home-setup-row">
                    <div>
                      <span>{item.title}</span>
                      <small>{item.detail}</small>
                    </div>
                    {item.open && (
                      <button
                        className="workspace-agent-pill"
                        type="button"
                        onClick={item.open}
                      >
                        {item.action}
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            </section>
          )}
        </>
      )}
    </div>
  )
}
