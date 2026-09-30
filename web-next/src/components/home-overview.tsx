import { useState } from "react"
import { useQueries, useQuery } from "@tanstack/react-query"
import type {
  QueryFunctionContext,
  UseQueryOptions,
} from "@tanstack/react-query"
import { useHydrated } from "@tanstack/react-router"
import { ThinkingOrb } from "thinking-orbs"
import { AgentComposer } from "./agent-composer"
import { ApiError } from "@/lib/api/client"
import {
  AGENT_REQUESTS_POLL_MS,
  agentRequestsQueryOptions,
} from "@/lib/agents/queries"
import { isUnavailableHere } from "@/lib/agents/policy"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import { connectionsQueryOptions, toolkitName } from "@/lib/apps/client"
import { corporaQueryOptions, listDocuments, ragKeys } from "@/lib/rag/client"
import {
  retryUnlessRefused,
  threadListQueryOptions,
} from "@/lib/threads/client"
import type { PrimaryChatActivity } from "@/lib/threads/chat-choice"
import "./home-overview.css"

const requestLimit = 100
const basesToCheck = 3
const documentsToCheck = 3

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
  if (query.isPending || query.isFetching) {
    return (
      <p className="home-quiet" role="status">
        Loading {name}…
      </p>
    )
  }
  if (!query.isError) return null
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

function currentData<T>(query: {
  data?: T
  isSuccess: boolean
  isFetching: boolean
}): T | undefined {
  // React Query retains prior data after a refused refresh. It is not a fresh
  // authorization to show that member's private work or offer its actions.
  return query.isSuccess && !query.isFetching ? query.data : undefined
}

function privateRead<T, TKey extends readonly unknown[]>(
  options: Pick<UseQueryOptions<T, unknown, T, TKey>, "queryKey" | "queryFn">,
  viewerId: string
) {
  const queryFn = options.queryFn
  return {
    queryKey: [...options.queryKey, "home", viewerId],
    queryFn: (context: QueryFunctionContext) => {
      if (typeof queryFn !== "function")
        throw new Error("No Home read configured")
      return queryFn({ ...context, queryKey: options.queryKey })
    },
    retry: retryUnlessRefused,
    staleTime: 0,
    refetchOnMount: "always" as const,
    refetchIntervalInBackground: false,
  }
}

function relativeTime(timestamp: string) {
  const time = Date.parse(timestamp)
  if (!Number.isFinite(time)) return "Recent conversation"
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
  }).format(time)
}

