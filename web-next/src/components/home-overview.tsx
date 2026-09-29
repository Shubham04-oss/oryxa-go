import { useState } from "react"
import { useQueries, useQuery } from "@tanstack/react-query"
import { useHydrated } from "@tanstack/react-router"
import { AgentComposer } from "./agent-composer"
import { ApiError } from "@/lib/api/client"
import { agentRequestsQueryOptions } from "@/lib/agents/queries"
import { isUnavailableHere } from "@/lib/agents/policy"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import { connectionsQueryOptions, toolkitName } from "@/lib/apps/client"
import { corporaQueryOptions, listDocuments, ragKeys } from "@/lib/rag/client"
import {
  retryUnlessRefused,
  threadListQueryOptions,
} from "@/lib/threads/client"
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
  isError: boolean
  error: unknown
  refetch: () => unknown
}

function ReadNotice({ name, query }: { name: string; query: ReadState }) {
  if (query.isPending) {
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
  agentRunning = false,
  onNewChat,
  onOpenRequests,
  onOpenAgent,
  onOpenConversation,
  onOpenTools,
  onOpenKnowledge,
}: {
  organizationId?: string
  viewerId?: string
  agentRunning?: boolean
  onNewChat?: (message?: string) => void
  onOpenRequests: () => void
  onOpenAgent: () => void
  onOpenConversation?: (threadId: string) => void
  onOpenTools: () => void
  onOpenKnowledge: () => void
}) {
  const hydrated = useHydrated()
  const enabled = hydrated && Boolean(organizationId && viewerId)
  const org = organizationId ?? ""
  const [draft, setDraft] = useState("")
  const inbox = useQuery({
    ...agentRequestsQueryOptions(org, "inbox", requestLimit),
    enabled,
    retry: retryUnlessRefused,
    refetchIntervalInBackground: false,
  })
  const threads = useQuery({ ...threadListQueryOptions(org), enabled })
  const connections = useQuery({
    ...connectionsQueryOptions(org),
    enabled,
    retry: retryUnlessRefused,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
  })
  const corpora = useQuery({
    ...corporaQueryOptions(org),
    enabled,
    retry: retryUnlessRefused,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
  })
  // No aggregate status endpoint exists. Check a fixed number of nonempty
  // bases, with a status filter so newer ready documents cannot hide a failure.
  const nonemptyBases = (corpora.data ?? []).filter(
    (base) => base.document_count > 0
  )
  const checkedBases = nonemptyBases.slice(0, basesToCheck)
  const failures = useQueries({
    queries: checkedBases.map((base) => ({
      queryKey: [
        ...ragKeys.documents(org, base.id),
        "home-failed",
        documentsToCheck,
      ],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        listDocuments(org, base.id, {
          status: "failed",
          limit: documentsToCheck,
          signal,
        }),
      enabled,
      retry: retryUnlessRefused,
      refetchInterval: 30_000,
      refetchIntervalInBackground: false,
    })),
  })
  const waiting = (inbox.data ?? []).filter((request) =>
    waitsOnMember(request, viewerId ?? "", Date.now())
  )
  const recent = (threads.data ?? []).slice(0, 3)
  const toolProblems = (connections.data ?? []).filter(
    (connection) =>
      connection.status === "failed" || connection.status === "expired"
  )
  const baseProblems = checkedBases.flatMap((base, index) => {
    const page = failures[index]?.data
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
    corpora.isPending ||
    corpora.isError ||
    nonemptyBases.length > checkedBases.length ||
    failures.some((query) => query.isPending || query.isError)
  const settled = [inbox, threads, connections, corpora, ...failures].every(
    (query) => !query.isPending && !query.isError
  )
  const needsMember = waiting.length > 0 || attention.length > 0
  // A base with no visible documents cannot contain a visible failed document.
  const checkedCount =
    (corpora.data ?? []).length -
    nonemptyBases.length +
    failures.filter((query) => query.isSuccess).length
  const coverage =
    (corpora.data?.length ?? 0) > 0
      ? `Checked ${checkedCount} of ${corpora.data!.length} knowledge bases.`
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
          <ReadNotice name="agent work" query={threads} />
          {(agentRunning || recent.length > 0) && (
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
              {agentRunning && (
                <p className="home-quiet" role="status">
                  Your agent is working.
                </p>
              )}
              <ul>
                {recent.map((thread) => (
                  <li key={thread.id} className="home-row">
                    <button
                      type="button"
                      disabled={!onOpenConversation}
                      onClick={() => onOpenConversation?.(thread.id)}
                    >
                      <span>{thread.title || "New conversation"}</span>
                      <small>{relativeTime(thread.updated_at)}</small>
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          )}
          <ReadNotice name="requests" query={inbox} />
          {waiting.length > 0 && (
            <section className="home-card" aria-label="Waiting on you">
              <header>
                <h2>
                  Waiting on you · {waiting.length}
                  {(inbox.data?.length ?? 0) >= requestLimit ? "+" : ""}
                </h2>
                <button type="button" onClick={onOpenRequests}>
                  All
                </button>
              </header>
              <ul>
                {waiting.slice(0, 3).map((request) => (
                  <li key={request.id} className="home-row">
                    <button type="button" onClick={onOpenRequests}>
                      <span>{request.purpose || request.task}</span>
                      <small>
                        {request.status === "review"
                          ? "Answer to review"
                          : "Request to approve"}{" "}
                        · {request.requester.agent_name}
                      </small>
                    </button>
                  </li>
                ))}
              </ul>
              {(inbox.data?.length ?? 0) >= requestLimit && (
                <p className="home-quiet">
                  From the latest {requestLimit} requests.
                </p>
              )}
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
            (inbox.data?.length ?? 0) >= requestLimit &&
            waiting.length === 0 && (
              <p className="home-quiet">
                Only the latest {requestLimit} requests were checked.
              </p>
            )}
          {settled && !needsMember && (
            <p className="home-quiet" role="status">
              Nothing waiting in the work checked.
              {coverage ? ` ${coverage}` : ""}
              {(inbox.data?.length ?? 0) >= requestLimit
                ? ` Only the latest ${requestLimit} requests were checked.`
                : ""}
            </p>
          )}
        </>
      ) : null}
    </div>
  )
}
