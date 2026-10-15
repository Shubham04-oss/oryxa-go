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
import { firstName } from "./people-format"
import { personIdentity } from "./teamspace-format"
import { originLabel } from "./request-card-format"
import { refreshRequests, usePeopleNames } from "./requests-view-queries"
import { clockTime } from "./direct-message-format"
import {
  agentFilesOf,
  agoPhrase,
  briefsOf,
  scheduleWords,
} from "./today-format"
import { ToolkitLogo } from "./workspace-tool-logo"
import { toolkitIcon } from "./workspace-tools-data"
import { IdentityAvatar } from "./workspace-avatar"
import { HomeGlance } from "./home-glance"
import type { DmPeople } from "./direct-message-people"
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

/** "Good morning, Ada": by the member's own clock. */
export function greeting(now: number, name?: string): string {
  const hour = new Date(now).getHours()
  const part =
    hour < 5
      ? "evening"
      : hour < 12
        ? "morning"
        : hour < 18
          ? "afternoon"
          : "evening"
  // An email address is not a name to greet someone by.
  const first = name?.includes("@") ? undefined : name?.trim().split(/\s+/)[0]
  return first ? `Good ${part}, ${first}` : `Good ${part}`
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
  onOpenPeople?: () => void
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
  variant = "page",
  viewerName,
  teammates,
  onOpenPeople,
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
  const nameOf = (userId: string, fallback?: string) =>
    people.nameOf(userId) || fallback?.trim() || "A teammate"

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
  const ownWorkflows = (authorizedData(workflows) ?? []).filter(
    (workflow) => workflow.owner_user_id === viewer
  )
  const workflowRows = ownWorkflows.slice(0, 3)
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
  // Only when something waits: the tile below says so when nothing does.
  const helloLine =
    needsCount > 0
      ? `${needsCount} ${needsCount === 1 ? "thing needs" : "things need"} you.`
      : ""

  const appsSection = (
    <section className="home-column" aria-labelledby="home-briefs">
      <header className="home-column-head">
        <h2 id="home-briefs">From your apps</h2>
        {updatesThreadId && (
          <button
            type="button"
            disabled={!onOpenConversation}
            onClick={() => onOpenConversation?.(updatesThreadId)}
          >
            All updates
          </button>
        )}
      </header>
      {updatesThreadId && <ReadNotice name="updates" query={updates} />}
      {briefs.length > 0 ? (
        <ul className="home-list">
          {briefs.map((brief) => (
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
        (!updatesThreadId || updates.isSuccess) &&
        threads.isSuccess && (
          <p className="home-quiet">
            {connections.isSuccess && tools.length === 0
              ? "Connect a tool and its news lands here."
              : "No updates from your apps yet."}
          </p>
        )
      )}
    </section>
  )

  if (variant === "glance") {
    if (!enabled)
      return (
        <p className="workspace-agent-empty">
          Your apps, teammates and conversations will appear here.
        </p>
      )
    return (
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
    )
  }

  return (
    <div className="home-overview home-today">
      <div className="home-scene" aria-hidden="true" />
      <header className="home-hello">
        <h2>{greeting(now, viewerName)}</h2>
        {enabled && helloLine && <p>{helloLine}</p>}
      </header>
      {enabled && (
        <div className="home-top">
          <section className="home-column" aria-labelledby="home-teammates">
            <header className="home-column-head">
              <h2 id="home-teammates">Teammates</h2>
              {waitingRequests.length > 0 && (
                <button type="button" onClick={() => onOpenRequests()}>
                  All requests
                </button>
              )}
            </header>
            <ReadNotice name="requests" query={inbox} />
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
                        <button
                          type="button"
                          className="home-item-action"
                          aria-label={`${review ? "Review" : reply ? "Reply to" : "Decide on"} ${firstName(asker)}’s request`}
                          onClick={() => onOpenRequests(request.id)}
                        >
                          {review ? "Review" : reply ? "Reply" : "Decide"}
                        </button>
                      </div>
                    </li>
                  )
                })}
                {waitingChats.map((chat) => (
                  <li key={chat.id}>
                    <div className="home-item">
                      <span className="home-item-mark">
                        <ThinkingOrb
                          size={20}
                          state="listening"
                          aria-hidden="true"
                        />
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
                      <button
                        type="button"
                        className="home-item-action"
                        disabled={!onOpenConversation}
                        onClick={() => onOpenConversation?.(chat.id)}
                      >
                        Open
                      </button>
                    </div>
                  </li>
                ))}
                {waitingWithoutId && (
                  <li>
                    <p className="home-quiet" role="status">
                      Your current conversation needs confirmation. It will open
                      when it is ready.
                    </p>
                  </li>
                )}
                {activeLeases
                  .slice(0, showAccess ? undefined : 3)
                  .map((lease) => {
                    const owner =
                      lease.role === "owner" && lease.owner_user_id === viewer
                    const otherId = owner
                      ? lease.asker_user_id
                      : lease.owner_user_id
                    const name = nameOf(otherId)
                    return (
                      <li key={lease.id}>
                        <div className="home-item">
                          <span className="home-item-mark">
                            <IdentityAvatar
                              identity={personIdentity(otherId, name)}
                            />
                          </span>
                          <span className="home-item-copy">
                            <span>
                              {owner ? (
                                <>
                                  <strong>{firstName(name)}</strong> can ask
                                  your agent
                                </>
                              ) : (
                                <>
                                  You can ask <strong>{firstName(name)}</strong>
                                  ’s agent
                                </>
                              )}
                            </span>
                            <small>
                              In one Slack thread · Until{" "}
                              {clockTime(lease.expires_at)}
                            </small>
                          </span>
                          {owner && lease.can_revoke && (
                            <button
                              className="home-item-action"
                              type="button"
                              disabled={endAccess.isPending}
                              onClick={() => endAccess.mutate(lease.id)}
                            >
                              End access
                            </button>
                          )}
                        </div>
                      </li>
                    )
                  })}
              </ul>
            )}
            {(activeLeases.length > 3 || leases.hasNextPage) && (
              <button
                className="home-more"
                type="button"
                disabled={leases.isFetchingNextPage}
                onClick={() => {
                  if (showAccess && leases.hasNextPage)
                    void leases.fetchNextPage()
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
              </button>
            )}
            <ReadNotice name="open access" query={leases} />
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
            {canSayQuiet && activeLeases.length === 0 && (
              <p className="home-quiet home-empty">
                Nothing needs you right now.
              </p>
            )}
          </section>
        </div>
      )}

      <section className="home-compose" aria-label="Your agent">
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
        {enabled && (
          <div className="home-tools" aria-label="Your tools">
            {tools.map((toolkit) => (
              <button
                key={toolkit}
                type="button"
                className="home-tool"
                onClick={onOpenTools}
              >
                <ToolkitLogo toolkit={{ toolkit, name: appName(toolkit) }} />
                <span>{appName(toolkit)}</span>
              </button>
            ))}
            {connections.isSuccess && (
              <button
                type="button"
                className="home-tool home-tool-connect"
                onClick={onOpenTools}
              >
                {tools.length > 0 ? "+ Connect" : "Connect your tools"}
              </button>
            )}
            <ReadNotice name="your tools" query={connections} />
          </div>
        )}
      </section>

      {enabled && (
        <h2 className="home-row-title" id="home-row">
          Pick up where you left off
        </h2>
      )}
      {enabled && (
        <div
          className="home-stack"
          role={"region"}
          aria-labelledby={"home-row"}
        >
          {appsSection}
          <section className="home-column" aria-labelledby="home-conversations">
            <header className="home-column-head">
              <h2 id="home-conversations">Conversations</h2>
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
            <ReadNotice name="conversations" query={threads} />
            {agentWork.length > 0 ? (
              <ul className="home-list" aria-label="Recent conversations">
                {agentWork.map((work) => (
                  <li key={work.threadId ?? "starting"}>
                    {work.threadId === null ? (
                      <div className="home-item" role="status">
                        <span className="home-item-copy">
                          <span>{work.title}</span>
                          <small className="home-work-status">
                            <ThinkingOrb
                              size={20}
                              state="working"
                              aria-hidden="true"
                            />
                            {work.detail}
                          </small>
                        </span>
                      </div>
                    ) : (
                      <button
                        type="button"
                        className="home-item"
                        disabled={!onOpenConversation}
                        onClick={() => {
                          if (work.threadId) onOpenConversation?.(work.threadId)
                        }}
                      >
                        <span className="home-item-copy">
                          <span>{work.title}</span>
                          <small
                            className={
                              work.running ? "home-work-status" : undefined
                            }
                          >
                            {work.running && (
                              <ThinkingOrb
                                size={20}
                                state="working"
                                aria-hidden="true"
                              />
                            )}
                            {work.detail}
                          </small>
                        </span>
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            ) : (
              threads.isSuccess && (
                <p className="home-quiet">Your conversations land here.</p>
              )
            )}
          </section>

          <section className="home-column" aria-labelledby="home-knowledge">
            <header className="home-column-head">
              <h2 id="home-knowledge">Knowledge</h2>
              <button type="button" onClick={onOpenKnowledge}>
                All
              </button>
            </header>
            <ReadNotice name="knowledge bases" query={corpora} />
            {knowledgeRows.length > 0 ? (
              <ul className="home-list">
                {knowledgeRows.map((base) => (
                  <li key={base.id}>
                    <button
                      type="button"
                      className="home-item"
                      onClick={onOpenKnowledge}
                    >
                      <span className="home-item-copy">
                        <span>{base.name}</span>
                        <small>
                          {base.document_count} document
                          {base.document_count === 1 ? "" : "s"} · Updated{" "}
                          {agoPhrase(base.updated_at, now).replace(
                            /^(Just|Yesterday)/,
                            (word) => word.toLowerCase()
                          )}
                        </small>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            ) : (
              corpora.isSuccess && (
                <p className="home-quiet">
                  Nothing to look up yet.{" "}
                  <button type="button" onClick={onOpenKnowledge}>
                    Add knowledge
                  </button>
                </p>
              )
            )}
          </section>

          <section className="home-column" aria-labelledby="home-files">
            <header className="home-column-head">
              <h2 id="home-files">Files</h2>
            </header>
            {agentFiles.length > 0 ? (
              <ul className="home-list">
                {agentFiles.map((file) => (
                  <li key={file.key}>
                    <button
                      type="button"
                      className="home-item"
                      disabled={!onOpenConversation}
                      onClick={() => onOpenConversation?.(file.threadId)}
                    >
                      <span className="home-item-copy">
                        <span>{file.name}</span>
                        <small>
                          {[
                            conversationTitle(
                              conversations.find(
                                (chat) => chat.id === file.threadId
                              )?.title
                            ),
                            agoPhrase(file.at, now),
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
              confirmations.every((query) => query.isSuccess) && (
                <p className="home-quiet">
                  Files your agent makes in recent conversations land here.
                </p>
              )
            )}
          </section>

          <section className="home-column" aria-labelledby="home-workflows">
            <header className="home-column-head">
              <h2 id="home-workflows">Recurring work</h2>
            </header>
            <ReadNotice name="recurring work" query={workflows} />
            {workflowRows.length > 0 ? (
              <ul className="home-list">
                {workflowRows.map((workflow) => (
                  <li key={workflow.id}>
                    <div className="home-item">
                      <span className="home-item-copy">
                        <span>{workflow.name}</span>
                        <small>
                          {workflow.status === "disabled"
                            ? "Paused"
                            : workflow.schedule &&
                                workflow.schedule.state === "enabled"
                              ? scheduleWords(
                                  workflow.schedule.expression,
                                  workflow.schedule.timezone
                                )
                              : "Runs when you start it"}
                        </small>
                      </span>
                    </div>
                  </li>
                ))}
              </ul>
            ) : (
              workflows.isSuccess && (
                <p className="home-quiet">Nothing runs on a schedule yet.</p>
              )
            )}
          </section>
        </div>
      )}

      {/* Slack and background access only feed the setup line: a read that
          failed or is not served here leaves its step out, quietly. */}
      {enabled && setup.length > 0 && (
        <p className="home-setup-line" aria-label="Finish setup">
          <span>Finish setup:</span>
          {setup.map((item, index) => (
            <span key={item.id}>
              {index > 0 && " · "}
              <button type="button" disabled={!item.open} onClick={item.open}>
                {item.title}
              </button>
            </span>
          ))}
        </p>
      )}
    </div>
  )
}
