import { useEffect, useId, useState } from "react"
import { ChevronDown, Trash2 } from "lucide-react"
import { timeAgo } from "./agents-format"
import type { Thread } from "@/lib/threads/client"
import "./agent-chat-history.css"

/** A compact, optional view of the agent's real conversations. */
export function AgentChatHistory({
  threads,
  selected,
  waiting = new Set(),
  loading,
  error,
  disabled,
  onRetry,
  onOpen,
  onDelete,
  openRequest,
  onOpened,
}: {
  threads: readonly Thread[]
  selected?: string | null
  waiting?: ReadonlySet<string>
  loading?: boolean
  error?: string
  disabled?: boolean
  onRetry?: () => void
  onOpen: (id: string) => void
  onDelete: (id: string) => Promise<void>
  /** An explicit Home shortcut, consumed once without changing the chat. */
  openRequest?: object | null
  onOpened?: () => void
}) {
  const id = useId()
  const [expanded, setExpanded] = useState(false)
  const [confirm, setConfirm] = useState<string>()
  const [deleting, setDeleting] = useState<string>()
  const [failure, setFailure] = useState("")
  useEffect(() => {
    if (!openRequest) return
    setExpanded(true)
    onOpened?.()
  }, [openRequest, onOpened])
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
    <section
      className="workspace-chat-history"
      aria-label="Conversation history"
    >
      <button
        className="workspace-chat-history-toggle"
        type="button"
        aria-expanded={expanded}
        aria-controls={id}
        onClick={() => setExpanded(!expanded)}
      >
        <ChevronDown size={13} data-expanded={expanded} /> History
      </button>
      {expanded && (
        <div id={id} className="workspace-chat-history-list">
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
                  <li key={thread.id}>
                    <button
                      className="workspace-chat-history-open"
                      type="button"
                      aria-current={selected === thread.id ? "true" : undefined}
                      disabled={busy}
                      onClick={() => onOpen(thread.id)}
                    >
                      <span title={title}>{title}</span>
                      <span className="workspace-chat-history-meta">
                        {waiting.has(thread.id) && (
                          <span>Needs approval · </span>
                        )}
                        <time dateTime={thread.updated_at}>
                          {timeAgo(thread.updated_at)}
                        </time>
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
      )}
    </section>
  )
}
