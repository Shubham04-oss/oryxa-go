import { useEffect, useRef, useState } from "react"
import type { Dispatch } from "react"
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query"
import { useHydrated } from "@tanstack/react-router"
import { Trash2 } from "lucide-react"
import { ThinkingOrb } from "thinking-orbs"
import { timeAgo } from "./agents-format"
import { ApiError } from "@/lib/api/client"
import {
  approvalCandidates,
  chatHistory,
  firstMessage,
  shownThread,
  usePrimaryChatActivity,
} from "@/lib/threads/chat-choice"
import type {
  ChatScope,
  PrimaryChat,
  PrimaryChatAction,
} from "@/lib/threads/chat-choice"
import {
  deleteThreadChat,
  threadApprovalQueryOptions,
  threadKeys,
  threadListQueryOptions,
} from "@/lib/threads/client"
import type { Thread, ThreadDetail } from "@/lib/threads/client"
import "./agent-chat-history.css"

/** An API refusal's own words, or `fallback` for anything without them. */
function failureText(error: unknown, fallback: string): string {
  return error instanceof ApiError && error.message !== "Request failed"
    ? error.message
    : fallback
}

/**
 * The member's conversations with their primary agent, newest first: the
 * sidebar's Recent list (DESIGN.md §2). Each row opens its conversation,
 * says when it was last active, and whether it waits on the member's
 * approval or their agent is working in it; delete asks inline first.
 */
export function AgentChatHistory({
  threads,
  selected,
  waiting = new Set(),
  working,
  loading,
  error,
  disabled,
  onRetry,
  onOpen,
  onDelete,
}: {
  threads: readonly Thread[]
  /** The conversation the center shows, if any. */
  selected?: string | null
  waiting?: ReadonlySet<string>
  /** The conversation the agent is working in now, as the open chat says. */
  working?: string
  loading?: boolean
  error?: string
  disabled?: boolean
  onRetry?: () => void
  onOpen: (id: string) => void
  onDelete: (id: string) => Promise<void>
}) {
  const [confirm, setConfirm] = useState<string>()
  const [deleting, setDeleting] = useState<string>()
  const [failure, setFailure] = useState("")
  const rows = threads
    .filter(
      (thread) =>
        thread.title.trim() !== "" ||
        waiting.has(thread.id) ||
        selected === thread.id
    )
    .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))
  async function remove(threadId: string) {
    setDeleting(threadId)
    setFailure("")
    try {
      await onDelete(threadId)
      setConfirm(undefined)
    } catch (cause) {
      setFailure(
        cause instanceof Error
          ? cause.message
          : "Couldn't delete this conversation. Try again."
      )
    } finally {
      setDeleting(undefined)
    }
  }
  return (
    <div className="workspace-chat-history">
      {loading ? (
        <p className="workspace-chat-history-note" role="status">
          Loading conversations…
        </p>
      ) : error ? (
        <p className="workspace-chat-history-note" role="status">
          {error}{" "}
          {onRetry && (
            <button type="button" onClick={onRetry}>
              Try again
            </button>
          )}
        </p>
      ) : rows.length === 0 ? (
        <p className="workspace-chat-history-note">
          No past conversations yet.
        </p>
      ) : (
        <ul>
          {rows.map((thread) => {
            const title = thread.title.trim() || "Untitled chat"
            const busy = disabled || Boolean(deleting)
            return (
              <li
                key={thread.id}
                data-current={selected === thread.id ? "true" : undefined}
              >
                <button
                  className="workspace-chat-history-open"
                  type="button"
                  aria-current={selected === thread.id ? "true" : undefined}
                  disabled={busy}
                  onClick={() => onOpen(thread.id)}
                >
                  <span className="workspace-chat-history-title" title={title}>
                    {title}
                  </span>
                  <span className="workspace-chat-history-meta">
                    {working === thread.id ? (
                      <span className="workspace-chat-history-working">
                        <ThinkingOrb
                          size={20}
                          style={{ width: 14, height: 14 }}
                          theme="dark"
                          state="working"
                          aria-hidden="true"
                        />
                        Working
                      </span>
                    ) : (
                      <>
                        {waiting.has(thread.id) && (
                          <span className="workspace-chat-history-waiting">
                            Needs approval ·{" "}
                          </span>
                        )}
                        <time dateTime={thread.updated_at}>
                          {timeAgo(thread.updated_at)}
                        </time>
                      </>
                    )}
                  </span>
                </button>
                {confirm === thread.id ? (
                  <span className="workspace-chat-history-confirm">
                    <button
                      type="button"
                      disabled={busy}
                      aria-label={`Confirm delete ${title}`}
                      onClick={() => void remove(thread.id)}
                    >
                      {deleting === thread.id ? "Deleting…" : "Delete"}
                    </button>
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => setConfirm(undefined)}
                    >
                      Cancel
                    </button>
                  </span>
                ) : (
                  <button
                    className="workspace-chat-history-delete"
                    type="button"
                    aria-label={`Delete ${title}`}
                    title="Delete conversation"
                    disabled={busy}
                    onClick={() => {
                      setConfirm(thread.id)
                      setFailure("")
                    }}
                  >
                    <Trash2 size={13} />
                  </button>
                )}
              </li>
            )
          })}
        </ul>
      )}
      {failure && (
        <p className="workspace-chat-history-note" role="alert">
          {failure}
        </p>
      )}
    </div>
  )
}

