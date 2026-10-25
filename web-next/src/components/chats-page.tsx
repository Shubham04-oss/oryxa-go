import { useMemo, useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { SquarePen } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { chatHistory, usePrimaryChatRunning } from "@/lib/threads/chat-choice"
import type { PrimaryChatTarget } from "@/lib/threads/chat-choice"
import {
  canRenameThread,
  threadApprovalQueryOptions,
  threadListQueryOptions,
} from "@/lib/threads/client"
import { deleteConversation, renameConversation } from "@/lib/threads/actions"
import type { Thread, ThreadDetail } from "@/lib/threads/client"
import { timeAgo } from "./agents-format"
import { FileKindBadge, fileKindLabel } from "./file-kind"
import { AgentChatMenu } from "./agent-chat-menu"
import { RunningDot } from "./agent-chat-history"
import "./chats-page.css"

/** Whether `at` falls on the member's today. */
function isToday(at: string, now = new Date()): boolean {
  const date = new Date(at)
  return (
    Number.isFinite(date.getTime()) &&
    date.toDateString() === now.toDateString()
  )
}

/**
 * What a row says about the files its chat kept, from Go's count (#388):
 * "3 files" and the latest one's name, or nothing while there are none or
 * Go does not count them. A 0 can mean "not counted yet" for an old thread,
 * so nothing is shown rather than a guess.
 */
export function chatFiles(
  thread: Pick<Thread, "artifact_count" | "latest_artifact">
): { label: string; latest?: string } | null {
  const count = thread.artifact_count
  if (typeof count !== "number" || !Number.isFinite(count) || count <= 0)
    return null
  return {
    label: count === 1 ? "1 file" : `${count} files`,
    latest: thread.latest_artifact?.name || undefined,
  }
}

/**
 * The conversations a title search keeps, newest first, in the day groups
 * the list shows: Today, then Earlier. An untitled conversation is listed
 * only while it is the open one or waits on the member, as history does.
 */
export function chatListGroups(
  threads: readonly Thread[],
  {
    query,
    selected,
    waiting,
    now = new Date(),
  }: {
    query: string
    selected?: string | null
    waiting: ReadonlySet<string>
    now?: Date
  }
): { label: string; threads: Thread[] }[] {
  const words = query.trim().toLowerCase()
  const rows = chatHistory(threads).filter(
    (thread) =>
      (thread.title.trim() !== "" ||
        waiting.has(thread.id) ||
        selected === thread.id) &&
      (!words ||
        (thread.title.trim() || "Untitled chat").toLowerCase().includes(words))
  )
  const today = rows.filter((thread) => isToday(thread.updated_at, now))
  const earlier = rows.filter((thread) => !isToday(thread.updated_at, now))
  return [
    { label: "Today", threads: today },
    { label: "Earlier", threads: earlier },
  ].filter((group) => group.threads.length > 0)
}

/**
 * The Chats page's list tile (ChatsNew): the member's conversations with
 * their agent, searchable by title, in Today and Earlier groups, the open
 * one raised, and New chat. Picking one moves the shared primary chat beside
 * it to that conversation, through the same queue Home uses, so its draft,
 * socket and unsent messages behave as they do anywhere else. A chat whose
 * run goes on carries a dot, and the member can leave it and come back
 * while it answers (C9, F10).
 */
export function ChatsList({
  organizationId,
  viewerId,
  agentName,
  selected,
  waitingThread,
  onOpen,
}: {
  organizationId: string
  viewerId: string
  agentName?: string
  /** The conversation the chat is on. */
  selected?: string | null
  /** The open conversation, when it waits on the member's approval. */
  waitingThread?: string | null
  /**
   * Once locked the list while a run worked. A running chat no longer holds
   * the member in it, so this is ignored; kept so callers need not change.
   */
  busy?: boolean
  onOpen: (target: PrimaryChatTarget) => void
}) {
  const queryClient = useQueryClient()
  const [query, setQuery] = useState("")
  const running = usePrimaryChatRunning({ organizationId, userId: viewerId })
  // The member's own list, as the chat reads it: the same cache entry.
  const threads = useQuery(threadListQueryOptions(organizationId, viewerId))
  // Approval markers come from what is already known (the open chat, and the
  // details the chat read for its opening rule); the list never fetches every
  // past transcript to mark itself.
  const waiting = useMemo(() => {
    const ids = new Set<string>()
    if (waitingThread) ids.add(waitingThread)
    for (const thread of threads.data ?? []) {
      const state = queryClient.getQueryState<ThreadDetail>(
        threadApprovalQueryOptions(organizationId, thread.id, viewerId).queryKey
      )
      if (
        state?.status === "success" &&
        state.data &&
        state.data.pending_confirmations.length > 0
      )
        ids.add(thread.id)
    }
    return ids
  }, [threads.data, waitingThread, queryClient, organizationId, viewerId])
  const groups = chatListGroups(threads.data ?? [], {
    query,
    selected,
    waiting,
  })

  return (
    <section className="chats-list" aria-label="Chats">
      <div className="chats-list-head">
        <Input
          className="chats-list-search"
          type="search"
          placeholder="Search chats"
          aria-label="Search chats"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <Button
          variant="outline"
          size="icon-sm"
          aria-label="New chat"
          title={agentName ? `New chat with ${agentName}` : "New chat"}
          onClick={() => onOpen({ kind: "new" })}
        >
          <SquarePen aria-hidden="true" />
        </Button>
      </div>
      <div className="chats-list-body">
        {threads.isPending ? (
          <p className="chats-list-note" role="status">
            Loading your chats…
          </p>
        ) : threads.isError ? (
          <p className="chats-list-note" role="alert">
            Couldn't load your chats.{" "}
            <Button
              variant="link"
              size="xs"
              onClick={() => void threads.refetch()}
            >
              Try again
            </Button>
          </p>
        ) : groups.length === 0 ? (
          <p className="chats-list-note">
            {query.trim()
              ? "No chat has that in its title."
              : agentName
                ? `Your chats with ${agentName} appear here.`
                : "Your chats with your agent appear here."}
          </p>
        ) : (
          groups.map((group) => (
            <div className="chats-list-group" key={group.label}>
              <p className="chats-list-group-label">{group.label}</p>
              <ul>
                {group.threads.map((thread) => {
                  const title = thread.title.trim() || "Untitled chat"
                  const current = selected === thread.id
                  const files = chatFiles(thread)
                  return (
                    <li key={thread.id} className="chats-list-item">
                      <Button
                        className="chats-list-row"
                        variant="ghost"
                        aria-current={current ? "true" : undefined}
                        onClick={() => {
                          if (!current)
                            onOpen({ kind: "thread", threadId: thread.id })
                        }}
                      >
                        <strong title={title}>
                          {running.has(thread.id) && <RunningDot />}
                          {title}
                        </strong>
                        <span className="chats-list-meta">
                          <time dateTime={thread.updated_at}>
                            {timeAgo(thread.updated_at)}
                          </time>
                          {files && (
                            <span className="chats-list-files">
                              {files.label}
                              {files.latest && (
                                <>
                                  <span aria-hidden="true">·</span>
                                  <FileKindBadge
                                    name={files.latest}
                                    size="small"
                                  />
                                  <span className="sr-only">
                                    , latest {fileKindLabel(files.latest)}
                                  </span>
                                </>
                              )}
                            </span>
                          )}
                          {waiting.has(thread.id) && (
                            <span className="chats-list-waiting">
                              Needs approval
                            </span>
                          )}
                        </span>
                      </Button>
                      <AgentChatMenu
                        className="chats-list-actions"
                        label={`Options for ${title}`}
                        title={title}
                        editableTitle={thread.title.trim()}
                        agentName={agentName}
                        disabled={false}
                        onRename={
                          canRenameThread(thread.id)
                            ? (next) =>
                                renameConversation(
                                  queryClient,
                                  organizationId,
                                  thread.id,
                                  next
                                )
                            : undefined
                        }
                        onDelete={async () => {
                          await deleteConversation(
                            queryClient,
                            organizationId,
                            thread.id
                          )
                          // The chat beside the list leaves it for New.
                          if (current) onOpen({ kind: "new" })
                        }}
                      />
                    </li>
                  )
                })}
              </ul>
            </div>
          ))
        )}
      </div>
    </section>
  )
}