export function HomeOverview({
  organizationId,
  viewerId,
  primaryChatActivity,
  onNewChat,
  onOpenRequests,
  onOpenAgent,
  onOpenConversation,
  onOpenTools,
  onOpenKnowledge,
}: {
  organizationId?: string
  viewerId?: string
  primaryChatActivity?: PrimaryChatActivity | null
  onNewChat?: (message?: string) => void
  onOpenRequests: (requestId?: string) => void
  onOpenAgent: () => void
  onOpenConversation?: (threadId: string) => void
  onOpenTools: () => void
  onOpenKnowledge: () => void
}) {
  const hydrated = useHydrated()
  const enabled = hydrated && Boolean(organizationId && viewerId)
  const org = organizationId ?? ""
  const viewer = viewerId ?? ""
  const [draft, setDraft] = useState("")
  const inboxOptions = agentRequestsQueryOptions(org, "inbox", requestLimit)
  const inbox = useQuery({
    ...privateRead(inboxOptions, viewer),
    enabled,
    refetchInterval: AGENT_REQUESTS_POLL_MS,
  })
  const threads = useQuery({
    ...privateRead(threadListQueryOptions(org, viewer), viewer),
    enabled,
  })
  const connections = useQuery({
    ...privateRead(connectionsQueryOptions(org), viewer),
    enabled,
    refetchInterval: 30_000,
  })
  const corpora = useQuery({
    ...privateRead(corporaQueryOptions(org), viewer),
    enabled,
    refetchInterval: 30_000,
  })
  const inboxRows = currentData(inbox) ?? []
  const corpusRows = currentData(corpora) ?? []
  // No aggregate status endpoint exists. Check a fixed number of nonempty
  // bases, with a status filter so newer ready documents cannot hide a failure.
  const nonemptyBases = corpusRows.filter((base) => base.document_count > 0)
  const checkedBases = nonemptyBases.slice(0, basesToCheck)
  const failures = useQueries({
    queries: checkedBases.map((base) => ({
      queryKey: [
        ...ragKeys.documents(org, base.id),
        "home-failed",
        documentsToCheck,
        viewer,
      ],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        listDocuments(org, base.id, {
          status: "failed",
          limit: documentsToCheck,
          signal,
        }),
      enabled,
      retry: retryUnlessRefused,
      staleTime: 0,
      refetchOnMount: "always" as const,
      refetchInterval: 30_000,
      refetchIntervalInBackground: false,
    })),
  })
  const waitingRequests = inboxRows.filter((request) =>
    waitsOnMember(request, viewerId ?? "", Date.now())
  )
  const conversations = (currentData(threads) ?? []).filter(
    (thread) => !thread.origin || thread.origin === "chat"
  )
  // Activity describes the selected primary chat, not all background work.
  const running =
    primaryChatActivity?.state === "running" ? primaryChatActivity : null
  const waitingChat =
    primaryChatActivity?.state === "waiting" ? primaryChatActivity : null
  const activityTitle =
    conversations.find((thread) => thread.id === primaryChatActivity?.threadId)
      ?.title || "Current conversation"
  const waitingThreadId = waitingChat?.threadId ?? null
  const openWaitingConversation =
    waitingThreadId !== null && onOpenConversation
      ? () => onOpenConversation(waitingThreadId)
      : undefined
  const waiting = [
    ...(waitingChat
      ? [
          {
            id: `chat:${waitingChat.threadId ?? "pending"}`,
            title: activityTitle,
            detail: "Needs confirmation",
            open: openWaitingConversation,
          },
        ]
      : []),
    ...waitingRequests.slice(0, waitingChat ? 2 : 3).map((request) => ({
      id: `request:${request.id}`,
      title: request.purpose || request.task,
      detail: `${request.status === "review" ? "Answer to review" : "Request to approve"} · ${request.requester.agent_name}`,
      open: () => onOpenRequests(request.id),
    })),
  ]
  const waitingCount = waitingRequests.length + (waitingChat ? 1 : 0)
  const incompleteRequests =
    !inbox.isSuccess || inbox.isFetching || inboxRows.length >= requestLimit
  const agentWork = [
    ...(running
      ? [
          {
            threadId: running.threadId,
            title:
              running.threadId === null
                ? "Starting conversation"
                : activityTitle,
            detail: "Working",
            running: true,
          },
        ]
      : []),
    ...conversations
      .filter(
        (thread) =>
          thread.id !== running?.threadId && thread.id !== waitingChat?.threadId
      )
      .slice(0, running ? 2 : 3)
      .map((thread) => ({
        threadId: thread.id,
        title: thread.title || "New conversation",
        detail: relativeTime(thread.updated_at),
        running: false,
      })),
  ].slice(0, 3)
  const toolProblems = (currentData(connections) ?? []).filter(
    (connection) =>
      connection.status === "failed" || connection.status === "expired"
  )
  const baseProblems = checkedBases.flatMap((base, index) => {
    const page = currentData(failures[index])
    return page?.documents.length ? [{ base, page }] : []
  })
  const attention = [
    ...toolProblems.map((connection) => ({
      id: connection.id,
      title: connection.label || toolkitName(connection.toolkit),
      detail:
        connection.status === "expired"
          ? "Connection expired"
          : "Connection failed",
      open: onOpenTools,
    })),
    ...baseProblems.map(({ base, page }) => ({
      id: base.id,
      title: base.name,
      detail: `${page.documents.length}${page.next_cursor ? "+" : ""} failed document${page.documents.length === 1 && !page.next_cursor ? "" : "s"}`,
      open: onOpenKnowledge,
    })),
  ]
  const incompleteKnowledge =
    !corpora.isSuccess ||
    corpora.isFetching ||
    nonemptyBases.length > checkedBases.length ||
    failures.some((query) => !query.isSuccess || query.isFetching)
  const settled = [inbox, threads, connections, corpora, ...failures].every(
    (query) => query.isSuccess && !query.isFetching
  )
  const needsMember = waitingCount > 0 || attention.length > 0
  // A base with no visible documents cannot contain a visible failed document.
  const checkedCount =
    corpusRows.length -
    nonemptyBases.length +
    failures.filter((query) => query.isSuccess && !query.isFetching).length
  const coverage =
    corpusRows.length > 0
      ? `Checked ${checkedCount} of ${corpusRows.length} knowledge bases.`
      : ""

  return (
    <div className="home-overview">
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
      {enabled ? (
        <>
          <ReadNotice name="requests" query={inbox} />
          {waiting.length > 0 && (
            <section className="home-card" aria-label="Waiting on you">
              <header>
                <h2>
                  Waiting on you · {waitingCount}
                  {incompleteRequests ? "+" : ""}
                </h2>
                <div>
                  {waitingChat && (
                    <button
                      type="button"
                      aria-label="All confirmations"
                      disabled={!openWaitingConversation}
                      onClick={openWaitingConversation}
                    >
                      {waitingRequests.length > 0 ? "All confirmations" : "All"}
                    </button>
                  )}
                  {waitingRequests.length > 0 && (
                    <button type="button" onClick={() => onOpenRequests()}>
                      All requests
                    </button>
                  )}
                </div>
              </header>
              <ul>
                {waiting.map((item) => (
                  <li key={item.id} className="home-row">
                    {item.open ? (
                      <button type="button" onClick={item.open}>
                        <span>{item.title}</span> <small>{item.detail}</small>
                      </button>
                    ) : (
                      <div role="status">
                        <span>{item.title}</span> <small>{item.detail}</small>
                      </div>
                    )}
                  </li>
                ))}
              </ul>
              {inboxRows.length >= requestLimit && (
                <p className="home-quiet">
                  Only the latest {requestLimit} requests were checked.
                </p>
              )}
            </section>
          )}
          <ReadNotice name="agent work" query={threads} />
          {agentWork.length > 0 && (
            <section className="home-card" aria-label="Your agent">
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
                  <button type="button" onClick={onOpenAgent}>
                    All
                  </button>
                </div>
              </header>
              <ul>
                {agentWork.map((work) => (
                  <li key={work.threadId ?? "starting"} className="home-row">
                    {work.threadId === null ? (
                      <div role="status">
                        <span>{work.title}</span>{" "}
                        <small
                          className={
                            work.running ? "home-work-status" : undefined
                          }
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
                      </div>
                    ) : (
                      <button
                        type="button"
                        disabled={!onOpenConversation}
                        onClick={() => {
                          if (work.threadId !== null)
                            onOpenConversation?.(work.threadId)
                        }}
                      >
                        <span>{work.title}</span>{" "}
                        <small
                          className={
                            work.running ? "home-work-status" : undefined
                          }
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
            </section>
          )}
          <ReadNotice name="tools" query={connections} />
          <ReadNotice name="knowledge bases" query={corpora} />
          {failures.map((query, index) => (
            <ReadNotice
              key={checkedBases[index].id}
              name={`${checkedBases[index].name} failures`}
              query={query}
            />
          ))}
          {attention.length > 0 && (
            <section className="home-card" aria-label="Needs attention">
              <header>
                <h2>
                  Needs attention · {attention.length}
                  {incompleteKnowledge ? "+" : ""}
                </h2>
                <div>
                  {toolProblems.length > 0 && (
                    <button type="button" onClick={onOpenTools}>
                      All tools
                    </button>
                  )}
                  {baseProblems.length > 0 && (
                    <button type="button" onClick={onOpenKnowledge}>
                      All knowledge bases
                    </button>
                  )}
                </div>
              </header>
              <ul>
                {attention.slice(0, 3).map((item) => (
                  <li key={item.id} className="home-row">
                    <button type="button" onClick={item.open}>
                      <span>{item.title}</span>
                      <small>{item.detail}</small>
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          )}
          {needsMember && coverage && <p className="home-quiet">{coverage}</p>}
          {needsMember &&
            inboxRows.length >= requestLimit &&
            waitingCount === 0 && (
              <p className="home-quiet">
                Only the latest {requestLimit} requests were checked.
              </p>
            )}
          {settled && !needsMember && (
            <p className="home-quiet" role="status">
              Nothing waiting in the work checked.
              {coverage ? ` ${coverage}` : ""}
              {inboxRows.length >= requestLimit
                ? ` Only the latest ${requestLimit} requests were checked.`
                : ""}
            </p>
          )}
        </>
      ) : null}
    </div>
  )
}