/**
 * The Recent list as the shell shows it, for the member's primary chat: their
 * own conversations, approval markers from the bounded check of the newest
 * five (and what earlier reads of theirs already hold, never a fetch of every
 * past transcript), the open chat's live state, and delete, which clears the
 * chat object before Go's thread. It reads only while it is shown.
 */
export function PrimaryChatHistory({
  organizationId,
  scope,
  chat,
  dispatch,
  current,
  onOpened,
}: {
  organizationId: string
  /** Whose conversations: this member's own cache entries only. */
  scope?: ChatScope
  chat: PrimaryChat
  dispatch: Dispatch<PrimaryChatAction>
  /** Whether the center shows the chat: only then is its conversation marked current. */
  current: boolean
  /** Told after the member opened a conversation. */
  onOpened?: (threadId: string) => void
}) {
  const queryClient = useQueryClient()
  const hydrated = useHydrated()
  const list = threadListQueryOptions(organizationId, scope?.userId)
  const threads = useQuery({ ...list, enabled: hydrated })
  const history = chatHistory(threads.data ?? [])
  const candidates = approvalCandidates(history)
  const details = useQueries({
    queries: candidates.map((thread) => ({
      ...threadApprovalQueryOptions(organizationId, thread.id, scope?.userId),
      enabled: hydrated,
    })),
  })
  const known = new Map(
    candidates.map((thread, i) => [
      thread.id,
      details[i].isSuccess ? details[i].data : undefined,
    ])
  )
  const waiting = new Set(
    history
      .filter((thread) => {
        // A refused candidate must not recover its marker from retained data.
        // Older chats may be known from this member's earlier bounded reads;
        // reading their cache never fans out more network requests.
        const cached = known.has(thread.id)
          ? undefined
          : queryClient.getQueryState<ThreadDetail>(
              threadApprovalQueryOptions(
                organizationId,
                thread.id,
                scope?.userId
              ).queryKey
            )
        const detail = known.has(thread.id)
          ? known.get(thread.id)
          : cached?.status === "success"
            ? cached.data
            : undefined
        return (detail?.pending_confirmations.length ?? 0) > 0
      })
      .map((thread) => thread.id)
  )
  // The open chat says at once what it waits on or works at.
  const activity = usePrimaryChatActivity(
    scope ?? { organizationId: "", userId: "" }
  )
  if (activity?.state === "waiting" && activity.threadId)
    waiting.add(activity.threadId)
  const working =
    activity?.state === "running" && activity.threadId
      ? activity.threadId
      : undefined
  const shown = shownThread(chat)
  // Delete answers later: it asks what is open then, not when it was pressed.
  const latestShown = useRef(shown)
  useEffect(() => {
    latestShown.current = shown
  })

  return (
    <AgentChatHistory
      threads={history}
      selected={current ? shown : undefined}
      waiting={waiting}
      working={working}
      loading={threads.isPending}
      error={
        threads.isError
          ? failureText(threads.error, "Couldn't load your conversations.")
          : undefined
      }
      onRetry={() => void threads.refetch()}
      // A first message on its way goes first: the chat can't be moved
      // until its conversation has it.
      disabled={firstMessage(chat) !== null}
      onOpen={(id) => {
        dispatch({ type: "move", thread: id })
        onOpened?.(id)
      }}
      onDelete={async (id) => {
        await deleteThreadChat(organizationId, id)
        await queryClient.cancelQueries({ queryKey: list.queryKey })
        queryClient.setQueryData<Thread[]>(list.queryKey, (old) =>
          old?.filter((thread) => thread.id !== id)
        )
        queryClient.removeQueries({
          queryKey: threadKeys.detail(organizationId, id),
        })
        if (latestShown.current === id) dispatch({ type: "move", thread: null })
        void queryClient.invalidateQueries({
          queryKey: threadKeys.list(organizationId),
        })
      }}
    />
  )
}
